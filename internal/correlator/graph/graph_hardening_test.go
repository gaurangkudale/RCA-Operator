package graph

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

// TestEnrichFromJaeger_NilTrace ensures the helper is a no-op on nil input
// rather than panicking when Jaeger returns no trace.
func TestEnrichFromJaeger_NilTrace(t *testing.T) {
	nodes := newNodeSet()
	var edges []Edge
	enrichFromJaeger(nil, nodes, &edges)
	if len(nodes.sorted()) != 0 || len(edges) != 0 {
		t.Fatalf("expected empty graph, got %d nodes, %d edges", len(nodes.sorted()), len(edges))
	}
}

// TestEnrichFromJaeger_MissingProcessID exercises the guarded Processes lookup.
// A span whose ProcessID is absent from trace.Processes must not produce a
// service node with an empty name and must not panic.
func TestEnrichFromJaeger_MissingProcessID(t *testing.T) {
	trace := &jaeger.Trace{
		Spans: []jaeger.Span{
			{SpanID: "s1", ProcessID: "p-missing"},
			{SpanID: "s2", ProcessID: "p1"},
		},
		Processes: map[string]jaeger.Process{
			"p1": {ServiceName: "checkout"},
		},
	}
	nodes := newNodeSet()
	var edges []Edge
	enrichFromJaeger(trace, nodes, &edges)

	for _, n := range nodes.sorted() {
		if n.Kind == NodeKindService && n.Name == "" {
			t.Fatalf("empty-name service node leaked into graph: %+v", n)
		}
		if n.ID == "svc:" {
			t.Fatalf("empty-svc node id leaked into graph")
		}
	}
}

// TestTruncate_NilGraph guards against nil input.
func TestTruncate_NilGraph(t *testing.T) {
	out, err := truncate(nil, 1024)
	if err != nil {
		t.Fatalf("truncate(nil) returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("truncate(nil) expected nil, got %+v", out)
	}
}

// TestTruncate_NoRoot_KeepsHighestBlastRadiusNode verifies the defensive
// fallback when a graph somehow lacks an IsRoot node and overflows the budget.
// The truncated graph must not be empty: the highest-blast-radius node should
// survive so the dashboard has something to render.
func TestTruncate_NoRoot_KeepsHighestBlastRadiusNode(t *testing.T) {
	// Build many nodes with no IsRoot so the budget is overrun and the worst-
	// case branch executes. Give one node a clearly higher blast radius so the
	// fallback has a deterministic winner.
	g := &IncidentGraph{}
	for i := range 200 {
		g.Nodes = append(g.Nodes, Node{
			ID:          "node-" + strings.Repeat("x", 20) + "-" + strconv.Itoa(i),
			Kind:        NodeKindPod,
			Name:        "pod-" + strconv.Itoa(i),
			Label:       strings.Repeat("y", 50),
			BlastRadius: 1,
		})
	}
	g.Nodes = append(g.Nodes, Node{
		ID:          "winner",
		Kind:        NodeKindPod,
		Name:        "winner",
		BlastRadius: 99,
	})

	out, err := truncate(g, 256)
	if err != nil {
		t.Fatalf("truncate returned error: %v", err)
	}
	if out == nil {
		t.Fatalf("truncate returned nil graph")
	}
	if !out.Truncated {
		t.Fatalf("expected Truncated=true on overflow")
	}
	if len(out.Nodes) == 0 {
		t.Fatalf("expected at least one surviving node, got 0")
	}
	if len(out.Nodes) == 1 && out.Nodes[0].ID != "winner" {
		t.Fatalf("expected winner node to survive, got %q", out.Nodes[0].ID)
	}
}

// TestHighestBlastRadiusID_TieBreak verifies deterministic id-ordered tie-break.
func TestHighestBlastRadiusID_TieBreak(t *testing.T) {
	g := &IncidentGraph{Nodes: []Node{
		{ID: "b", BlastRadius: 5},
		{ID: "a", BlastRadius: 5},
		{ID: "c", BlastRadius: 3},
	}}
	if got := highestBlastRadiusID(g); got != "a" {
		t.Fatalf("expected tie broken to lowest id 'a', got %q", got)
	}
}

func TestHighestBlastRadiusID_Empty(t *testing.T) {
	if got := highestBlastRadiusID(nil); got != "" {
		t.Fatalf("expected empty id for nil graph, got %q", got)
	}
	if got := highestBlastRadiusID(&IncidentGraph{}); got != "" {
		t.Fatalf("expected empty id for empty graph, got %q", got)
	}
}

// TestBuilder_NilJaegerTrace_DoesNotPanic threads a nil Jaeger trace through
// the full Build path to lock in the resilience contract.
func TestBuilder_NilJaegerTrace_DoesNotPanic(t *testing.T) {
	b := NewBuilder(nil, nilTraceFetcher{}, logr.Discard())
	incident := &rcav1alpha1.IncidentReport{}
	incident.Name = "i1"
	incident.Namespace = "ns1"
	incident.Annotations = map[string]string{"rca.rca-operator.tech/trace-id": "abc"}

	g, err := b.Build(context.Background(), incident)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if g == nil || len(g.Nodes) == 0 {
		t.Fatalf("Build returned empty graph")
	}
}

type nilTraceFetcher struct{}

func (nilTraceFetcher) GetTrace(_ context.Context, _ string) (*jaeger.Trace, error) {
	return nil, nil
}
