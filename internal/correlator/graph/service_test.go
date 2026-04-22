package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

type fakeDepsFetcher struct {
	deps []jaeger.Dependency
	err  error
}

func (f fakeDepsFetcher) GetDependencies(_ context.Context, _ time.Duration, _ time.Time) ([]jaeger.Dependency, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.deps, nil
}

func newServiceGraphTestClient(t *testing.T, objs ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca scheme: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, obj := range objs {
		builder = builder.WithRuntimeObjects(obj)
	}
	return builder
}

func TestServiceGraphBuild_AttachesResolvedNamespace(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "shipping", Namespace: "rca-demo"}}
	k8s := newServiceGraphTestClient(t, svc).Build()

	b := NewServiceGraphBuilder(k8s, fakeDepsFetcher{deps: []jaeger.Dependency{{Parent: "checkout", Child: "shipping", CallCount: 56}}}, logr.Discard())
	g, err := b.Build(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	var shippingNode *ClusterNode
	for i := range g.Nodes {
		if g.Nodes[i].Kind == NodeKindService && g.Nodes[i].Name == "shipping" {
			shippingNode = &g.Nodes[i]
			break
		}
	}
	if shippingNode == nil {
		t.Fatalf("shipping node not found in graph")
	}
	if shippingNode.Namespace != "rca-demo" {
		t.Fatalf("shipping namespace = %q, want %q", shippingNode.Namespace, "rca-demo")
	}
}

func TestServiceGraphBuild_LeavesNamespaceEmptyWhenAmbiguous(t *testing.T) {
	svcA := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "shipping", Namespace: "rca-demo"}}
	svcB := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "shipping", Namespace: "staging"}}
	k8s := newServiceGraphTestClient(t, svcA, svcB).Build()

	b := NewServiceGraphBuilder(k8s, fakeDepsFetcher{deps: []jaeger.Dependency{{Parent: "checkout", Child: "shipping", CallCount: 10}}}, logr.Discard())
	g, err := b.Build(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	for i := range g.Nodes {
		if g.Nodes[i].Kind == NodeKindService && g.Nodes[i].Name == "shipping" {
			if g.Nodes[i].Namespace != "" {
				t.Fatalf("shipping namespace = %q, want empty when ambiguous", g.Nodes[i].Namespace)
			}
			return
		}
	}
	t.Fatalf("shipping node not found in graph")
}

func TestServiceGraphBuild_FallbackFromIncidentGraphWhenJaegerUnavailable(t *testing.T) {
	inc := incidentWithGraph(t, "rca-demo", "inc-1", IncidentGraph{
		Nodes: []Node{
			{ID: "service:frontend", Kind: NodeKindService, Name: "frontend"},
			{ID: "service:checkout", Kind: NodeKindService, Name: "checkout"},
		},
		Edges: []Edge{{From: "service:frontend", To: "service:checkout", Kind: EdgeKindCalls, Count: 7}},
	})
	k8s := newServiceGraphTestClient(t, inc).Build()

	b := NewServiceGraphBuilder(k8s, nil, logr.Discard())
	g, err := b.Build(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if got := g.Meta["source"]; got != "incident-graph" {
		t.Fatalf("source = %q, want incident-graph", got)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(g.Edges))
	}
}

func TestServiceGraphBuild_MergesIncidentFallbackWhenJaegerSparse(t *testing.T) {
	inc := incidentWithGraph(t, "rca-demo", "inc-2", IncidentGraph{
		Nodes: []Node{
			{ID: "service:frontend", Kind: NodeKindService, Name: "frontend"},
			{ID: "service:checkout", Kind: NodeKindService, Name: "checkout"},
		},
		Edges: []Edge{{From: "service:frontend", To: "service:checkout", Kind: EdgeKindCalls, Count: 3}},
	})
	k8s := newServiceGraphTestClient(t, inc).Build()

	deps := []jaeger.Dependency{{Parent: "frontend", Child: "frontend", CallCount: 10}}
	b := NewServiceGraphBuilder(k8s, fakeDepsFetcher{deps: deps}, logr.Discard())
	g, err := b.Build(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if got := g.Meta["source"]; got != "jaeger+incident-fallback" {
		t.Fatalf("source = %q, want jaeger+incident-fallback", got)
	}
	if !hasCallEdge(g, "service:frontend", "service:checkout") {
		t.Fatalf("expected merged frontend->checkout call edge")
	}
}

func TestServiceGraphBuild_JaegerErrorStillUsesIncidentFallback(t *testing.T) {
	inc := incidentWithGraph(t, "rca-demo", "inc-3", IncidentGraph{
		Nodes: []Node{{ID: "service:payments", Kind: NodeKindService, Name: "payments"}},
	})
	k8s := newServiceGraphTestClient(t, inc).Build()

	b := NewServiceGraphBuilder(k8s, fakeDepsFetcher{err: errors.New("jaeger down")}, logr.Discard())
	g, err := b.Build(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if got := g.Meta["source"]; got != "incident-graph" {
		t.Fatalf("source = %q, want incident-graph", got)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Name != "payments" {
		t.Fatalf("unexpected nodes: %+v", g.Nodes)
	}
}

func incidentWithGraph(t *testing.T, ns, name string, g IncidentGraph) *rcav1alpha1.IncidentReport {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal incident graph: %v", err)
	}
	return &rcav1alpha1.IncidentReport{
		TypeMeta:   metav1.TypeMeta{APIVersion: schema.GroupVersion{Group: "rca.rca-operator.tech", Version: "v1alpha1"}.String(), Kind: "IncidentReport"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			Severity:     "P2",
			IncidentType: "OTelSpanError",
			IncidentGraph: &runtime.RawExtension{
				Raw: raw,
			},
		},
	}
}

func hasCallEdge(g *ClusterGraph, from, to string) bool {
	for _, e := range g.Edges {
		if e.Kind == EdgeKindCalls && e.From == from && e.To == to {
			return true
		}
	}
	return false
}
