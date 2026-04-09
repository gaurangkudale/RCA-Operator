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

// ComputeBlastRadius performs a BFS walk from the failed service to find all
// downstream services that may be impacted. The walk follows outgoing edges
// (services that the failed service calls) and also walks upstream to find
// all services that depend on the failed service.
//
// Returns a deduplicated list of affected service names (excluding the source).
func ComputeBlastRadius(graph *ServiceGraph, failedService string) []string {
	if graph == nil {
		return nil
	}
	if _, ok := graph.Nodes[failedService]; !ok {
		return nil
	}

	visited := make(map[string]bool)
	visited[failedService] = true

	// BFS upstream: find all callers that depend on the failed service
	bfsUpstream(graph, failedService, visited)

	// BFS downstream: find all callees that the failed service calls
	bfsDownstream(graph, failedService, visited)

	// Collect results (excluding the source)
	result := make([]string, 0, len(visited)-1)
	for svc := range visited {
		if svc != failedService {
			result = append(result, svc)
		}
	}
	return result
}

// bfsUpstream walks incoming edges to find all services that depend on the failed service.
// These are the callers whose requests will fail because the failed service is down.
func bfsUpstream(graph *ServiceGraph, start string, visited map[string]bool) {
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, dep := range graph.Dependents(current) {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}
}

// bfsDownstream walks outgoing edges to find all services called by the failed service.
// These may also be affected (e.g., orphaned connections, stale state).
func bfsDownstream(graph *ServiceGraph, start string, visited map[string]bool) {
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, neighbor := range graph.Neighbors(current) {
			if !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}
}

// ComputeUpstreamBlastRadius returns only the upstream services that depend on the
// failed service (callers whose requests will fail). This is the narrower, more
// precise blast radius typically used in incident reports.
func ComputeUpstreamBlastRadius(graph *ServiceGraph, failedService string) []string {
	if graph == nil {
		return nil
	}
	if _, ok := graph.Nodes[failedService]; !ok {
		return nil
	}

	visited := make(map[string]bool)
	visited[failedService] = true

	bfsUpstream(graph, failedService, visited)

	result := make([]string, 0, len(visited)-1)
	for svc := range visited {
		if svc != failedService {
			result = append(result, svc)
		}
	}
	return result
}
