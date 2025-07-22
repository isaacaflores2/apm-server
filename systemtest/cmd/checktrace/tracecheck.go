package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/elastic/apm-data/model/modelpb"
	beaterconfig "github.com/elastic/apm-server/internal/beater/config"
	"github.com/elastic/apm-server/x-pack/apm-server/sampling"
	"github.com/elastic/apm-server/x-pack/apm-server/sampling/eventstorage"
	"github.com/elastic/apm-server/x-pack/apm-server/sampling/pubsub/pubsubtest"
	"github.com/elastic/beats/v7/libbeat/cfgfile"
	agentconfig "github.com/elastic/elastic-agent-libs/config"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/paths"
)

// TraceSummary contains an analysis of each trace and its sampling decision
type TraceSummary struct {
	TraceID            string
	EventType          modelpb.APMEventType
	ParentCount        int
	ChildCount         int
	ParentServiceName  string
	ParentEnvironment  string
	ParentEventOutcome string
	Sampled            bool
}

func runTraceCheck(inputFilePath string, configFilePath string) error {
	logger := logp.NewLogger("checktrace")

	// Read and process trace events from the input file
	if inputFilePath == "" {
		return fmt.Errorf("input file is required")
	}

	batch, err := parseInputFileAsAPMEvents(inputFilePath, logger)
	if err != nil {
		return fmt.Errorf("failed to read events from file: %w", err)
	}

	// Store a copy of the batch for analysis after processing
	originalBatch := make(modelpb.Batch, len(*batch))
	copy(originalBatch, *batch)

	// Load apm-server configuration
	apmServerConfig, err := loadConfigFromFile(configFilePath, logger)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Setup local event storage
	storageDir := paths.Resolve(paths.Data, "checktrace-storage")
	db, err := eventstorage.NewStorageManager(storageDir, logger)
	if err != nil {
		return fmt.Errorf("failed to create storage manager: %w", err)
	}
	defer db.Close()

	summaries, err := summarizeAndProcessEvents(apmServerConfig, db, &originalBatch, logger)
	if err != nil {
		return fmt.Errorf("failed to summarize and process events: %w", err)
	}

	err = printTraceSummaries(summaries)
	if err != nil {
		return fmt.Errorf("failed to print trace summaries: %w", err)
	}
	return nil
}

// loadConfigFromFile loads configuration from the specified YAML file
// If configFilePath is empty, creates a default configuration
func loadConfigFromFile(configFilePath string, logger *logp.Logger) (*beaterconfig.Config, error) {
	var cfg *agentconfig.C
	var err error

	// Either load from file or create default config
	if configFilePath == "" {
		fmt.Println("No config file specified, using default config.")
		cfg, err = createDefaultConfig()
		if err != nil {
			return nil, fmt.Errorf("error creating default config: %w", err)
		}
	} else {
		// Load from file
		cfg, err = cfgfile.Load(configFilePath, nil)
		if err != nil {
			return nil, fmt.Errorf("error loading config file: %w", err)
		}
	}

	// Unpack config
	var unpackedConfig struct {
		APMServer  *agentconfig.C        `config:"apm-server"`
		Output     agentconfig.Namespace `config:"output"`
		DataStream struct {
			Namespace string `config:"namespace"`
		} `config:"data_stream"`
	}
	if err := cfg.Unpack(&unpackedConfig); err != nil {
		return nil, fmt.Errorf("failed to unpack config: %w", err)
	}

	var elasticsearchOutputConfig *agentconfig.C
	if unpackedConfig.Output.Name() == "elasticsearch" {
		elasticsearchOutputConfig = unpackedConfig.Output.Config()
	}

	finalCfg, err := beaterconfig.NewConfig(unpackedConfig.APMServer, elasticsearchOutputConfig, logger)
	if err != nil {
		return nil, err
	}
	return finalCfg, nil
}

func createDefaultConfig() (*agentconfig.C, error) {

	// Create default configuration YAML
	defaultConfigYAML := `
apm-server:
  host: "127.0.0.1:8200"
  sampling:
    tail:
      enabled: true
      interval: 1s
      policies:
        - sample_rate: 1.0
`
	cfg, err := agentconfig.NewConfigFrom(defaultConfigYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create default config: %w", err)
	}
	return cfg, err
}

