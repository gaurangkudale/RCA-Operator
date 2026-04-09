package correlator

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
	"github.com/gaurangkudale/rca-operator/internal/topology"
)

// testTelemetryQuerier implements TelemetryQuerier for cross-signal tests.
type testTelemetryQuerier struct {
	errorTraces  []telemetry.TraceSummary
	errorErr     error
	dependencies []telemetry.DependencyEdge
}

func (q *testTelemetryQuerier) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]telemetry.TraceSummary, error) {
	return nil, nil
}
func (q *testTelemetryQuerier) GetTrace(_ context.Context, _ string) (*telemetry.Trace, error) {
	return nil, nil
}
func (q *testTelemetryQuerier) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]telemetry.TraceSummary, error) {
	return q.errorTraces, q.errorErr
}
func (q *testTelemetryQuerier) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]telemetry.MetricSeries, error) {
	return nil, nil
}
func (q *testTelemetryQuerier) GetServiceMetrics(_ context.Context, _ string, _ time.Duration) (*telemetry.ServiceMetrics, error) {
	return nil, nil
}
func (q *testTelemetryQuerier) SearchLogs(_ context.Context, _ telemetry.LogFilter) ([]telemetry.LogEntry, error) {
	return nil, nil
}
func (q *testTelemetryQuerier) GetDependencies(_ context.Context, _ time.Duration) ([]telemetry.DependencyEdge, error) {
	return q.dependencies, nil
}
func (q *testTelemetryQuerier) CorrelateByTraceID(_ context.Context, _ string) (*telemetry.CorrelatedSignals, error) {
	return nil, nil
}

func makeTestReport(name, namespace, workloadName string) *rcav1alpha1.IncidentReport {
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "CrashLoopBackOff",
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:    "Active",
			Severity: "P1",
		},
	}
	if workloadName != "" {
		report.Spec.Scope.WorkloadRef = &rcav1alpha1.IncidentObjectRef{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  namespace,
			Name:       workloadName,
		}
	}
	return report
}

func TestCrossSignalEnricher_Enrich_WithTraces(t *testing.T) {
	querier := &testTelemetryQuerier{
		errorTraces: []telemetry.TraceSummary{
			{TraceID: "trace-aaa", RootService: "payment-svc", HasError: true},
			{TraceID: "trace-bbb", RootService: "payment-svc", HasError: true},
			{TraceID: "trace-aaa", RootService: "payment-svc", HasError: true}, // duplicate
		},
	}

	enricher := NewCrossSignalEnricher(querier, nil, nil, logr.Discard())
	report := makeTestReport("inc-1", "prod", "payment-svc")

	result := enricher.Enrich(context.Background(), report)

	if len(result.RelatedTraces) != 2 {
		t.Fatalf("expected 2 unique traces, got %d: %v", len(result.RelatedTraces), result.RelatedTraces)
	}
	if result.RelatedTraces[0] != "trace-aaa" || result.RelatedTraces[1] != "trace-bbb" {
		t.Errorf("unexpected traces: %v", result.RelatedTraces)
	}
}

func TestCrossSignalEnricher_Enrich_MaxTraces(t *testing.T) {
	traces := make([]telemetry.TraceSummary, 0, 20)
	for i := range 20 {
		traces = append(traces, telemetry.TraceSummary{
			TraceID:     "trace-" + string(rune('a'+i)),
			RootService: "svc",
			HasError:    true,
		})
	}
	querier := &testTelemetryQuerier{errorTraces: traces}

	enricher := NewCrossSignalEnricher(querier, nil, nil, logr.Discard(), WithMaxTraces(5))
	report := makeTestReport("inc-max", "prod", "svc")

	result := enricher.Enrich(context.Background(), report)

	if len(result.RelatedTraces) != 5 {
		t.Fatalf("expected 5 traces (max), got %d", len(result.RelatedTraces))
	}
}

func TestCrossSignalEnricher_Enrich_WithBlastRadius(t *testing.T) {
	querier := &testTelemetryQuerier{
		dependencies: []telemetry.DependencyEdge{
			{Parent: "gateway", Child: "payment-svc", CallCount: 100},
			{Parent: "ingress", Child: "gateway", CallCount: 200},
		},
	}

	builder := topology.NewBuilder(querier, logr.Discard())
	cache := topology.NewCache(builder, logr.Discard())

	enricher := NewCrossSignalEnricher(querier, cache, nil, logr.Discard())
	report := makeTestReport("inc-blast", "staging", "payment-svc")

	result := enricher.Enrich(context.Background(), report)

	// payment-svc has upstream callers: gateway, ingress
	if len(result.BlastRadius) < 1 {
		t.Fatalf("expected non-empty blast radius, got %v", result.BlastRadius)
	}
	has := func(name string) bool {
		return slices.Contains(result.BlastRadius, name)
	}
	if !has("gateway") {
		t.Error("expected gateway in blast radius")
	}
}

