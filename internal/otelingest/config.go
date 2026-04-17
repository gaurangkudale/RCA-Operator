// Package otelingest is an in-operator OTLP/HTTP receiver that turns inbound
// spans and logs (fanned out from the cluster-wide OTel Collector DaemonSet)
// into watcher.CorrelatorEvent signals for the correlation engine.
//
// It exposes a minimal HTTP server on a configurable port (default :4319) with
// two endpoints:
//
//	POST /v1/traces  — OTLP ExportTraceServiceRequest (JSON or protobuf)
//	POST /v1/logs    — OTLP ExportLogsServiceRequest  (JSON or protobuf)
//
// Filtering policy (which spans/logs become correlator signals) lives here, not
// in the collector. The collector fans out everything matching its OTTL filter;
// the operator decides what is worth buffering.
package otelingest

import (
	"regexp"
	"time"
)

// Config describes the filter and behavior policy for the ingest server.
// Fields map directly to the Helm otelIngest.* values block.
type Config struct {
	// BindAddress is the TCP address the HTTP server listens on, e.g. ":4319".
	BindAddress string

	// ReadTimeout and WriteTimeout bound each HTTP request. 10s is ample for
	// OTLP batches sized by the collector's batch processor.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// MaxRequestBytes caps the decoded size of any single OTLP request. Ingest
	// rejects requests above this cap with 413. Default 16 MiB.
	MaxRequestBytes int64

	// AgentName is the default AgentName stamped on synthesized events. This is
	// later overridden per-event if a matching RCAAgent is found; for ingest
	// skeleton (Milestone A) we default to a cluster-scoped sentinel.
	AgentName string

	// TraceFilters controls which inbound spans become signals.
	TraceFilters TraceFilters

	// LogFilters controls which inbound log records become signals.
	LogFilters LogFilters

	// Redaction is the list of compiled regex patterns applied to log bodies
	// and to span status messages. PII is replaced with the token "[REDACTED]".
	// Compilation happens at NewServer time from a []string source.
	Redaction []*regexp.Regexp
}

// TraceFilters mirrors Helm otelIngest.filters.traces.
type TraceFilters struct {
	// StatusCodeERROR retains spans whose Status.Code == STATUS_CODE_ERROR.
	StatusCodeERROR bool

	// HTTPStatusGte emits a signal when a span attribute http.status_code (or
	// http.response.status_code) is >= this value. Zero disables the check.
	HTTPStatusGte int

	// LatencyP99Ms emits an OTelSpanLatencySpike signal when a span's duration
	// exceeds this threshold in milliseconds. Zero disables the check.
	LatencyP99Ms int
}

// LogFilters mirrors Helm otelIngest.filters.logs.
type LogFilters struct {
	// MinSeverity is the OTel severity text ("WARN", "ERROR", "FATAL").
	// Records below this are dropped at ingest.
	MinSeverity string
}

// DefaultConfig returns a Config populated with the Phase 2 defaults documented
// in helm/values.yaml. Redaction is empty here; callers pass in the pattern
// strings from values.yaml via CompileRedaction.
func DefaultConfig() Config {
	return Config{
		BindAddress:     ":4319",
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		MaxRequestBytes: 16 * 1024 * 1024,
		AgentName:       "otelingest",
		TraceFilters: TraceFilters{
			StatusCodeERROR: true,
			HTTPStatusGte:   500,
			LatencyP99Ms:    5000,
		},
		LogFilters: LogFilters{MinSeverity: "WARN"},
	}
}

// CompileRedaction compiles a list of regex strings into []*regexp.Regexp.
// Invalid patterns are skipped with no error; log any such case at the call site.
func CompileRedaction(patterns []string) ([]*regexp.Regexp, []error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	var errs []error
	for _, p := range patterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, re)
	}
	return out, errs
}
