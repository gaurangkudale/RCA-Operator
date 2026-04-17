// Package graph builds the incident topology graph — a compact JSON document
// linking the affected Kubernetes resources to the services / spans pulled
// from the originating distributed trace. The dashboard renders this graph in
// the "Incident Topology" panel; the controller persists it on the
// IncidentReport under status.incidentGraph as a runtime.RawExtension.
package graph

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/go-logr/logr"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// MaxGraphBytes is the hard ceiling for the serialized incident graph stored
// on the IncidentReport status. Graphs larger than this ceiling are truncated
// using the "keep root + highest-blast-radius" policy (see truncate).
const MaxGraphBytes = 64 * 1024 // 64 KiB

// NodeKind identifies what a node represents.
const (
	NodeKindPod        = "Pod"
	NodeKindDeployment = "Deployment"
	NodeKindNode       = "Node"
	NodeKindService    = "Service"  // A remote service observed via spans.
	NodeKindSpan       = "Span"     // An individual OTel span.
	NodeKindIncident   = "Incident" // The incident itself (root node).
)

// EdgeKind identifies what an edge represents.
const (
	EdgeKindOwns        = "owns"         // Deployment → Pod
	EdgeKindScheduledOn = "scheduled_on" // Pod → Node
	EdgeKindAffects     = "affects"      // Incident → Resource
	EdgeKindCalls       = "calls"        // Service → Service (span parent → child)
	EdgeKindRunsOn      = "runs_on"      // Span → Pod (via k8s.pod.name attr)
)

// Node is a single vertex in the incident topology graph.
type Node struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	Label       string `json:"label,omitempty"`
	IsRoot      bool   `json:"isRoot,omitempty"`
	BlastRadius int    `json:"blastRadius"`
}

