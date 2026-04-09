package topology

import (
	"sort"
	"testing"

	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

func TestNewServiceGraph(t *testing.T) {
	g := NewServiceGraph()
	if g.Nodes == nil {
		t.Fatal("expected non-nil Nodes map")
	}
	if len(g.Nodes) != 0 {
		t.Errorf("expected empty graph, got %d nodes", len(g.Nodes))
	}
}

func TestServiceGraph_AddNode(t *testing.T) {
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "api-gateway", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "payment-svc", Status: telemetry.HealthStatusCritical})

	if len(g.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(g.Nodes))
	}
	if g.Nodes["api-gateway"].Status != telemetry.HealthStatusHealthy {
		t.Error("expected api-gateway to be healthy")
	}
	if g.Nodes["payment-svc"].Status != telemetry.HealthStatusCritical {
		t.Error("expected payment-svc to be critical")
	}
}

func TestServiceGraph_AddNode_Overwrite(t *testing.T) {
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "svc", Status: telemetry.HealthStatusHealthy})
	g.AddNode(&ServiceNode{Name: "svc", Status: telemetry.HealthStatusCritical})

	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node after overwrite, got %d", len(g.Nodes))
	}
	if g.Nodes["svc"].Status != telemetry.HealthStatusCritical {
		t.Error("expected overwritten status to be critical")
	}
}

func TestServiceGraph_AddEdge(t *testing.T) {
	g := NewServiceGraph()
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "payment", Status: telemetry.EdgeStatusActive})

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	// AddEdge should auto-create nodes
	if len(g.Nodes) != 2 {
		t.Fatalf("expected 2 auto-created nodes, got %d", len(g.Nodes))
	}
	if g.Nodes["gateway"].Status != telemetry.HealthStatusUnknown {
		t.Error("auto-created node should have Unknown status")
	}
}

func TestServiceGraph_AddEdge_ExistingNodes(t *testing.T) {
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "gateway", Status: telemetry.HealthStatusHealthy})
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "payment", Status: telemetry.EdgeStatusActive})

	// Should not overwrite existing node
	if g.Nodes["gateway"].Status != telemetry.HealthStatusHealthy {
		t.Error("AddEdge should not overwrite existing node")
	}
	// But should create missing target
	if _, ok := g.Nodes["payment"]; !ok {
		t.Error("expected payment node to be auto-created")
	}
}

func TestServiceGraph_Neighbors(t *testing.T) {
	g := NewServiceGraph()
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "auth"})
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "payment"})
	g.AddEdge(ServiceEdge{Source: "payment", Target: "db"})

	neighbors := g.Neighbors("gateway")
	sort.Strings(neighbors)
	if len(neighbors) != 2 {
		t.Fatalf("expected 2 neighbors, got %d", len(neighbors))
	}
	if neighbors[0] != "auth" || neighbors[1] != "payment" {
		t.Errorf("unexpected neighbors: %v", neighbors)
	}

	// Node with no outgoing edges
	dbNeighbors := g.Neighbors("db")
	if len(dbNeighbors) != 0 {
		t.Errorf("expected 0 neighbors for db, got %d", len(dbNeighbors))
	}
}

func TestServiceGraph_Dependents(t *testing.T) {
	g := NewServiceGraph()
	g.AddEdge(ServiceEdge{Source: "gateway", Target: "payment"})
	g.AddEdge(ServiceEdge{Source: "frontend", Target: "payment"})
	g.AddEdge(ServiceEdge{Source: "payment", Target: "db"})

	dependents := g.Dependents("payment")
	sort.Strings(dependents)
	if len(dependents) != 2 {
		t.Fatalf("expected 2 dependents, got %d", len(dependents))
	}
	if dependents[0] != "frontend" || dependents[1] != "gateway" {
		t.Errorf("unexpected dependents: %v", dependents)
	}

	// Node with no incoming edges
	gwDeps := g.Dependents("gateway")
	if len(gwDeps) != 0 {
		t.Errorf("expected 0 dependents for gateway, got %d", len(gwDeps))
	}
}

func TestServiceGraph_ServiceNames(t *testing.T) {
	g := NewServiceGraph()
	g.AddNode(&ServiceNode{Name: "a"})
	g.AddNode(&ServiceNode{Name: "b"})
	g.AddNode(&ServiceNode{Name: "c"})

	names := g.ServiceNames()
	sort.Strings(names)
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("unexpected names: %v", names)
	}
}
