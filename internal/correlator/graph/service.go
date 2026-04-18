package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

// ServiceDependencyFetcher is the subset of the Jaeger client used by the
// service-graph builder. Declared as an interface so tests can inject a fake.
type ServiceDependencyFetcher interface {
	GetDependencies(ctx context.Context, lookback time.Duration, endTs time.Time) ([]jaeger.Dependency, error)
}

// ServiceGraphBuilder produces a Jaeger-style service dependency graph
// (nodes = services, edges = calls with counts) suitable for rendering in the
// dashboard topology view. Status on each service node is overlaid from open
// IncidentReports so the UI can colour unhealthy services red/amber.
type ServiceGraphBuilder struct {
	k8s    client.Client
	jaeger ServiceDependencyFetcher
	log    logr.Logger
}

// NewServiceGraphBuilder returns a builder. jaeger may be nil, in which case
// Build returns an empty graph (handler surfaces that as 503-capable 204).
func NewServiceGraphBuilder(k8s client.Client, jc ServiceDependencyFetcher, log logr.Logger) *ServiceGraphBuilder {
	return &ServiceGraphBuilder{k8s: k8s, jaeger: jc, log: log.WithName("service-graph")}
}

// Build returns the current cluster-wide service dependency graph for the
// given lookback window.
func (b *ServiceGraphBuilder) Build(ctx context.Context, lookback time.Duration) (*ClusterGraph, error) {
	g := &ClusterGraph{Meta: map[string]string{}, Nodes: []ClusterNode{}, Edges: []Edge{}}
	if b.jaeger == nil {
		g.Meta["source"] = "unavailable"
		return g, nil
	}

	deps, err := b.jaeger.GetDependencies(ctx, lookback, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("jaeger dependencies: %w", err)
	}

	// Build incident overlay so service nodes can be coloured by severity.
	var incidents rcav1alpha1.IncidentReportList
	if b.k8s != nil {
		if err := b.k8s.List(ctx, &incidents); err != nil {
			b.log.V(1).Info("list incidents failed; proceeding without overlay", "err", err)
		}
	}
	serviceStatus := buildServiceOverlay(incidents.Items)

	seen := map[string]struct{}{}
	addService := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		status := serviceStatus[name]
		if status == "" {
			status = StatusHealthy
		}
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:    "service:" + name,
				Kind:  NodeKindService,
				Name:  name,
				Label: name,
			},
			Status: status,
		})
	}

	for _, d := range deps {
		addService(d.Parent)
		addService(d.Child)
		if d.Parent == "" || d.Child == "" {
			continue
		}
		g.Edges = append(g.Edges, Edge{
			From:  "service:" + d.Parent,
			To:    "service:" + d.Child,
			Kind:  EdgeKindCalls,
			Count: d.CallCount,
		})
	}

	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})

	g.Meta["source"] = "jaeger"
	g.Meta["lookbackSeconds"] = fmt.Sprintf("%d", int(lookback.Seconds()))
	g.Meta["nodes"] = fmt.Sprintf("%d", len(g.Nodes))
	g.Meta["edges"] = fmt.Sprintf("%d", len(g.Edges))
	return g, nil
}

// buildServiceOverlay maps service-name → worst status across open incidents.
// Matching is deliberately lenient: in real clusters an OTel-derived signal
// typically attaches the AffectedResource to the backing workload (Deployment,
// StatefulSet, DaemonSet, Pod) rather than the Service object itself — via
// applyOTelScopeOverrides in signals/normalizer.go. Without the workload
// fallback below the Service node in the dashboard stays green even while the
// Workload view correctly flags the incident.
func buildServiceOverlay(list []rcav1alpha1.IncidentReport) map[string]string {
	out := map[string]string{}
	for _, inc := range list {
		phase := strings.ToLower(inc.Status.Phase)
		if phase == "resolved" || phase == "closed" {
			continue
		}
		status := severityToStatus(inc.Status.Severity)
		for _, r := range inc.Status.AffectedResources {
			if r.Name == "" {
				continue
			}
			switch r.Kind {
			case "Service",
				"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet",
				"Pod":
				out[r.Name] = pickWorse(out[r.Name], status)
			}
		}
	}
	return out
}
