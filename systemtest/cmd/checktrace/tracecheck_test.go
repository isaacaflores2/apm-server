package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/elastic/apm-data/model/modelpb"
	beaterconfig "github.com/elastic/apm-server/internal/beater/config"
	"github.com/elastic/apm-server/internal/elasticsearch"
	"github.com/elastic/apm-server/x-pack/apm-server/sampling/eventstorage"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/logp/logptest"
	"github.com/elastic/elastic-agent-libs/paths"
)

func TestSummarizeAndProcessEvents(t *testing.T) {
	tests := []struct {
		name                    string
		testDataFile            string
		expectedTraceCount      int
		expectedSampledTraceIDs []string
	}{
		{
			name:               "Check example traces",
			testDataFile:       "testdata/example-traces.json",
			expectedTraceCount: 5,
			expectedSampledTraceIDs: []string{
				"0b8064454d96d2367037bef04a423eca",
				"feabab44a0233185e1796fc2ce6b4fc7",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logp.NewLogger("tracecheck_test")

			// Load test data
			batch, err := parseInputFileAsAPMEvents(tt.testDataFile, logger)
			require.NoError(t, err, "Failed to load test data")
			require.NotNil(t, batch, "Test data should not be nil")
			require.NotEmpty(t, *batch, "Test data should not be empty")

			apmServerConfig, db := createTestConfigAndStorage(t)

			summaries, err := summarizeAndProcessEvents(apmServerConfig, db, batch, logger)
			require.NoError(t, err, "summarizeAndProcessEvents should not return error")

			assert.Len(t, summaries, tt.expectedTraceCount, "Should have expected number of trace summaries")

			for _, summary := range summaries {
				t.Run(fmt.Sprintf("summary_%s", summary.TraceID), func(t *testing.T) {

					// All traces should not have any empty fields
					assert.NotEmpty(t, summary.TraceID, "TraceID should not be empty")
					assert.NotEmpty(t, summary.ParentServiceName, "ParentServiceName should not be empty")
					assert.NotEmpty(t, summary.ParentEnvironment, "ParentEnvironment should not be empty")
					assert.NotEmpty(t, summary.ParentEventOutcome, "ParentEventOutcome should not be empty")

					switch summary.ParentCount {
					case 0:
						// Traces without a parent should not be sampled
						assert.False(t, summary.Sampled, "Traces without a parent should not be sampled")
					case 1:
						// Traces with a parent should have valid event type
						assert.NotEqual(t, modelpb.UndefinedEventType, summary.EventType, "EventType should not be undefined for traces with a parent")
					}

					for _, sampledTraceID := range tt.expectedSampledTraceIDs {
						if sampledTraceID == summary.TraceID {
							assert.True(t, summary.Sampled, "Trace should be sampled")
						}
					}
				})
			}
		})
	}
}

func TestSummarizeAndProcessEventsWithInvalidConfig(t *testing.T) {
	logger := logp.NewLogger("tracecheck_test")
	apmServerConfig, db := createTestConfigAndStorage(t)

	batch, err := parseInputFileAsAPMEvents("testdata/example-traces.json", logger)
	require.NoError(t, err)

	summaries, err := summarizeAndProcessEvents(nil, db, batch, logger)
	assert.Error(t, err, "Should return error with nil config")
	assert.Nil(t, summaries, "Should return nil summaries on error")

	summaries, err = summarizeAndProcessEvents(apmServerConfig, nil, batch, logger)
	assert.Error(t, err, "Should return error with nil db")
	assert.Nil(t, summaries, "Should return nil summaries on error")
}

func TestSummarizeAndProcessEventsWithEmptyBatch(t *testing.T) {
	logger := logp.NewLogger("tracecheck_test")
	apmServerConfig, db := createTestConfigAndStorage(t)

	// Test with empty batch
	emptyBatch := &modelpb.Batch{}
	summaries, err := summarizeAndProcessEvents(apmServerConfig, db, emptyBatch, logger)
	require.NoError(t, err, "Should handle empty batch gracefully")
	assert.Empty(t, summaries, "Should return empty summaries for empty batch")
}

// Helper function to create test configuration
func createTestConfigAndStorage(t *testing.T) (*beaterconfig.Config, *eventstorage.StorageManager) {
	// Create a unique temporary directory for each test to avoid storage locks
	tempDir, err := os.MkdirTemp("", fmt.Sprintf("tracecheck_test_%d_*", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	storageDir := paths.Resolve(paths.Data, "checktrace-storage")
	db, err := eventstorage.NewStorageManager(storageDir, logptest.NewTestingLogger(t, ""))
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
	})

	// Create basic test configuration
	cfg := &beaterconfig.Config{
		Sampling: beaterconfig.SamplingConfig{
			Tail: beaterconfig.TailSamplingConfig{
				Enabled: true,
				Policies: []beaterconfig.TailSamplingPolicy{
					{
						SampleRate: 1.0,
					},
				},
				ESConfig:              &elasticsearch.Config{},
				Interval:              100 * time.Millisecond,
				IngestRateDecayFactor: 0.9,
				TTL:                   1 * time.Minute,
				StorageLimitParsed:    1024 * 1024 * 100, // 100MB
				DiskUsageThreshold:    0.9,
				DiscardOnWriteFailure: false,
				DatabaseCacheSize:     0,
			},
		},
	}
	return cfg, db
}