func summarizeAndProcessEvents(apmServerConfig *beaterconfig.Config, db *eventstorage.StorageManager, batch *modelpb.Batch, logger *logp.Logger) ([]TraceSummary, error) {
	if apmServerConfig == nil {
		return nil, fmt.Errorf("apm-server config is required")
	}
	if db == nil {
		return nil, fmt.Errorf("storage manager is required")
	}
	if len(*batch) == 0 {
		return nil, nil
	}

	// Store a copy of the batch for analysis after processing
	originalBatch := make(modelpb.Batch, len(*batch))
	copy(originalBatch, *batch)

	// Generate the initial summary of each trace
	initialSummary, err := generateInitialSummary(&originalBatch)
	if err != nil {
		return nil, fmt.Errorf("failed to generate pre-summaries: %w", err)
	}

	// Create and the start sampling processor
	processor, eventStore, err := createSamplingProcessor(apmServerConfig, db, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create sampling processor: %w", err)
	}
	go func() {
		if err = processor.Run(); err != nil {
			logger.Error(err)
		}
	}()

	// Process the events with the sampling processor
	err = processor.ProcessBatch(context.Background(), batch)
	if err != nil {
		return nil, fmt.Errorf("failed to process events: %w", err)
	}

	// Stop the processor to trigger finalization
	if err := processor.Stop(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to stop processor: %w", err)
	}

	// Wait for sampling finalization to complete
	// TODO(isaacaflores2): We can poll event storage until we find a traceID or subscribe to the pubsub channel to get all sampled traces
	time.Sleep(time.Second)

	// Use the event store to get the sampling decisions
	// and add them to the summary
	return addSamplingDecision(initialSummary, eventStore), nil
}

// createSamplingProcessor creates and configures a sampling processor
func createSamplingProcessor(apmServerConfig *beaterconfig.Config, db *eventstorage.StorageManager, logger *logp.Logger) (*sampling.Processor, eventstorage.RW, error) {
	tailSamplingConfig := apmServerConfig.Sampling.Tail

	// Initialize policies from the config
	var policies []sampling.Policy
	if len(tailSamplingConfig.Policies) > 0 {
		policies = make([]sampling.Policy, len(tailSamplingConfig.Policies))
		for i, in := range tailSamplingConfig.Policies {
			policies[i] = sampling.Policy{
				PolicyCriteria: sampling.PolicyCriteria{
					ServiceName:        in.Service.Name,
					ServiceEnvironment: in.Service.Environment,
					TraceName:          in.Trace.Name,
					TraceOutcome:       in.Trace.Outcome,
				},
				SampleRate: in.SampleRate,
			}
		}
	} else {
		fmt.Println("No policies defined in config, using default policy")
		policies = []sampling.Policy{
			{
				PolicyCriteria: sampling.PolicyCriteria{}, // Empty criteria matches all traces
				SampleRate:     1.0,                       // 100% sampling rate
			},
		}
	}

	// use a noop batch processor since we do not need to index events
	noopBatchProcessor := modelpb.ProcessBatchFunc(func(ctx context.Context, batch *modelpb.Batch) error {
		return nil
	})

	samplingConfig := sampling.Config{
		BatchProcessor: noopBatchProcessor,
		MeterProvider:  noop.NewMeterProvider(),
		LocalSamplingConfig: sampling.LocalSamplingConfig{
			FlushInterval:         tailSamplingConfig.Interval,
			MaxDynamicServices:    1000,
			Policies:              policies,
			IngestRateDecayFactor: tailSamplingConfig.IngestRateDecayFactor,
		},
		RemoteSamplingConfig: sampling.RemoteSamplingConfig{
			CompressionLevel: tailSamplingConfig.ESConfig.CompressionLevel,
			Elasticsearch:    pubsubtest.Client(nil, nil),
			SampledTracesDataStream: sampling.DataStreamConfig{
				Type:      "traces",
				Dataset:   "apm.sampled",
				Namespace: "default",
			},
			UUID: "checktrace",
		},
		StorageConfig: sampling.StorageConfig{
			DB:                    db,
			Storage:               db.NewReadWriter(tailSamplingConfig.StorageLimitParsed, tailSamplingConfig.DiskUsageThreshold),
			TTL:                   tailSamplingConfig.TTL,
			DiscardOnWriteFailure: tailSamplingConfig.DiscardOnWriteFailure,
		},
	}

	processor, err := sampling.NewProcessor(samplingConfig, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sampling processor: %w", err)
	}
	return processor, samplingConfig.StorageConfig.Storage, nil
}

// parseInputFileAsAPMEvents reads trace event documents from the input file path.
// The file is expected to contain trace events after they have been indexed into Elasticsearch.
// The trace event documents will be converted to APMEvents.
// Note: APMEvents are created with just enough source data to trigger sampling.
func parseInputFileAsAPMEvents(filepath string, logger *logp.Logger) (*modelpb.Batch, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var traceDocs []TraceDoc
	err = json.Unmarshal(content, &traceDocs)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Elasticsearch response: %w", err)
	}

	batch := &modelpb.Batch{}
	for _, traceDoc := range traceDocs {
		*batch = append(*batch, createAPMEventFromSource(traceDoc))
	}
	return batch, nil
}

