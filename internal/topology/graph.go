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
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

// ServiceGraph represents the complete service dependency DAG.
type ServiceGraph struct {
	Nodes map[string]*ServiceNode `json:"nodes"`
	Edges []ServiceEdge           `json:"edges"`
}

// ServiceNode represents a single service in the topology graph.
type ServiceNode struct {
	Name      string                    `json:"name"`
	Namespace string                    `json:"namespace,omitempty"`
	Kind      string                    `json:"kind,omitempty"` // Deployment, StatefulSet, DaemonSet, etc.
	Status    telemetry.HealthStatus    `json:"status"`
	Metrics   *telemetry.ServiceMetrics `json:"metrics,omitempty"`
	Incidents []IncidentRef             `json:"incidents,omitempty"`
	Icon      string                    `json:"icon,omitempty"` // UI icon hint (server, database, globe, etc.)
}

// IncidentRef is a lightweight reference to an active incident on a service.
type IncidentRef struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Severity     string `json:"severity"`
	Phase        string `json:"phase"`
	IncidentType string `json:"incidentType"`
	Summary      string `json:"summary,omitempty"`
}

// ServiceEdge represents a directed dependency between two services.
type ServiceEdge struct {
	Source     string               `json:"source"`
	Target     string               `json:"target"`
	Status     telemetry.EdgeStatus `json:"status"`
	CallCount  int64                `json:"callCount,omitempty"`
	ErrorRate  float64              `json:"errorRate,omitempty"`
	AvgLatency float64              `json:"avgLatency,omitempty"` // ms
}

// NewServiceGraph creates an empty service graph.
func NewServiceGraph() *ServiceGraph {
	return &ServiceGraph{
		Nodes: make(map[string]*ServiceNode),
	}
}

// AddNode adds or updates a node in the graph.
func (g *ServiceGraph) AddNode(node *ServiceNode) {
	g.Nodes[node.Name] = node
}

// AddEdge adds an edge to the graph, creating nodes if they don't exist.
func (g *ServiceGraph) AddEdge(edge ServiceEdge) {
	if _, ok := g.Nodes[edge.Source]; !ok {
		g.Nodes[edge.Source] = &ServiceNode{
			Name:   edge.Source,
			Status: telemetry.HealthStatusUnknown,
		}
	}
	if _, ok := g.Nodes[edge.Target]; !ok {
		g.Nodes[edge.Target] = &ServiceNode{
			Name:   edge.Target,
			Status: telemetry.HealthStatusUnknown,
		}
	}
	g.Edges = append(g.Edges, edge)
}

// Neighbors returns the names of services that the given service calls (outgoing edges).
func (g *ServiceGraph) Neighbors(service string) []string {
	var neighbors []string
	for _, e := range g.Edges {
		if e.Source == service {
			neighbors = append(neighbors, e.Target)
		}
	}
	return neighbors
}

// Dependents returns the names of services that call the given service (incoming edges).
func (g *ServiceGraph) Dependents(service string) []string {
	var dependents []string
	for _, e := range g.Edges {
		if e.Target == service {
			dependents = append(dependents, e.Source)
		}
	}
	return dependents
}

// ServiceNames returns all service names in the graph.
func (g *ServiceGraph) ServiceNames() []string {
	names := make([]string, 0, len(g.Nodes))
	for name := range g.Nodes {
		names = append(names, name)
	}
	return names
}
