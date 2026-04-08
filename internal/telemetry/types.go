/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package telemetry

import "time"

// TraceSummary is a lightweight representation of a trace for listing.
type TraceSummary struct {
	TraceID     string        `json:"traceID"`
	RootService string        `json:"rootService"`
	RootSpan    string        `json:"rootSpan"`
	StartTime   time.Time     `json:"startTime"`
	Duration    time.Duration `json:"duration"`
	SpanCount   int           `json:"spanCount"`
	HasError    bool          `json:"hasError"`
}

// Trace is a full distributed trace containing all its spans.
type Trace struct {
	TraceID string `json:"traceID"`
	Spans   []Span `json:"spans"`
}

// Span represents a single unit of work within a trace.
type Span struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	ParentSpanID  string            `json:"parentSpanID,omitempty"`
	OperationName string            `json:"operationName"`
	ServiceName   string            `json:"serviceName"`
	StartTime     time.Time         `json:"startTime"`
	Duration      time.Duration     `json:"duration"`
	StatusCode    SpanStatusCode    `json:"statusCode"`
	StatusMessage string            `json:"statusMessage,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	Logs          []SpanLog         `json:"logs,omitempty"`
}

// SpanStatusCode represents the status of a span.
type SpanStatusCode int

const (
	StatusUnset SpanStatusCode = iota
	StatusOK
	StatusError
)

// SpanLog is a timestamped log entry attached to a span.
type SpanLog struct {
	Timestamp time.Time         `json:"timestamp"`
	Fields    map[string]string `json:"fields"`
}

// MetricSeries is a single time series returned from a metric query.
type MetricSeries struct {
	Labels     map[string]string `json:"labels"`
	Datapoints []Datapoint       `json:"datapoints"`
}

// Datapoint is a single metric value at a point in time.
type Datapoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// ServiceMetrics contains key metrics for a service over a time window.
type ServiceMetrics struct {
	ServiceName string  `json:"serviceName"`
	RequestRate float64 `json:"requestRate"` // req/s
	ErrorRate   float64 `json:"errorRate"`   // errors/s
	P50Latency  float64 `json:"p50Latency"`  // ms
	P95Latency  float64 `json:"p95Latency"`  // ms
	P99Latency  float64 `json:"p99Latency"`  // ms
	CPUUsage    float64 `json:"cpuUsage"`    // cores
	MemoryUsage float64 `json:"memoryUsage"` // bytes
}

// LogEntry represents a single log line from the observability backend.
type LogEntry struct {
	Timestamp   time.Time         `json:"timestamp"`
	Body        string            `json:"body"`
	Severity    string            `json:"severity"` // TRACE, DEBUG, INFO, WARN, ERROR, FATAL
	ServiceName string            `json:"serviceName"`
	TraceID     string            `json:"traceID,omitempty"`
	SpanID      string            `json:"spanID,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// LogFilter specifies criteria for searching logs.
type LogFilter struct {
	ServiceName string    `json:"serviceName,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	Keyword     string    `json:"keyword,omitempty"`
	TraceID     string    `json:"traceID,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Limit       int       `json:"limit,omitempty"`
}

// DependencyEdge represents a caller-callee relationship between services.
type DependencyEdge struct {
	Parent     string  `json:"parent"`
	Child      string  `json:"child"`
	CallCount  int64   `json:"callCount"`
	ErrorRate  float64 `json:"errorRate,omitempty"`  // fraction 0.0-1.0
	AvgLatency float64 `json:"avgLatency,omitempty"` // ms
}

// CorrelatedSignals groups traces, metrics, and logs for a single trace context.
type CorrelatedSignals struct {
	TraceID string         `json:"traceID"`
	Trace   *Trace         `json:"trace,omitempty"`
	Logs    []LogEntry     `json:"logs,omitempty"`
	Metrics []MetricSeries `json:"metrics,omitempty"`
}

// HealthStatus represents the health state of a service node.
type HealthStatus string

const (
	HealthStatusHealthy  HealthStatus = "healthy"
	HealthStatusWarning  HealthStatus = "warning"
	HealthStatusCritical HealthStatus = "critical"
	HealthStatusUnknown  HealthStatus = "unknown"
)

// EdgeStatus represents the status of a connection between services.
type EdgeStatus string

const (
	EdgeStatusActive   EdgeStatus = "active"
	EdgeStatusWarning  EdgeStatus = "warning"
	EdgeStatusCritical EdgeStatus = "critical"
)
