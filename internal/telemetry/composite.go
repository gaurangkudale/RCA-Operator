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

// CompositeQuerier delegates to multiple backend-specific clients.
// It routes trace queries to the trace backend (Jaeger), metric queries to
// Prometheus, and combines results for cross-signal correlation.
// Use this when SigNoz (unified backend) is not available.
type CompositeQuerier struct {
	traces  TelemetryQuerier // Jaeger or nil
	metrics TelemetryQuerier // Prometheus or nil
	logs    TelemetryQuerier // future: Loki or nil
}

// NewCompositeQuerier creates a composite querier from individual backend clients.
// Any parameter may be nil; nil backends return empty results for their signal type.
func NewCompositeQuerier(traces, metrics, logs TelemetryQuerier) *CompositeQuerier {
	noop := &NoopQuerier{}
	if traces == nil {
		traces = noop
	}
	if metrics == nil {
		metrics = noop
	}
	if logs == nil {
		logs = noop
	}
	return &CompositeQuerier{
		traces:  traces,
		metrics: metrics,
		logs:    logs,
	}
}

func (c *CompositeQuerier) FindTracesByService(ctx context.Context, service string, start, end time.Time, limit int) ([]TraceSummary, error) {
	return c.traces.FindTracesByService(ctx, service, start, end, limit)
}

func (c *CompositeQuerier) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	return c.traces.GetTrace(ctx, traceID)
}

func (c *CompositeQuerier) FindErrorTraces(ctx context.Context, service string, window time.Duration) ([]TraceSummary, error) {
	return c.traces.FindErrorTraces(ctx, service, window)
}

func (c *CompositeQuerier) QueryMetric(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]MetricSeries, error) {
	return c.metrics.QueryMetric(ctx, query, start, end, step)
}

func (c *CompositeQuerier) GetServiceMetrics(ctx context.Context, service string, window time.Duration) (*ServiceMetrics, error) {
	return c.metrics.GetServiceMetrics(ctx, service, window)
}

func (c *CompositeQuerier) SearchLogs(ctx context.Context, filter LogFilter) ([]LogEntry, error) {
	return c.logs.SearchLogs(ctx, filter)
}

func (c *CompositeQuerier) GetDependencies(ctx context.Context, window time.Duration) ([]DependencyEdge, error) {
	// Prefer traces backend for dependencies (Jaeger provides /api/dependencies)
	return c.traces.GetDependencies(ctx, window)
}

func (c *CompositeQuerier) CorrelateByTraceID(ctx context.Context, traceID string) (*CorrelatedSignals, error) {
	result := &CorrelatedSignals{TraceID: traceID}

	// Get trace from traces backend
	trace, err := c.traces.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	result.Trace = trace

	// Get correlated logs from logs backend
	logs, err := c.logs.SearchLogs(ctx, LogFilter{
		TraceID: traceID,
		Start:   time.Now().Add(-1 * time.Hour),
		End:     time.Now(),
		Limit:   100,
	})
	if err == nil {
		result.Logs = logs
	}

	return result, nil
}

var _ TelemetryQuerier = (*CompositeQuerier)(nil)
