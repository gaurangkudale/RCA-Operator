package topology

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

// testQuerier implements TelemetryQuerier for topology builder tests.
type testQuerier struct {
	deps    []telemetry.DependencyEdge
	metrics map[string]*telemetry.ServiceMetrics
	depsErr error
}

func (q *testQuerier) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]telemetry.TraceSummary, error) {
	return nil, nil
}
func (q *testQuerier) GetTrace(_ context.Context, _ string) (*telemetry.Trace, error) {
	return nil, nil
}
func (q *testQuerier) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]telemetry.TraceSummary, error) {
	return nil, nil
}
func (q *testQuerier) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]telemetry.MetricSeries, error) {
	return nil, nil
}
func (q *testQuerier) GetServiceMetrics(_ context.Context, service string, _ time.Duration) (*telemetry.ServiceMetrics, error) {
	if q.metrics != nil {
		return q.metrics[service], nil
	}
	return nil, nil
}
func (q *testQuerier) SearchLogs(_ context.Context, _ telemetry.LogFilter) ([]telemetry.LogEntry, error) {
	return nil, nil
}
func (q *testQuerier) GetDependencies(_ context.Context, _ time.Duration) ([]telemetry.DependencyEdge, error) {
	return q.deps, q.depsErr
}
func (q *testQuerier) CorrelateByTraceID(_ context.Context, _ string) (*telemetry.CorrelatedSignals, error) {
	return nil, nil
}

func TestBuilder_BuildGraph(t *testing.T) {
	q := &testQuerier{
		deps: []telemetry.DependencyEdge{
			{Parent: "api-gateway", Child: "payment-svc", CallCount: 1000, ErrorRate: 0.05},
			{Parent: "payment-svc", Child: "postgres-db", CallCount: 500, ErrorRate: 0.15},
		},
		metrics: map[string]*telemetry.ServiceMetrics{
			"api-gateway": {ServiceName: "api-gateway", RequestRate: 200, P99Latency: 50},
			"payment-svc": {ServiceName: "payment-svc", RequestRate: 100, ErrorRate: 5, P99Latency: 200},
		},
	}

	builder := NewBuilder(q, logr.Discard())
	incidents := []IncidentRef{
		{Name: "payment-svc-crash", Namespace: "prod", Severity: "P1", Phase: "Active", IncidentType: "CrashLoop"},
	}

	graph, err := builder.BuildGraph(context.Background(), 15*time.Minute, incidents)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	// Should have 3 nodes from 2 dependency edges
	if len(graph.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(graph.Nodes))
	}
	if len(graph.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(graph.Edges))
	}

	// Check edge classification
	for _, e := range graph.Edges {
		if e.Source == "payment-svc" && e.Target == "postgres-db" {
			if e.Status != telemetry.EdgeStatusCritical {
				t.Errorf("expected critical edge for 15%% error rate, got %s", e.Status)
			}
		}
		if e.Source == "api-gateway" && e.Target == "payment-svc" {
			if e.Status != telemetry.EdgeStatusWarning {
				t.Errorf("expected warning edge for 5%% error rate, got %s", e.Status)
			}
		}
	}

	// Check metrics enrichment
	gateway := graph.Nodes["api-gateway"]
	if gateway.Metrics == nil {
		t.Error("expected metrics on api-gateway")
	} else if gateway.Metrics.RequestRate != 200 {
		t.Errorf("expected gateway request rate 200, got %f", gateway.Metrics.RequestRate)
	}

	// Check incident overlay
	paymentNode := graph.Nodes["payment-svc"]
	if paymentNode.Status != telemetry.HealthStatusCritical {
		t.Errorf("expected payment-svc to be critical (has P1 Active incident), got %s", paymentNode.Status)
	}
	if len(paymentNode.Incidents) != 1 {
		t.Errorf("expected 1 incident on payment-svc, got %d", len(paymentNode.Incidents))
	}

	// Gateway should be healthy (no incidents)
	if gateway.Status != telemetry.HealthStatusHealthy {
		t.Errorf("expected api-gateway to be healthy, got %s", gateway.Status)
	}
}