func TestCrossSignalEnricher_Enrich_NoService(t *testing.T) {
	querier := &testTelemetryQuerier{}
	enricher := NewCrossSignalEnricher(querier, nil, nil, logr.Discard())

	// Report with no workload ref or pod label
	report := makeTestReport("inc-empty", "default", "")

	result := enricher.Enrich(context.Background(), report)

	if len(result.RelatedTraces) != 0 {
		t.Errorf("expected no traces for empty service, got %v", result.RelatedTraces)
	}
}

func TestCrossSignalEnricher_NilQuerier(t *testing.T) {
	enricher := NewCrossSignalEnricher(nil, nil, nil, logr.Discard())
	report := makeTestReport("inc-noop", "prod", "svc")

	result := enricher.Enrich(context.Background(), report)

	if len(result.RelatedTraces) != 0 {
		t.Errorf("expected no traces from NoopQuerier, got %v", result.RelatedTraces)
	}
}

func TestApplyEnrichment(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			RelatedTraces: []string{"existing-trace"},
		},
	}

	result := EnrichmentResult{
		RelatedTraces: []string{"existing-trace", "new-trace"},
		BlastRadius:   []string{"gateway", "ingress"},
	}

	modified := ApplyEnrichment(report, result)
	if !modified {
		t.Error("expected modified=true")
	}

	// Should have 2 traces (deduped "existing-trace")
	if len(report.Status.RelatedTraces) != 2 {
		t.Fatalf("expected 2 traces, got %d: %v", len(report.Status.RelatedTraces), report.Status.RelatedTraces)
	}
	if report.Status.RelatedTraces[0] != "existing-trace" || report.Status.RelatedTraces[1] != "new-trace" {
		t.Errorf("unexpected traces: %v", report.Status.RelatedTraces)
	}

	if len(report.Status.BlastRadius) != 2 {
		t.Fatalf("expected 2 blast radius entries, got %d", len(report.Status.BlastRadius))
	}
}

func TestApplyEnrichment_NoChange(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			BlastRadius: []string{"a", "b"},
		},
	}

	result := EnrichmentResult{
		BlastRadius: []string{"a", "b"},
	}

	modified := ApplyEnrichment(report, result)
	if modified {
		t.Error("expected modified=false when data unchanged")
	}
}

func TestApplyEnrichment_Empty(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	result := EnrichmentResult{}

	modified := ApplyEnrichment(report, result)
	if modified {
		t.Error("expected modified=false for empty enrichment")
	}
}

func TestExtractServiceName(t *testing.T) {
	tests := []struct {
		name     string
		report   *rcav1alpha1.IncidentReport
		expected string
	}{
		{
			"workload ref",
			makeTestReport("inc-svcname", "prod", "payment-svc"),
			"payment-svc",
		},
		{
			"resource ref (node)",
			&rcav1alpha1.IncidentReport{
				Spec: rcav1alpha1.IncidentReportSpec{
					Scope: rcav1alpha1.IncidentScope{
						ResourceRef: &rcav1alpha1.IncidentObjectRef{Kind: "Node", Name: "node-1"},
					},
				},
			},
			"node-1",
		},
		{
			"pod label",
			&rcav1alpha1.IncidentReport{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelPodName: "auth-svc-abc12"},
				},
			},
			"auth-svc",
		},
		{
			"empty",
			&rcav1alpha1.IncidentReport{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}},
			},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractServiceName(tt.report)
			if got != tt.expected {
				t.Errorf("extractServiceName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestStripPodSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"payment-svc-7d4b8c6f5-x2k9n", "payment-svc"},
		{"payment-svc-abc12", "payment-svc"},
		{"payment-svc", "payment-svc"},
		{"svc", "svc"},
		{"my-app-v2-abc12-def34", "my-app-v2"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripPodSuffix(tt.input)
			if got != tt.expected {
				t.Errorf("stripPodSuffix(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestStringSliceEqual(t *testing.T) {
	tests := []struct {
		a, b     []string
		expected bool
	}{
		{nil, nil, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a"}, []string{"a", "b"}, false},
	}
	for _, tt := range tests {
		got := stringSliceEqual(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("stringSliceEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.expected)
		}
	}
}
