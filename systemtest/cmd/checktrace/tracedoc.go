package main

import (
	"time"
)

// TraceDoc represents an Elasticsearch document for a trace
type TraceDoc struct {
	Source Source `json:"_source"`
}

// Source contains the source fields for a trace
type Source struct {
	Parent      Parent      `json:"parent"`
	Processor   Processor   `json:"processor"`
	Trace       Trace       `json:"trace"`
	Timestamp   time.Time   `json:"@timestamp"`
	Service     Service     `json:"service"`
	Event       Event       `json:"event"`
	Transaction Transaction `json:"transaction"`
	Span        Span        `json:"span"`
	Timestamp0  Timestamp   `json:"timestamp"`
}

type Parent struct {
	ID string `json:"id"`
}

type Processor struct {
	Event string `json:"event"`
}
type Trace struct {
	ID string `json:"id"`
}

type Service struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
}
type Event struct {
	Outcome string `json:"outcome"`
}

type Span struct {
	RepresentativeCount int    `json:"representative_count"`
	Subtype             string `json:"subtype"`
	Name                string `json:"name"`
	Action              string `json:"action"`
	ID                  string `json:"id"`
	Type                string `json:"type"`
}

type Timestamp struct {
	Us int64 `json:"us"`
}

type Transaction struct {
	Result              string `json:"result"`
	RepresentativeCount int    `json:"representative_count"`
	Name                string `json:"name"`
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Sampled             bool   `json:"sampled"`
}