func TestBuilder_BuildGraph_EmptyDependencies(t *testing.T) {
	q := &testQuerier{deps: nil}
	builder := NewBuilder(q, logr.Discard())

	graph, err := builder.BuildGraph(context.Background(), 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(graph.Nodes) != 0 {
		t.Errorf("expected 0 nodes for empty deps, got %d", len(graph.Nodes))
	}
}

func TestBuilder_BuildGraph_DepsError(t *testing.T) {
	q := &testQuerier{depsErr: context.DeadlineExceeded}
	builder := NewBuilder(q, logr.Discard())

	graph, err := builder.BuildGraph(context.Background(), 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("BuildGraph should not fail on dep error: %v", err)
	}
	// Should return empty graph, not error
	if len(graph.Nodes) != 0 {
		t.Errorf("expected empty graph on dep error, got %d nodes", len(graph.Nodes))
	}
}

func TestComputeNodeStatus(t *testing.T) {
	tests := []struct {
		name      string
		incidents []IncidentRef
		expected  telemetry.HealthStatus
	}{
		{"no incidents", nil, telemetry.HealthStatusHealthy},
		{"resolved only", []IncidentRef{{Phase: "Resolved", Severity: "P1"}}, telemetry.HealthStatusHealthy},
		{"detecting P3", []IncidentRef{{Phase: "Detecting", Severity: "P3"}}, telemetry.HealthStatusWarning},
		{"active P3", []IncidentRef{{Phase: "Active", Severity: "P3"}}, telemetry.HealthStatusWarning},
		{"active P1", []IncidentRef{{Phase: "Active", Severity: "P1"}}, telemetry.HealthStatusCritical},
		{"active P2", []IncidentRef{{Phase: "Active", Severity: "P2"}}, telemetry.HealthStatusCritical},
		{"mixed", []IncidentRef{
			{Phase: "Active", Severity: "P3"},
			{Phase: "Active", Severity: "P1"},
		}, telemetry.HealthStatusCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ServiceNode{Incidents: tt.incidents}
			got := computeNodeStatus(node)
			if got != tt.expected {
				t.Errorf("computeNodeStatus() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestClassifyEdgeStatus(t *testing.T) {
	tests := []struct {
		name     string
		dep      telemetry.DependencyEdge
		expected telemetry.EdgeStatus
	}{
		{"healthy", telemetry.DependencyEdge{ErrorRate: 0}, telemetry.EdgeStatusActive},
		{"low errors", telemetry.DependencyEdge{ErrorRate: 0.005}, telemetry.EdgeStatusActive},
		{"warning errors", telemetry.DependencyEdge{ErrorRate: 0.05}, telemetry.EdgeStatusWarning},
		{"warning latency", telemetry.DependencyEdge{ErrorRate: 0, AvgLatency: 1500}, telemetry.EdgeStatusWarning},
		{"critical errors", telemetry.DependencyEdge{ErrorRate: 0.2}, telemetry.EdgeStatusCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyEdgeStatus(tt.dep)
			if got != tt.expected {
				t.Errorf("classifyEdgeStatus() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestInferIcon(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"nginx-ingress", "globe"},
		{"api-gateway", "globe"},
		{"postgres-db", "database"},
		{"redis-cache", "database"},
		{"kafka-broker", "mail"},
		{"auth-service", "shield-check"},
		{"payment-svc", "credit-card"},
		{"frontend-app", "monitor"},
		{"inventory-svc", "server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ServiceNode{Name: tt.name}
			got := inferIcon(node)
			if got != tt.expected {
				t.Errorf("inferIcon(%s) = %s, want %s", tt.name, got, tt.expected)
			}
		})
	}
}

func TestMatchesService(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		nodeNS   string
		incName  string
		incNS    string
		expected bool
	}{
		{"exact match", "payment-svc", "", "payment-svc", "", true},
		{"prefix match", "payment-svc", "", "payment-svc-abc123", "", true},
		{"no match", "payment-svc", "", "auth-svc", "", false},
		{"namespace mismatch", "payment-svc", "prod", "payment-svc", "staging", false},
		{"namespace match", "payment-svc", "prod", "payment-svc-crash", "prod", true},
		{"short incident name", "payment-svc", "", "pay", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ServiceNode{Name: tt.nodeName, Namespace: tt.nodeNS}
			inc := IncidentRef{Name: tt.incName, Namespace: tt.incNS}
			got := matchesService(node, inc)
			if got != tt.expected {
				t.Errorf("matchesService() = %v, want %v", got, tt.expected)
			}
		})
	}
}
