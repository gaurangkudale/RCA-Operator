package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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
	source := "unavailable"
	deps := []jaeger.Dependency{}
	if b.jaeger != nil {
		source = "jaeger"
		var err error
		deps, err = b.jaeger.GetDependencies(ctx, lookback, time.Time{})
		if err != nil {
			// Do not hard-fail the dashboard when Jaeger dependencies are
			// temporarily unavailable. We still attempt to build a useful
			// graph from open incident topology snapshots.
			b.log.V(1).Info("Jaeger dependencies unavailable; using fallback", "err", err.Error())
			source = "jaeger-error"
			deps = nil
		}
	}

	// Build incident overlay so service nodes can be coloured by severity.
	var incidents rcav1alpha1.IncidentReportList
	var services corev1.ServiceList
	if b.k8s != nil {
		if err := b.k8s.List(ctx, &incidents); err != nil {
			b.log.V(1).Info("list incidents failed; proceeding without overlay", "err", err)
		}
		if err := b.k8s.List(ctx, &services); err != nil {
			b.log.V(1).Info("list services failed; proceeding without namespace enrichment", "err", err)
		}
	}
	serviceStatus := buildServiceOverlay(incidents.Items)
	serviceNamespace := buildServiceNamespaceLookup(services.Items)
	fallbackNodes, fallbackEdges := buildIncidentServiceFallback(incidents.Items)

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
		namespace := resolveServiceNamespace(name, serviceNamespace)
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:        "service:" + name,
				Kind:      NodeKindService,
				Name:      name,
				Namespace: namespace,
				Label:     name,
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

	if serviceGraphSparse(g) && (len(fallbackNodes) > 0 || len(fallbackEdges) > 0) {
		for _, name := range fallbackNodes {
			addService(name)
		}
		for _, e := range fallbackEdges {
			addService(strings.TrimPrefix(e.From, "service:"))
			addService(strings.TrimPrefix(e.To, "service:"))
			g.Edges = append(g.Edges, e)
		}
		switch source {
		case "jaeger":
			source = "jaeger+incident-fallback"
		default:
			source = "incident-graph"
		}
	}

	g.Edges = dedupeCallEdges(g.Edges)

	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})

	g.Meta["source"] = source
	g.Meta["lookbackSeconds"] = fmt.Sprintf("%d", int(lookback.Seconds()))
	g.Meta["nodes"] = fmt.Sprintf("%d", len(g.Nodes))
	g.Meta["edges"] = fmt.Sprintf("%d", len(g.Edges))
	return g, nil
}

func serviceGraphSparse(g *ClusterGraph) bool {
	if g == nil {
		return true
	}
	if len(g.Nodes) <= 1 {
		return true
	}
	for _, e := range g.Edges {
		if e.Kind == EdgeKindCalls && e.From != e.To {
			return false
		}
	}
	return true
}

func buildIncidentServiceFallback(list []rcav1alpha1.IncidentReport) ([]string, []Edge) {
	services := map[string]struct{}{}
	edgeCounts := map[string]int64{}

	for i := range list {
		inc := list[i]
		phase := strings.ToLower(inc.Status.Phase)
		if phase == "resolved" || phase == "closed" {
			continue
		}
		raw := inc.Status.IncidentGraph
		if raw == nil || len(raw.Raw) == 0 {
			continue
		}
		var ig IncidentGraph
		if err := json.Unmarshal(raw.Raw, &ig); err != nil {
			continue
		}
		idToService := map[string]string{}
		for _, n := range ig.Nodes {
			if n.Kind != NodeKindService {
				continue
			}
			name := strings.TrimSpace(n.Name)
			if name == "" && strings.HasPrefix(n.ID, "service:") {
				name = strings.TrimPrefix(n.ID, "service:")
			}
			if name == "" {
				continue
			}
			services[name] = struct{}{}
			idToService[n.ID] = name
		}

		for _, e := range ig.Edges {
			if e.Kind != EdgeKindCalls {
				continue
			}
			fromName, okFrom := idToService[e.From]
			toName, okTo := idToService[e.To]
			if !okFrom || !okTo || fromName == "" || toName == "" || fromName == toName {
				continue
			}
			fromID := "service:" + fromName
			toID := "service:" + toName
			key := fromID + "\x00" + toID
			incCount := e.Count
			if incCount <= 0 {
				incCount = 1
			}
			edgeCounts[key] += incCount
		}
	}

	nodeNames := make([]string, 0, len(services))
	for name := range services {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)

	edges := make([]Edge, 0, len(edgeCounts))
	for key, count := range edgeCounts {
		parts := strings.Split(key, "\x00")
		if len(parts) != 2 {
			continue
		}
		edges = append(edges, Edge{From: parts[0], To: parts[1], Kind: EdgeKindCalls, Count: count})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	return nodeNames, edges
}

func dedupeCallEdges(edges []Edge) []Edge {
	out := make([]Edge, 0, len(edges))
	counts := map[string]int64{}
	nonCall := make([]Edge, 0)
	for _, e := range edges {
		if e.Kind != EdgeKindCalls {
			nonCall = append(nonCall, e)
			continue
		}
		key := e.From + "\x00" + e.To
		counts[key] += e.Count
	}
	for key, c := range counts {
		parts := strings.Split(key, "\x00")
		if len(parts) != 2 {
			continue
		}
		out = append(out, Edge{From: parts[0], To: parts[1], Kind: EdgeKindCalls, Count: c})
	}
	out = append(out, nonCall...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// buildServiceNamespaceLookup maps service-name -> namespace when the mapping
// is unambiguous in non-system namespaces. Ambiguous entries are stored as an
// empty string and ignored by resolveServiceNamespace.
func buildServiceNamespaceLookup(services []corev1.Service) map[string]string {
	out := map[string]string{}
	for i := range services {
		svc := services[i]
		if svc.Name == "" || svc.Namespace == "" || systemNamespaces[svc.Namespace] {
			continue
		}
		if existing, ok := out[svc.Name]; !ok {
			out[svc.Name] = svc.Namespace
		} else if existing != svc.Namespace {
			out[svc.Name] = ""
		}
	}
	return out
}

// resolveServiceNamespace resolves Jaeger service names to Kubernetes
// namespaces by exact name first, then "name.namespace" style prefixes.
func resolveServiceNamespace(serviceName string, lookup map[string]string) string {
	if ns := lookup[serviceName]; ns != "" {
		return ns
	}
	base := serviceName
	if dot := strings.Index(serviceName, "."); dot > 0 {
		base = serviceName[:dot]
	}
	if ns := lookup[base]; ns != "" {
		return ns
	}
	return ""
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
