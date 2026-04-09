package topology

import (
	"slices"
	"sort"
	"testing"

	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

func makeTestGraph() *ServiceGraph {
	//  ingress -> gateway -> auth-svc
	//                     -> payment-svc -> postgres-db
	//                     -> inventory-svc -> postgres-db
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "ingress", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "gateway", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "auth-svc", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "payment-svc", Status: telemetry.HealthStatusCritical})
	g.AddNode(&ServiceNode{Name: "inventory-svc", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "postgres-db", Status: telemetry.HealthStatusWarning})

	g.AddEdge(ServiceEdge{Source: "ingress", Target: "gateway"})
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "auth-svc"})
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "payment-svc"})
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "inventory-svc"})
	g.AddEdge(ServiceEdge{Source: "payment-svc", Target: "postgres-db"})
	g.AddEdge(ServiceEdge{Source: "inventory-svc", Target: "postgres-db"})

	return g
}

func TestComputeBlastRadius_PaymentFailure(t *testing.T) {
	g := makeTestGraph()
	affected := ComputeBlastRadius(g, "payment-svc")
	sort.Strings(affected)

	// Upstream: gateway, ingress (they call payment-svc transitively)
	// Downstream: postgres-db (payment-svc calls it)
	// But gateway also connects to inventory-svc which connects to postgres-db,
	// so the full blast should include everything reachable.
	if len(affected) == 0 {
		t.Fatal("expected non-empty blast radius")
	}

	// payment-svc itself should NOT be in the result
	for _, svc := range affected {
		if svc == "payment-svc" {
			t.Error("blast radius should not include the failed service itself")
		}
	}

	// At minimum, gateway (upstream) and postgres-db (downstream) should be affected
	has := func(name string) bool {
		return slices.Contains(affected, name)
	}
	if !has("gateway") {
		t.Error("expected gateway in blast radius (upstream caller)")
	}
	if !has("postgres-db") {
		t.Error("expected postgres-db in blast radius (downstream callee)")
	}
}

func TestComputeBlastRadius_DatabaseFailure(t *testing.T) {
	g := makeTestGraph()
	affected := ComputeBlastRadius(g, "postgres-db")
	sort.Strings(affected)

	// postgres-db is a leaf: upstream callers are payment-svc and inventory-svc,
	// which in turn are called by gateway, which is called by ingress.
	// No downstream from postgres-db.
	if len(affected) == 0 {
		t.Fatal("expected non-empty blast radius")
	}

	has := func(name string) bool {
		return slices.Contains(affected, name)
	}
	if !has("payment-svc") {
		t.Error("expected payment-svc in blast radius (calls postgres-db)")
	}
	if !has("inventory-svc") {
		t.Error("expected inventory-svc in blast radius (calls postgres-db)")
	}
	if !has("gateway") {
		t.Error("expected gateway in blast radius (calls payment-svc and inventory-svc)")
	}
}

func TestComputeBlastRadius_LeafService(t *testing.T) {
	g := makeTestGraph()
	affected := ComputeBlastRadius(g, "auth-svc")
	sort.Strings(affected)

	// auth-svc is a leaf with no downstream. Upstream: gateway -> ingress
	has := func(name string) bool {
		return slices.Contains(affected, name)
	}
	if !has("gateway") {
		t.Error("expected gateway in blast radius")
	}
	if !has("ingress") {
		t.Error("expected ingress in blast radius")
	}
}

func TestComputeBlastRadius_IngressFailure(t *testing.T) {
	g := makeTestGraph()
	affected := ComputeBlastRadius(g, "ingress")

	// ingress is the root. Downstream: everything. No upstream.
	if len(affected) == 0 {
		t.Fatal("expected non-empty blast radius")
	}
	// Should include all other services
	if len(affected) != 5 {
		t.Errorf("expected 5 affected services, got %d: %v", len(affected), affected)
	}
}

func TestComputeBlastRadius_NilGraph(t *testing.T) {
	result := ComputeBlastRadius(nil, "anything")
	if result != nil {
		t.Errorf("expected nil for nil graph, got %v", result)
	}
}

func TestComputeBlastRadius_UnknownService(t *testing.T) {
	g := makeTestGraph()
	result := ComputeBlastRadius(g, "nonexistent")
	if result != nil {
		t.Errorf("expected nil for unknown service, got %v", result)
	}
}

func TestComputeBlastRadius_SingleNode(t *testing.T) {
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "solo"})

	result := ComputeBlastRadius(g, "solo")
	if len(result) != 0 {
		t.Errorf("expected empty blast radius for isolated node, got %v", result)
	}
}

func TestComputeUpstreamBlastRadius(t *testing.T) {
	g := makeTestGraph()
	affected := ComputeUpstreamBlastRadius(g, "payment-svc")
	sort.Strings(affected)

	// Only upstream: gateway and ingress
	has := func(name string) bool {
		return slices.Contains(affected, name)
	}
	if !has("gateway") {
		t.Error("expected gateway in upstream blast radius")
	}
	if !has("ingress") {
		t.Error("expected ingress in upstream blast radius")
	}

	// Should NOT include downstream services
	if has("postgres-db") {
		t.Error("upstream blast radius should not include downstream services")
	}
}

func TestComputeUpstreamBlastRadius_NilGraph(t *testing.T) {
	result := ComputeUpstreamBlastRadius(nil, "test")
	if result != nil {
		t.Error("expected nil for nil graph")
	}
}

func TestComputeUpstreamBlastRadius_UnknownService(t *testing.T) {
	g := makeTestGraph()
	result := ComputeUpstreamBlastRadius(g, "nonexistent")
	if result != nil {
		t.Error("expected nil for unknown service")
	}
}