// Edge is a directed link in the incident topology graph.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// IncidentGraph is the persisted topology view for a single incident.
type IncidentGraph struct {
	Nodes     []Node            `json:"nodes"`
	Edges     []Edge            `json:"edges"`
	Truncated bool              `json:"truncated,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// BufferSnapshotter is the minimum interface the builder needs to read
// OTel events correlated with the incident's trace. It is satisfied by
// *correlator.Buffer but is declared as an interface so tests can inject a
// fake without constructing the full correlator.
type BufferSnapshotter interface {
	SnapshotByTrace(traceID string) []correlator.Entry
}

// TraceFetcher abstracts the Jaeger client so tests can inject fakes and the
// builder can gracefully no-op when Jaeger is not configured.
type TraceFetcher interface {
	GetTrace(ctx context.Context, traceID string) (*jaeger.Trace, error)
}

// Builder assembles an IncidentGraph for a given IncidentReport by combining
// Kubernetes resource references (from the report's scope + affected
// resources) with service-call topology from the correlator's trace buffer
// and, optionally, a Jaeger query result.
type Builder struct {
	buffer BufferSnapshotter
	jaeger TraceFetcher
	log    logr.Logger
}

// NewBuilder constructs a Builder. Either dependency may be nil — a nil
// buffer disables trace-buffer enrichment, a nil jaeger client disables
// Jaeger enrichment. In both cases the builder still emits the K8s-only
// subgraph (incident root + affected resources).
func NewBuilder(buffer BufferSnapshotter, jaegerClient TraceFetcher, log logr.Logger) *Builder {
	return &Builder{buffer: buffer, jaeger: jaegerClient, log: log.WithName("incident-graph")}
}

// Build returns the incident topology graph for incident. The graph always
// contains at least the incident root node; enrichment from the trace buffer
// and Jaeger is best-effort.
func (b *Builder) Build(ctx context.Context, incident *rcav1alpha1.IncidentReport) (*IncidentGraph, error) {
	if incident == nil {
		return nil, nil
	}

	nodes := newNodeSet()
	var edges []Edge

	rootID := "incident:" + incident.Namespace + "/" + incident.Name
	nodes.add(Node{
		ID:        rootID,
		Kind:      NodeKindIncident,
		Name:      incident.Name,
		Namespace: incident.Namespace,
		Label:     incident.Status.IncidentType,
		IsRoot:    true,
	})

	// Seed from the incident's K8s resource inventory.
	for _, res := range incident.Status.AffectedResources {
		resID := k8sNodeID(res.Kind, res.Namespace, res.Name)
		nodes.add(Node{
			ID:        resID,
			Kind:      res.Kind,
			Name:      res.Name,
			Namespace: res.Namespace,
			Label:     res.Name,
		})
		edges = append(edges, Edge{From: rootID, To: resID, Kind: EdgeKindAffects})
	}

	// Attach the primary scope resource if it isn't already listed.
	if ref := incident.Spec.Scope.ResourceRef; ref != nil && ref.Name != "" {
		resID := k8sNodeID(ref.Kind, ref.Namespace, ref.Name)
		if nodes.add(Node{
			ID:        resID,
			Kind:      ref.Kind,
			Name:      ref.Name,
			Namespace: ref.Namespace,
			Label:     ref.Name,
		}) {
			edges = append(edges, Edge{From: rootID, To: resID, Kind: EdgeKindAffects})
		}
	}

	// Attach the workload ref + ownership edge when present.
	if ref := incident.Spec.Scope.WorkloadRef; ref != nil && ref.Name != "" {
		wlID := k8sNodeID(ref.Kind, ref.Namespace, ref.Name)
		nodes.add(Node{
			ID:        wlID,
			Kind:      ref.Kind,
			Name:      ref.Name,
			Namespace: ref.Namespace,
			Label:     ref.Name,
		})
		// Link the workload to any pod-kind affected resources in the same ns.
		for _, res := range incident.Status.AffectedResources {
			if res.Kind == NodeKindPod && res.Namespace == ref.Namespace {
				edges = append(edges, Edge{
					From: wlID,
					To:   k8sNodeID(res.Kind, res.Namespace, res.Name),
					Kind: EdgeKindOwns,
				})
			}
		}
	}

	traceID := ""
	if incident.Annotations != nil {
		traceID = incident.Annotations["rca.rca-operator.tech/trace-id"]
	}

	// Enrich from the trace buffer (already-observed OTel events).
	if traceID != "" && b.buffer != nil {
		for _, entry := range b.buffer.SnapshotByTrace(traceID) {
			b.enrichFromEvent(entry.Event, nodes, &edges, rootID)
		}
	}

	// Enrich from Jaeger (live trace topology) when available. Misses are
	// tolerated — a nil result means "no data", not "failure".
	if traceID != "" && b.jaeger != nil {
		trace, err := b.jaeger.GetTrace(ctx, traceID)
		if err != nil {
			b.log.V(1).Info("jaeger fetch failed, falling back to buffer-only graph",
				"traceID", traceID, "err", err.Error())
		} else if trace != nil {
			enrichFromJaeger(trace, nodes, &edges)
		}
	}

	sortedNodes := nodes.sorted()
	edges = dedupEdges(edges)

	g := &IncidentGraph{
		Nodes: sortedNodes,
		Edges: edges,
		Meta:  map[string]string{},
	}
	if traceID != "" {
		g.Meta["traceID"] = traceID
	}
	computeBlastRadius(g)

	// 64 KiB truncation: preserve the incident root + highest-blast-radius
	// nodes until the serialized graph fits under the ceiling.
	truncated, err := truncate(g, MaxGraphBytes)
	if err != nil {
		return nil, err
	}
	return truncated, nil
}

// enrichFromEvent folds a single OTel event from the trace buffer into the
// graph: the service-name + span name becomes a Service/Span node and any
// k8s.pod.name resource attribute is linked back to the Pod.
func (b *Builder) enrichFromEvent(e watcher.CorrelatorEvent, nodes *nodeSet, edges *[]Edge, rootID string) {
	var (
		serviceName string
		spanID      string
		podName     string
		namespace   string
	)
	switch ev := e.(type) {
	case watcher.OTelSpanErrorEvent:
		serviceName = ev.ServiceName
		spanID = ev.SpanID
		podName = ev.PodName
		namespace = ev.Namespace
	case watcher.OTelSpanLatencySpikeEvent:
		serviceName = ev.ServiceName
		spanID = ev.SpanID
		podName = ev.PodName
		namespace = ev.Namespace
	case watcher.OTelLogMatchEvent:
		serviceName = ev.ServiceName
		podName = ev.PodName
		namespace = ev.Namespace
	case watcher.OTelSpanEventEvent:
		serviceName = ev.ServiceName
		spanID = ev.SpanID
		podName = ev.PodName
		namespace = ev.Namespace
	default:
		return
	}

	if serviceName != "" {
		svcID := "svc:" + serviceName
		nodes.add(Node{
			ID:    svcID,
			Kind:  NodeKindService,
			Name:  serviceName,
			Label: serviceName,
		})
		*edges = append(*edges, Edge{From: rootID, To: svcID, Kind: EdgeKindAffects})

		if podName != "" {
			podID := k8sNodeID(NodeKindPod, namespace, podName)
			if nodes.add(Node{
				ID:        podID,
				Kind:      NodeKindPod,
				Name:      podName,
				Namespace: namespace,
				Label:     podName,
			}) {
				*edges = append(*edges, Edge{From: rootID, To: podID, Kind: EdgeKindAffects})
			}
			*edges = append(*edges, Edge{From: svcID, To: podID, Kind: EdgeKindRunsOn})
		}
	}

	if spanID != "" {
		spanNodeID := "span:" + spanID
		label := spanID
		if serviceName != "" {
			label = serviceName + " (" + spanID + ")"
		}
		nodes.add(Node{
			ID:    spanNodeID,
			Kind:  NodeKindSpan,
			Name:  spanID,
			Label: label,
		})
	}
}

// enrichFromJaeger folds a full Jaeger trace into the graph: each processID's
// service becomes a Service node, and each parent/child span pair becomes a
// calls edge between services. Pod identity is taken from the first span's
// k8s.pod.name tag when present.
func enrichFromJaeger(trace *jaeger.Trace, nodes *nodeSet, edges *[]Edge) {
	spanService := make(map[string]string, len(trace.Spans))
	for _, span := range trace.Spans {
		svc := trace.Processes[span.ProcessID].ServiceName
		spanService[span.SpanID] = svc

		if svc != "" {
			svcID := "svc:" + svc
			nodes.add(Node{ID: svcID, Kind: NodeKindService, Name: svc, Label: svc})
		}

		// k8s.pod.name tag associates the span's emitting pod when the
		// collector's k8sattributes processor is active.
		var podName, namespace string
		for _, tag := range span.Tags {
			key := tag.Key
			if key == "k8s.pod.name" {
				podName = asString(tag.Value)
			}
			if key == "k8s.namespace.name" {
				namespace = asString(tag.Value)
			}
		}
		if podName != "" && svc != "" {
			podID := k8sNodeID(NodeKindPod, namespace, podName)
			nodes.add(Node{ID: podID, Kind: NodeKindPod, Name: podName, Namespace: namespace, Label: podName})
			*edges = append(*edges, Edge{From: "svc:" + svc, To: podID, Kind: EdgeKindRunsOn})
		}
	}

	// Parent-child span edges → service-to-service calls.
	for _, span := range trace.Spans {
		childSvc := spanService[span.SpanID]
		for _, ref := range span.References {
			parentSvc := spanService[ref.SpanID]
			if parentSvc == "" || childSvc == "" || parentSvc == childSvc {
				continue
			}
			*edges = append(*edges, Edge{
				From: "svc:" + parentSvc,
				To:   "svc:" + childSvc,
				Kind: EdgeKindCalls,
			})
		}
	}
}

// asString best-effort coerces a Jaeger KeyValue.Value (any) to a string.
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return ""
	}
}

// nodeSet is an ordered, deduplicated node store keyed on Node.ID.
type nodeSet struct {
	order []string
	byID  map[string]Node
}

func newNodeSet() *nodeSet {
	return &nodeSet{byID: make(map[string]Node)}
}

// add inserts n if its ID is new. Returns true when the node was actually
// added — callers rely on this to emit the "incident → resource" edge only
// for first-sight resources.
func (s *nodeSet) add(n Node) bool {
	if _, ok := s.byID[n.ID]; ok {
		return false
	}
	s.byID[n.ID] = n
	s.order = append(s.order, n.ID)
	return true
}

// sorted returns nodes in deterministic insertion order.
func (s *nodeSet) sorted() []Node {
	out := make([]Node, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id])
	}
	return out
}

// dedupEdges removes exact duplicate (from, to, kind) tuples, preserving the
// first occurrence of each.
func dedupEdges(in []Edge) []Edge {
	seen := make(map[Edge]struct{}, len(in))
	out := make([]Edge, 0, len(in))
	for _, e := range in {
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

// computeBlastRadius sets BlastRadius on each node to the count of inbound
// edges targeting it. Root nodes always score at least 1 so they survive
// truncation regardless of whether any edges point at them.
func computeBlastRadius(g *IncidentGraph) {
	inbound := make(map[string]int, len(g.Nodes))
	outbound := make(map[string]int, len(g.Nodes))
	for _, e := range g.Edges {
		inbound[e.To]++
		outbound[e.From]++
	}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		n.BlastRadius = inbound[n.ID] + outbound[n.ID]
		if n.IsRoot && n.BlastRadius == 0 {
			n.BlastRadius = 1
		}
	}
}

// k8sNodeID builds a stable graph-node id for a K8s resource.
func k8sNodeID(kind, namespace, name string) string {
	var b strings.Builder
	b.WriteString("k8s:")
	b.WriteString(kind)
	b.WriteByte(':')
	if namespace != "" {
		b.WriteString(namespace)
		b.WriteByte('/')
	}
	b.WriteString(name)
	return b.String()
}

// truncate returns a graph whose JSON encoding is at most maxBytes. When the
// original graph already fits, it is returned unchanged. When it overflows,
// non-root nodes are dropped in ascending-blast-radius order (ties broken by
// node id for determinism) until the encoding fits; edges touching dropped
// nodes are dropped with them. The incident root is always preserved.
func truncate(g *IncidentGraph, maxBytes int) (*IncidentGraph, error) {
	encoded, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= maxBytes {
		return g, nil
	}

	// Rank non-root nodes by (blastRadius asc, id asc) — lowest-impact first.
	type rank struct {
		id     string
		weight int
	}
	var ranked []rank
	for _, n := range g.Nodes {
		if n.IsRoot {
			continue
		}
		ranked = append(ranked, rank{id: n.ID, weight: n.BlastRadius})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].weight == ranked[j].weight {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].weight < ranked[j].weight
	})

	dropped := make(map[string]struct{})
	for _, r := range ranked {
		dropped[r.id] = struct{}{}
		trimmed := filter(g, dropped)
		buf, err := json.Marshal(trimmed)
		if err != nil {
			return nil, err
		}
		if len(buf) <= maxBytes {
			trimmed.Truncated = true
			return trimmed, nil
		}
	}

	// Worst case: only the root survives.
	rootOnly := filter(g, nodeIDSetExcept(g, rootID(g)))
	rootOnly.Truncated = true
	return rootOnly, nil
}

// filter returns a copy of g with every node whose ID is in dropped removed,
// and every edge touching a dropped node removed.
func filter(g *IncidentGraph, dropped map[string]struct{}) *IncidentGraph {
	out := &IncidentGraph{
		Meta: g.Meta,
	}
	for _, n := range g.Nodes {
		if _, drop := dropped[n.ID]; drop {
			continue
		}
		out.Nodes = append(out.Nodes, n)
	}
	for _, e := range g.Edges {
		if _, drop := dropped[e.From]; drop {
			continue
		}
		if _, drop := dropped[e.To]; drop {
			continue
		}
		out.Edges = append(out.Edges, e)
	}
	return out
}

// rootID returns the first root node's id in g, or "" when none is present.
func rootID(g *IncidentGraph) string {
	for _, n := range g.Nodes {
		if n.IsRoot {
			return n.ID
		}
	}
	return ""
}

// nodeIDSetExcept returns a set of every node id in g except exceptID.
func nodeIDSetExcept(g *IncidentGraph, exceptID string) map[string]struct{} {
	out := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.ID == exceptID {
			continue
		}
		out[n.ID] = struct{}{}
	}
	return out
}
