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

import (
	"context"
	"time"
)

// TelemetryQuerier is the central abstraction for querying external observability backends.
// Implementations exist for SigNoz (unified), Jaeger (traces only), Prometheus (metrics only),
// and a composite querier that delegates to multiple backends.
type TelemetryQuerier interface {
	// --- Traces ---

	// FindTracesByService returns traces involving the given service within the time window.
	FindTracesByService(ctx context.Context, service string, start, end time.Time, limit int) ([]TraceSummary, error)

	// GetTrace returns the full trace for a given trace ID.
	GetTrace(ctx context.Context, traceID string) (*Trace, error)

	// FindErrorTraces returns traces with error spans for the given service within a lookback window.
	FindErrorTraces(ctx context.Context, service string, window time.Duration) ([]TraceSummary, error)

	// --- Metrics ---

	// QueryMetric executes a PromQL-style query and returns time series data.
	QueryMetric(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]MetricSeries, error)

	// GetServiceMetrics returns aggregated RED metrics (Rate, Errors, Duration) for a service.
	GetServiceMetrics(ctx context.Context, service string, window time.Duration) (*ServiceMetrics, error)

	// --- Logs ---

	// SearchLogs searches for log entries matching the given filter criteria.
	SearchLogs(ctx context.Context, filter LogFilter) ([]LogEntry, error)

	// --- Topology ---

	// GetDependencies returns service dependency edges observed within the given time window.
	GetDependencies(ctx context.Context, window time.Duration) ([]DependencyEdge, error)

	// --- Cross-signal ---

	// CorrelateByTraceID returns correlated traces, logs, and metrics for a specific trace ID.
	CorrelateByTraceID(ctx context.Context, traceID string) (*CorrelatedSignals, error)
}

// NoopQuerier is a TelemetryQuerier that returns empty results.
// Used when no telemetry backend is configured (backward-compatible Phase 1 behavior).
type NoopQuerier struct{}

func (n *NoopQuerier) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]TraceSummary, error) {
	return nil, nil
}

func (n *NoopQuerier) GetTrace(_ context.Context, _ string) (*Trace, error) {
	return nil, nil
}

func (n *NoopQuerier) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]TraceSummary, error) {
	return nil, nil
}

func (n *NoopQuerier) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]MetricSeries, error) {
	return nil, nil
}

func (n *NoopQuerier) GetServiceMetrics(_ context.Context, _ string, _ time.Duration) (*ServiceMetrics, error) {
	return nil, nil
}

func (n *NoopQuerier) SearchLogs(_ context.Context, _ LogFilter) ([]LogEntry, error) {
	return nil, nil
}

func (n *NoopQuerier) GetDependencies(_ context.Context, _ time.Duration) ([]DependencyEdge, error) {
	return nil, nil
}

func (n *NoopQuerier) CorrelateByTraceID(_ context.Context, _ string) (*CorrelatedSignals, error) {
	return nil, nil
}

var _ TelemetryQuerier = (*NoopQuerier)(nil)
