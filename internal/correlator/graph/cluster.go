package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// StatusHealthy / StatusWarning / StatusCritical are the severity colours the
// dashboard applies to topology nodes.
const (
	StatusHealthy  = "healthy"
	StatusWarning  = "warning"
	StatusCritical = "critical"
)

// ClusterNode extends Node with a status field used by the cluster-wide
// topology view. It marshals with the same shape as Node so the dashboard
// renderer can consume either payload.
type ClusterNode struct {
	Node
	Status string `json:"status,omitempty"`
}

// ClusterGraph is the cluster-wide counterpart to IncidentGraph.
type ClusterGraph struct {
	Nodes     []ClusterNode     `json:"nodes"`
	Edges     []Edge            `json:"edges"`
	Truncated bool              `json:"truncated,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// ClusterBuilder assembles a cluster-wide topology graph from live Kubernetes
// state plus any open IncidentReports (for status overlay).
type ClusterBuilder struct {
	k8s client.Client
	log logr.Logger
}

// NewClusterBuilder returns a ClusterBuilder reading from the given client.
func NewClusterBuilder(k8s client.Client, log logr.Logger) *ClusterBuilder {
	return &ClusterBuilder{k8s: k8s, log: log.WithName("cluster-graph")}
}

// Build lists Deployments, Pods, Nodes, and Services across all namespaces and
// returns a ClusterGraph. Open IncidentReports are overlaid as node-level
// status (critical for P1/P2, warning for P3+, else healthy).
func (b *ClusterBuilder) Build(ctx context.Context) (*ClusterGraph, error) {
	g := &ClusterGraph{Meta: map[string]string{}}

	var deps appsv1.DeploymentList
	if err := b.k8s.List(ctx, &deps); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	var pods corev1.PodList
	if err := b.k8s.List(ctx, &pods); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	var nodes corev1.NodeList
	if err := b.k8s.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var svcs corev1.ServiceList
	if err := b.k8s.List(ctx, &svcs); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var incidents rcav1alpha1.IncidentReportList
	if err := b.k8s.List(ctx, &incidents); err != nil {
		b.log.V(1).Info("list incidents failed; proceeding without overlay", "err", err)
	}

	// Index open incidents by (ns/podName) and (ns/deploymentName) and node.
	podAffect := map[string]string{}  // ns/pod -> status
	depAffect := map[string]string{}  // ns/deployment -> status
	nodeAffect := map[string]string{} // node -> status
	nsAffect := map[string]string{}   // ns -> status

	for _, inc := range incidents.Items {
		phase := strings.ToLower(inc.Status.Phase)
		if phase == "resolved" || phase == "closed" {
			continue
		}
		status := severityToStatus(inc.Status.Severity)
		for _, r := range inc.Status.AffectedResources {
			key := inc.Namespace + "/" + r.Name
			switch r.Kind {
			case "Pod":
				podAffect[key] = pickWorse(podAffect[key], status)
			case "Deployment":
				depAffect[key] = pickWorse(depAffect[key], status)
			case "Node":
				nodeAffect[r.Name] = pickWorse(nodeAffect[r.Name], status)
			case "Namespace":
				nsAffect[r.Name] = pickWorse(nsAffect[r.Name], status)
			}
		}
	}

	// systemNamespaces are skipped so the cluster graph stays focused on workloads.
	skipNS := map[string]bool{
		"kube-system":        true,
		"kube-public":        true,
		"kube-node-lease":    true,
		"local-path-storage": true,
	}

	// Nodes (k8s worker nodes)
	for _, n := range nodes.Items {
		status := nodeReadinessStatus(&n)
		if ov := nodeAffect[n.Name]; ov != "" {
			status = pickWorse(status, ov)
		}
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:    "node:" + n.Name,
				Kind:  NodeKindNode,
				Name:  n.Name,
				Label: n.Name,
			},
			Status: status,
		})
	}

	// Deployments
	for _, d := range deps.Items {
		if skipNS[d.Namespace] {
			continue
		}
		key := d.Namespace + "/" + d.Name
		status := deploymentStatus(&d)
		if ov := depAffect[key]; ov != "" {
			status = pickWorse(status, ov)
		}
		if ov := nsAffect[d.Namespace]; ov != "" {
			status = pickWorse(status, ov)
		}
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:        "deploy:" + key,
				Kind:      NodeKindDeployment,
				Name:      d.Name,
				Namespace: d.Namespace,
				Label:     d.Name,
			},
			Status: status,
		})
	}

	// Pods → edges to owning Deployment (by ownerRef) and to scheduled Node.
	for _, p := range pods.Items {
		if skipNS[p.Namespace] {
			continue
		}
		key := p.Namespace + "/" + p.Name
		status := podStatus(&p)
		if ov := podAffect[key]; ov != "" {
			status = pickWorse(status, ov)
		}
		id := "pod:" + key
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:        id,
				Kind:      NodeKindPod,
				Name:      p.Name,
				Namespace: p.Namespace,
				Label:     p.Name,
			},
			Status: status,
		})
		if p.Spec.NodeName != "" {
			g.Edges = append(g.Edges, Edge{From: id, To: "node:" + p.Spec.NodeName, Kind: EdgeKindScheduledOn})
		}
		if owner := deploymentOwnerFromPod(&p, deps.Items); owner != "" {
			g.Edges = append(g.Edges, Edge{From: "deploy:" + p.Namespace + "/" + owner, To: id, Kind: EdgeKindOwns})
		}
	}

	// Services as standalone nodes (no edges yet; selector-based matching can
	// be added later if the UI needs it).
	for _, s := range svcs.Items {
		if skipNS[s.Namespace] {
			continue
		}
		g.Nodes = append(g.Nodes, ClusterNode{
			Node: Node{
				ID:        "svc:" + s.Namespace + "/" + s.Name,
				Kind:      NodeKindService,
				Name:      s.Name,
				Namespace: s.Namespace,
				Label:     s.Name,
			},
			Status: StatusHealthy,
		})
	}

	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})

	g.Meta["nodes"] = fmt.Sprintf("%d", len(g.Nodes))
	g.Meta["edges"] = fmt.Sprintf("%d", len(g.Edges))

	return g, nil
}

func severityToStatus(sev string) string {
	switch strings.ToUpper(sev) {
	case "P1", "P2", "CRITICAL":
		return StatusCritical
	case "P3", "P4", "WARNING":
		return StatusWarning
	}
	return StatusWarning
}

func pickWorse(a, b string) string {
	rank := func(s string) int {
		switch s {
		case StatusCritical:
			return 3
		case StatusWarning:
			return 2
		case StatusHealthy:
			return 1
		}
		return 0
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func nodeReadinessStatus(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return StatusHealthy
			}
			return StatusCritical
		}
	}
	return StatusWarning
}

func deploymentStatus(d *appsv1.Deployment) string {
	if d.Status.Replicas == 0 && d.Spec.Replicas != nil && *d.Spec.Replicas > 0 {
		return StatusCritical
	}
	if d.Spec.Replicas != nil && d.Status.ReadyReplicas < *d.Spec.Replicas {
		return StatusWarning
	}
	return StatusHealthy
}

func podStatus(p *corev1.Pod) string {
	switch p.Status.Phase {
	case corev1.PodFailed:
		return StatusCritical
	case corev1.PodPending:
		return StatusWarning
	case corev1.PodRunning:
		for _, c := range p.Status.ContainerStatuses {
			if !c.Ready {
				return StatusWarning
			}
			if c.RestartCount > 5 {
				return StatusWarning
			}
		}
		return StatusHealthy
	}
	return StatusWarning
}

func deploymentOwnerFromPod(p *corev1.Pod, deps []appsv1.Deployment) string {
	for _, o := range p.OwnerReferences {
		if o.Kind != "ReplicaSet" {
			continue
		}
		// ReplicaSet name is "<deployment>-<hash>"; match by prefix against
		// same-namespace deployments.
		for _, d := range deps {
			if d.Namespace != p.Namespace {
				continue
			}
			if strings.HasPrefix(o.Name, d.Name+"-") {
				return d.Name
			}
		}
	}
	return ""
}
