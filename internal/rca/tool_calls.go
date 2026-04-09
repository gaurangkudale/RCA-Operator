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

package rca

import "encoding/json"

// ToolDef defines a tool the LLM can invoke (OpenAI function calling format).
type ToolDef struct {
	Type     string          `json:"type"`
	Function ToolFunctionDef `json:"function"`
}

// ToolFunctionDef holds the function metadata for a tool.
type ToolFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// InvestigationTools returns the tool definitions available during AI investigation.
// These are read-only queries against the telemetry backend.
func InvestigationTools() []ToolDef {
	return []ToolDef{
		{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "query_metrics",
				Description: "Execute a PromQL query against Prometheus to retrieve time-series metric data for a service or pod.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {
							"type": "string",
							"description": "PromQL query expression (e.g. rate(http_requests_total{service=\"payment-svc\"}[5m]))"
						},
						"start": {
							"type": "string",
							"description": "Start time in RFC3339 format"
						},
						"end": {
							"type": "string",
							"description": "End time in RFC3339 format"
						}
					},
					"required": ["query"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "search_logs",
				Description: "Search application logs by service name, severity level, or keyword. Returns matching log entries from the observability backend.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"service": {
							"type": "string",
							"description": "Service name to filter logs by"
						},
						"severity": {
							"type": "string",
							"description": "Minimum severity: TRACE, DEBUG, INFO, WARN, ERROR, FATAL"
						},
						"keyword": {
							"type": "string",
							"description": "Keyword to search for in log messages"
						},
						"window": {
							"type": "string",
							"description": "Lookback window (e.g. 15m, 1h). Default: 15m"
						}
					},
					"required": ["service"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "get_trace",
				Description: "Fetch the full distributed trace by trace ID, including all spans with their durations, status codes, and tags.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"trace_id": {
							"type": "string",
							"description": "W3C trace ID to look up"
						}
					},
					"required": ["trace_id"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "get_service_metrics",
				Description: "Get aggregated RED metrics (Request rate, Error rate, Duration/latency) plus CPU and memory usage for a service.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"service": {
							"type": "string",
							"description": "Service name"
						},
						"window": {
							"type": "string",
							"description": "Lookback window (e.g. 15m, 1h). Default: 15m"
						}
					},
					"required": ["service"]
				}`),
			},
		},
		{
			Type: "function",
			Function: ToolFunctionDef{
				Name:        "get_dependencies",
				Description: "Get the service dependency graph showing which services call which other services, with call counts and error rates.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"service": {
							"type": "string",
							"description": "Optional: filter to show only dependencies of this service"
						}
					}
				}`),
			},
		},
	}
}

// QueryMetricsArgs holds parsed arguments for the query_metrics tool.
type QueryMetricsArgs struct {
	Query string `json:"query"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// SearchLogsArgs holds parsed arguments for the search_logs tool.
type SearchLogsArgs struct {
	Service  string `json:"service"`
	Severity string `json:"severity"`
	Keyword  string `json:"keyword"`
	Window   string `json:"window"`
}

// GetTraceArgs holds parsed arguments for the get_trace tool.
type GetTraceArgs struct {
	TraceID string `json:"trace_id"`
}

// GetServiceMetricsArgs holds parsed arguments for the get_service_metrics tool.
type GetServiceMetricsArgs struct {
	Service string `json:"service"`
	Window  string `json:"window"`
}

// GetDependenciesArgs holds parsed arguments for the get_dependencies tool.
type GetDependenciesArgs struct {
	Service string `json:"service"`
}
