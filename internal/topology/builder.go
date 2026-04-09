/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topology

import (
	"context"
	"time"

	"github.com/go-logr/logr"

	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

// Builder constructs a ServiceGraph from telemetry dependency data and incident state.
type Builder struct {
	querier telemetry.TelemetryQuerier
	log     logr.Logger
}

// NewBuilder creates a topology builder that uses the given telemetry querier.
func NewBuilder(querier telemetry.TelemetryQuerier, log logr.Logger) *Builder {
	return &Builder{
		querier: querier,
		log:     log,
	}
}

// BuildGraph constructs a ServiceGraph from OTel span dependencies and enriches it
// with per-service metrics and incident state.
func (b *Builder) BuildGraph(ctx context.Context, window time.Duration, incidents []IncidentRef) (*ServiceGraph, error) {
	graph := NewServiceGraph()

	// Step 1: Get dependency edges from the telemetry backend (OTel span parent-child relationships)
	deps, err := b.querier.GetDependencies(ctx, window)
	if err != nil {
		b.log.Error(err, "failed to get dependencies from telemetry backend")
		// Return empty graph rather than failing -- operator should still work without topology
		return graph, nil
	}

	// Step 2: Build the graph skeleton from dependency edges
	for _, dep := range deps {
		edgeStatus := classifyEdgeStatus(dep)
		graph.AddEdge(ServiceEdge{
			Source:     dep.Parent,
			Target:     dep.Child,
			Status:     edgeStatus,
			CallCount:  dep.CallCount,
			ErrorRate:  dep.ErrorRate,
			AvgLatency: dep.AvgLatency,
		})
	}

	// Step 3: Enrich nodes with service metrics
	for name, node := range graph.Nodes {
		metrics, err := b.querier.GetServiceMetrics(ctx, name, window)
		if err != nil {
			b.log.V(1).Info("failed to get service metrics", "service", name, "error", err)
			continue
		}
		if metrics != nil {
			node.Metrics = metrics
		}
	}

	// Step 4: Overlay incident data onto nodes
	b.overlayIncidents(graph, incidents)

	// Step 5: Promote node status based on edge error rates (even without a K8s-level incident)
	upgradeNodeStatusFromEdges(graph)

	// Step 6: Infer node icons from service names
	for _, node := range graph.Nodes {
		if node.Icon == "" {
			node.Icon = inferIcon(node)
		}
	}

	return graph, nil
}

// overlayIncidents maps active incidents to their corresponding service nodes
// and sets node health status based on incident severity.
func (b *Builder) overlayIncidents(graph *ServiceGraph, incidents []IncidentRef) {
	// Group incidents by service name (approximate match via incident name/namespace)
	for _, inc := range incidents {
		for _, node := range graph.Nodes {
			if matchesService(node, inc) {
				node.Incidents = append(node.Incidents, inc)
			}
		}
	}

	// Set node status based on incidents
	for _, node := range graph.Nodes {
		node.Status = computeNodeStatus(node)
	}
}

// computeNodeStatus determines the health status of a node based on its incidents.
func computeNodeStatus(node *ServiceNode) telemetry.HealthStatus {
	if len(node.Incidents) == 0 {
		return telemetry.HealthStatusHealthy
	}

	for _, inc := range node.Incidents {
		if inc.Phase == "Active" && (inc.Severity == "P1" || inc.Severity == "P2") {
			return telemetry.HealthStatusCritical
		}
	}

	for _, inc := range node.Incidents {
		if inc.Phase == "Active" || inc.Phase == "Detecting" {
			return telemetry.HealthStatusWarning
		}
	}

	return telemetry.HealthStatusHealthy
}

// matchesService returns true if the incident likely affects this service node.
//
// Matching strategy (in priority order):
//  1. Namespace guard: if both sides have a namespace set and they differ → no match.
//     NOTE: topology nodes built from Jaeger/SigNoz do not carry a namespace, so this
//     guard only fires when the node was explicitly created with a namespace.
//  2. Exact name match (most precise).
//  3. Node name is a prefix of the incident name — handles the common case where the
//     Jaeger service is "payment" and the incident is named "payment-crash-abc123".
//  4. Incident name is a prefix of the node name — handles the inverse (service registered
//     as "payment-svc" matching an incident on pod "payment").
func matchesService(node *ServiceNode, inc IncidentRef) bool {
	// Namespace guard: only applies when both sides carry a non-empty namespace.
	if inc.Namespace != "" && node.Namespace != "" && inc.Namespace != node.Namespace {
		return false
	}

	nodeName := node.Name
	incName := inc.Name

	// Exact match.
	if nodeName == incName {
		return true
	}

	// Node name is a prefix of the incident name: "payment" matches "payment-crash-abc"
	if len(incName) > len(nodeName) && incName[:len(nodeName)] == nodeName && incName[len(nodeName)] == '-' {
		return true
	}

	// Incident name is a prefix of the node name: "payment" matches "payment-svc"
	if len(nodeName) > len(incName) && nodeName[:len(incName)] == incName && nodeName[len(incName)] == '-' {
		return true
	}

	return false
}

// upgradeNodeStatusFromEdges promotes node health status based on edge error rates.
// When service-to-service calls are failing, the affected nodes should reflect that
// degradation even without a corresponding Kubernetes-level incident.
//
// For critical edges (>10% error rate):
//   - target node is upgraded to at least critical (it is likely the failing service)
//   - source node is upgraded to at least warning (its outbound calls are failing)
//
// For warning edges (>1% error or >1s latency):
//   - both source and target are upgraded to at least warning
//
// Upgrades are monotonic: a node already at critical is never downgraded.
func upgradeNodeStatusFromEdges(graph *ServiceGraph) {
	for _, edge := range graph.Edges {
		src := graph.Nodes[edge.Source]
		tgt := graph.Nodes[edge.Target]
		if src == nil || tgt == nil {
			continue
		}
		switch edge.Status {
		case telemetry.EdgeStatusCritical:
			if edgeStatusPriority(tgt.Status) < edgeStatusPriority(telemetry.HealthStatusCritical) {
				tgt.Status = telemetry.HealthStatusCritical
			}
			if edgeStatusPriority(src.Status) < edgeStatusPriority(telemetry.HealthStatusWarning) {
				src.Status = telemetry.HealthStatusWarning
			}
		case telemetry.EdgeStatusWarning:
			if edgeStatusPriority(src.Status) < edgeStatusPriority(telemetry.HealthStatusWarning) {
				src.Status = telemetry.HealthStatusWarning
			}
			if edgeStatusPriority(tgt.Status) < edgeStatusPriority(telemetry.HealthStatusWarning) {
				tgt.Status = telemetry.HealthStatusWarning
			}
		}
	}
}

// edgeStatusPriority returns a numeric priority for monotonic health status upgrades.
func edgeStatusPriority(s telemetry.HealthStatus) int {
	switch s {
	case telemetry.HealthStatusCritical:
		return 3
	case telemetry.HealthStatusWarning:
		return 2
	case telemetry.HealthStatusHealthy:
		return 1
	default: // unknown
		return 0
	}
}

// classifyEdgeStatus determines edge health from dependency metrics.
func classifyEdgeStatus(dep telemetry.DependencyEdge) telemetry.EdgeStatus {
	if dep.ErrorRate > 0.1 { // >10% error rate
		return telemetry.EdgeStatusCritical
	}
	if dep.ErrorRate > 0.01 || dep.AvgLatency > 1000 { // >1% errors or >1s latency
		return telemetry.EdgeStatusWarning
	}
	return telemetry.EdgeStatusActive
}

// inferIcon guesses a UI icon based on the service name.
func inferIcon(node *ServiceNode) string {
	name := node.Name
	switch {
	case contains(name, "ingress", "gateway", "nginx", "envoy", "haproxy"):
		return "globe"
	case contains(name, "postgres", "mysql", "mongo", "redis", "memcache", "elastic", "clickhouse", "cassandra", "db"):
		return "database"
	case contains(name, "kafka", "rabbit", "nats", "pulsar", "queue", "mq"):
		return "mail"
	case contains(name, "auth", "identity", "iam", "oauth", "sso"):
		return "shield-check"
	case contains(name, "payment", "billing", "checkout", "stripe"):
		return "credit-card"
	case contains(name, "frontend", "ui", "web", "app"):
		return "monitor"
	default:
		return "server"
	}
}

func contains(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