// createAPMEventFromSource creates a APMEvent with minimal source data
func createAPMEventFromSource(traceDoc TraceDoc) *modelpb.APMEvent {
	traceSource := traceDoc.Source
	event := &modelpb.APMEvent{
		Trace:    &modelpb.Trace{Id: traceSource.Trace.ID},
		ParentId: traceSource.Parent.ID,
		Service: &modelpb.Service{
			Name:        traceSource.Service.Name,
			Environment: traceSource.Service.Environment,
		},
		Event: &modelpb.Event{
			Outcome: traceSource.Event.Outcome,
		},
	}

	// Handle processor-specific fields
	switch traceSource.Processor.Event {
	case "transaction":
		event.Transaction = &modelpb.Transaction{
			Id:      traceSource.Transaction.ID,
			Type:    traceSource.Transaction.Type,
			Sampled: traceSource.Transaction.Sampled,
		}
	case "span":
		event.Span = &modelpb.Span{
			Id:   traceSource.Span.ID,
			Type: traceSource.Span.Type,
		}
	}
	return event
}

// generateInitialSummary analyzes each trace and generates pre-summaries before sampling
func generateInitialSummary(batch *modelpb.Batch) ([]TraceSummary, error) {
	// Group events by trace ID
	traceEvents := make(map[string][]*modelpb.APMEvent)
	for _, event := range *batch {
		if event.Trace != nil && event.Trace.Id != "" {
			traceID := event.Trace.Id
			traceEvents[traceID] = append(traceEvents[traceID], event)
		}
	}

	// Analyze each trace
	var preSummaries = make([]TraceSummary, 0, len(traceEvents))
	for traceID, events := range traceEvents {
		summary := TraceSummary{
			TraceID:            traceID,
			EventType:          modelpb.UndefinedEventType,
			ParentCount:        0,
			ChildCount:         0,
			ParentServiceName:  "unknown",
			ParentEnvironment:  "unknown",
			ParentEventOutcome: "unknown",
		}

		// extract relevant info from each event
		for _, event := range events {
			if event.ParentId == "" {
				summary.ParentCount++
				summary.EventType = event.Type()
				service := event.GetService()
				if service.GetName() != "" {
					summary.ParentServiceName = service.GetName()
				}
				if service.GetEnvironment() != "" {
					summary.ParentEnvironment = service.GetEnvironment()
				}
				if event.GetEvent().GetOutcome() != "" {
					summary.ParentEventOutcome = event.GetEvent().GetOutcome()
				}
			} else {
				summary.ChildCount++
			}
		}
		preSummaries = append(preSummaries, summary)
	}

	// Sort by trace ID for consistent output
	sort.Slice(preSummaries, func(i, j int) bool {
		return preSummaries[i].TraceID < preSummaries[j].TraceID
	})

	return preSummaries, nil
}

// addSamplingDecision retrieves sampling decisions from the processor for the processed batch
func addSamplingDecision(summaries []TraceSummary, eventStore eventstorage.RW) []TraceSummary {
	for i, summary := range summaries {
		sampled, err := eventStore.IsTraceSampled(summary.TraceID)
		if err != nil {
			fmt.Printf("failed to get sampling decision for trace %s: %s\n", summary.TraceID, err)
		} else {
			summaries[i].Sampled = sampled
		}
	}
	return summaries
}

// printTraceSummaries prints trace summaries in a tabulated format
func printTraceSummaries(summaries []TraceSummary) error {
	if len(summaries) == 0 {
		fmt.Printf("No traces found for pre-analysis")
		return nil
	}

	// Create a tabwriter for aligned output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	// Print header
	fmt.Fprintln(w, "\n\nTRACE_ID\tEVENT_TYPE\tPARENT_TRANSACTIONS\tCHILD_TRANSACTIONS\tPARENT_SERVICE_NAME\tPARENT_ENV\tPARENT_OUTCOME\tSAMPLED")

	// Print each pre-summary
	for _, summary := range summaries {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%t\n",
			summary.TraceID,
			summary.EventType,
			summary.ParentCount,
			summary.ChildCount,
			summary.ParentServiceName,
			summary.ParentEnvironment,
			summary.ParentEventOutcome,
			summary.Sampled,
		)
	}

	return nil
}
