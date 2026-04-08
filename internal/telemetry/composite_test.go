package telemetry

import (
	"context"
	"testing"
	"time"
)

// mockQuerier implements TelemetryQuerier for testing the composite querier.
type mockQuerier struct {
	traces       []TraceSummary
	trace        *Trace
	errorTraces  []TraceSummary
	metricSeries []MetricSeries
	svcMetrics   *ServiceMetrics
	logs         []LogEntry
	deps         []DependencyEdge
}

func (m *mockQuerier) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]TraceSummary, error) {
	return m.traces, nil
}
func (m *mockQuerier) GetTrace(_ context.Context, _ string) (*Trace, error) {
	return m.trace, nil
}
func (m *mockQuerier) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]TraceSummary, error) {
	return m.errorTraces, nil
}
func (m *mockQuerier) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]MetricSeries, error) {
	return m.metricSeries, nil
}
func (m *mockQuerier) GetServiceMetrics(_ context.Context, _ string, _ time.Duration) (*ServiceMetrics, error) {
	return m.svcMetrics, nil
}
func (m *mockQuerier) SearchLogs(_ context.Context, _ LogFilter) ([]LogEntry, error) {
	return m.logs, nil
}
func (m *mockQuerier) GetDependencies(_ context.Context, _ time.Duration) ([]DependencyEdge, error) {
	return m.deps, nil
}
func (m *mockQuerier) CorrelateByTraceID(_ context.Context, _ string) (*CorrelatedSignals, error) {
	return nil, nil
}

func TestCompositeQuerier_DelegatesToCorrectBackend(t *testing.T) {
	tracesBackend := &mockQuerier{
		traces: []TraceSummary{{TraceID: "trace-from-jaeger"}},
		trace:  &Trace{TraceID: "trace-detail", Spans: []Span{{SpanID: "s1"}}},
		deps:   []DependencyEdge{{Parent: "a", Child: "b", CallCount: 100}},
	}
	metricsBackend := &mockQuerier{
		metricSeries: []MetricSeries{{Labels: map[string]string{"job": "test"}}},
		svcMetrics:   &ServiceMetrics{ServiceName: "test-svc", RequestRate: 42},
	}
	logsBackend := &mockQuerier{
		logs: []LogEntry{{Body: "test log", Severity: "ERROR"}},
	}

	composite := NewCompositeQuerier(tracesBackend, metricsBackend, logsBackend)
	ctx := context.Background()

	// Traces should come from traces backend
	traces, err := composite.FindTracesByService(ctx, "svc", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("FindTracesByService: %v", err)
	}
	if len(traces) != 1 || traces[0].TraceID != "trace-from-jaeger" {
		t.Errorf("expected trace from Jaeger backend, got %v", traces)
	}

	// Metrics should come from metrics backend
	series, err := composite.QueryMetric(ctx, "up", time.Now(), time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("QueryMetric: %v", err)
	}
	if len(series) != 1 || series[0].Labels["job"] != "test" {
		t.Errorf("expected metrics from Prometheus backend, got %v", series)
	}

	svcMetrics, err := composite.GetServiceMetrics(ctx, "test-svc", time.Minute)
	if err != nil {
		t.Fatalf("GetServiceMetrics: %v", err)
	}
	if svcMetrics == nil || svcMetrics.RequestRate != 42 {
		t.Errorf("expected service metrics from Prometheus, got %v", svcMetrics)
	}

	// Logs should come from logs backend
	logs, err := composite.SearchLogs(ctx, LogFilter{})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].Body != "test log" {
		t.Errorf("expected logs from logs backend, got %v", logs)
	}

	// Dependencies should come from traces backend
	deps, err := composite.GetDependencies(ctx, 15*time.Minute)
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].Parent != "a" {
		t.Errorf("expected dependencies from traces backend, got %v", deps)
	}
}

func TestCompositeQuerier_CorrelateByTraceID(t *testing.T) {
	tracesBackend := &mockQuerier{
		trace: &Trace{TraceID: "t1", Spans: []Span{{SpanID: "s1", ServiceName: "svc"}}},
	}
	logsBackend := &mockQuerier{
		logs: []LogEntry{{Body: "error log", TraceID: "t1"}},
	}

	composite := NewCompositeQuerier(tracesBackend, nil, logsBackend)
	result, err := composite.CorrelateByTraceID(context.Background(), "t1")
	if err != nil {
		t.Fatalf("CorrelateByTraceID: %v", err)
	}
	if result.TraceID != "t1" {
		t.Errorf("expected traceID t1, got %s", result.TraceID)
	}
	if result.Trace == nil || len(result.Trace.Spans) != 1 {
		t.Error("expected trace with 1 span")
	}
	if len(result.Logs) != 1 {
		t.Errorf("expected 1 correlated log, got %d", len(result.Logs))
	}
}

func TestCompositeQuerier_NilBackends(t *testing.T) {
	// All nil backends should return empty results (not panic)
	composite := NewCompositeQuerier(nil, nil, nil)
	ctx := context.Background()

	traces, err := composite.FindTracesByService(ctx, "svc", time.Now(), time.Now(), 10)
	if err != nil || traces != nil {
		t.Error("nil traces backend should return nil, nil")
	}

	metrics, err := composite.QueryMetric(ctx, "up", time.Now(), time.Now(), time.Minute)
	if err != nil || metrics != nil {
		t.Error("nil metrics backend should return nil, nil")
	}

	logs, err := composite.SearchLogs(ctx, LogFilter{})
	if err != nil || logs != nil {
		t.Error("nil logs backend should return nil, nil")
	}

	deps, err := composite.GetDependencies(ctx, time.Minute)
	if err != nil || deps != nil {
		t.Error("nil traces backend should return nil dependencies")
	}
}

func TestNoopQuerier(t *testing.T) {
	noop := &NoopQuerier{}
	ctx := context.Background()

	traces, err := noop.FindTracesByService(ctx, "svc", time.Now(), time.Now(), 10)
	if err != nil || traces != nil {
		t.Error("NoopQuerier.FindTracesByService should return nil, nil")
	}
	trace, err := noop.GetTrace(ctx, "id")
	if err != nil || trace != nil {
		t.Error("NoopQuerier.GetTrace should return nil, nil")
	}
	metrics, err := noop.QueryMetric(ctx, "up", time.Now(), time.Now(), time.Minute)
	if err != nil || metrics != nil {
		t.Error("NoopQuerier.QueryMetric should return nil, nil")
	}
	logs, err := noop.SearchLogs(ctx, LogFilter{})
	if err != nil || logs != nil {
		t.Error("NoopQuerier.SearchLogs should return nil, nil")
	}
	deps, err := noop.GetDependencies(ctx, time.Minute)
	if err != nil || deps != nil {
		t.Error("NoopQuerier.GetDependencies should return nil, nil")
	}
	corr, err := noop.CorrelateByTraceID(ctx, "id")
	if err != nil || corr != nil {
		t.Error("NoopQuerier.CorrelateByTraceID should return nil, nil")
	}
}
