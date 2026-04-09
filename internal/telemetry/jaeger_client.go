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

package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// JaegerClient queries the Jaeger Query Service HTTP API (port 16686).
// Jaeger provides traces and service dependency data; it does NOT provide metrics or logs.
type JaegerClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewJaegerClient creates a Jaeger query client for the given endpoint.
// The endpoint should be the base URL without trailing slash, e.g. "http://jaeger-query:16686".
func NewJaegerClient(endpoint string, httpClient *http.Client) *JaegerClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &JaegerClient{
		endpoint:   endpoint,
		httpClient: httpClient,
	}
}

func (c *JaegerClient) FindTracesByService(ctx context.Context, service string, start, end time.Time, limit int) ([]TraceSummary, error) {
	if limit <= 0 {
		limit = 20
	}

	params := url.Values{}
	params.Set("service", service)
	params.Set("start", strconv.FormatInt(start.UnixMicro(), 10))
	params.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.doGet(ctx, "/api/traces", params)
	if err != nil {
		return nil, fmt.Errorf("jaeger FindTracesByService: %w", err)
	}

	var resp jaegerTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger FindTracesByService unmarshal: failed to parse response as JSON: %w (response: %s)", err, truncateResponseBody(body))
	}

	return resp.toTraceSummaries(), nil
}

func (c *JaegerClient) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	body, err := c.doGet(ctx, "/api/traces/"+url.PathEscape(traceID), nil)
	if err != nil {
		return nil, fmt.Errorf("jaeger GetTrace: %w", err)
	}

	var resp jaegerTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger GetTrace unmarshal: failed to parse response as JSON: %w (response: %s)", err, truncateResponseBody(body))
	}

	if len(resp.Data) == 0 {
		return nil, nil
	}

	return resp.Data[0].toTrace(), nil
}

func (c *JaegerClient) FindErrorTraces(ctx context.Context, service string, window time.Duration) ([]TraceSummary, error) {
	end := time.Now()
	start := end.Add(-window)

	params := url.Values{}
	params.Set("service", service)
	params.Set("start", strconv.FormatInt(start.UnixMicro(), 10))
	params.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
	params.Set("limit", "50")
	params.Set("tags", `{"error":"true"}`)

	body, err := c.doGet(ctx, "/api/traces", params)
	if err != nil {
		return nil, fmt.Errorf("jaeger FindErrorTraces: %w", err)
	}

	var resp jaegerTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger FindErrorTraces unmarshal: failed to parse response as JSON: %w (response: %s)", err, truncateResponseBody(body))
	}

	return resp.toTraceSummaries(), nil
}

// QueryMetric is not supported by Jaeger (traces-only backend).
func (c *JaegerClient) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]MetricSeries, error) {
	return nil, nil
}

// GetServiceMetrics is not supported by Jaeger (traces-only backend).
func (c *JaegerClient) GetServiceMetrics(_ context.Context, _ string, _ time.Duration) (*ServiceMetrics, error) {
	return nil, nil
}

// SearchLogs is not supported by Jaeger (traces-only backend).
func (c *JaegerClient) SearchLogs(_ context.Context, _ LogFilter) ([]LogEntry, error) {
	return nil, nil
}

func (c *JaegerClient) GetDependencies(ctx context.Context, window time.Duration) ([]DependencyEdge, error) {
	endTs := time.Now().UnixMilli()
	lookback := window.Milliseconds()

	params := url.Values{}
	params.Set("endTs", strconv.FormatInt(endTs, 10))
	params.Set("lookback", strconv.FormatInt(lookback, 10))

	body, err := c.doGet(ctx, "/api/dependencies", params)
	if err != nil {
		return nil, fmt.Errorf("jaeger GetDependencies: %w", err)
	}

	var resp jaegerDependenciesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jaeger GetDependencies unmarshal: failed to parse response as JSON: %w (response: %s)", err, truncateResponseBody(body))
	}

	edges := make([]DependencyEdge, 0, len(resp.Data))
	for _, d := range resp.Data {
		edges = append(edges, DependencyEdge{
			Parent:    d.Parent,
			Child:     d.Child,
			CallCount: d.CallCount,
		})
	}
	return edges, nil
}

// CorrelateByTraceID returns only the trace (Jaeger has no logs/metrics).
func (c *JaegerClient) CorrelateByTraceID(ctx context.Context, traceID string) (*CorrelatedSignals, error) {
	trace, err := c.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	return &CorrelatedSignals{
		TraceID: traceID,
		Trace:   trace,
	}, nil
}

func (c *JaegerClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.endpoint + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			// Log close errors but don't fail the operation
			_ = err
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Validate that response is JSON, not HTML error page
	// HTML responses typically start with '<' character
	if len(body) > 0 && body[0] == '<' {
		return nil, fmt.Errorf("received HTML response instead of JSON (status %d): %s", resp.StatusCode, string(body[:minInt(len(body), 200)]))
	}

	// Check Content-Type header matches JSON
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !containsSubstring(contentType, "application/json", "text/plain") {
		return nil, fmt.Errorf("unexpected Content-Type: %s (expected application/json)", contentType)
	}

	return body, nil
}

// Helper function declarations (now in utils.go)

// --- Jaeger response types ---

type jaegerTracesResponse struct {
	Data []jaegerTraceData `json:"data"`
}

type jaegerTraceData struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string          `json:"traceID"`
	SpanID        string          `json:"spanID"`
	OperationName string          `json:"operationName"`
	References    []jaegerRef     `json:"references"`
	StartTime     int64           `json:"startTime"` // microseconds
	Duration      int64           `json:"duration"`  // microseconds
	Tags          []jaegerKV      `json:"tags"`
	Logs          []jaegerSpanLog `json:"logs"`
	ProcessID     string          `json:"processID"`
	Warnings      []string        `json:"warnings"`
}

type jaegerRef struct {
	RefType string `json:"refType"` // "CHILD_OF" or "FOLLOWS_FROM"
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerKV struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type jaegerSpanLog struct {
	Timestamp int64      `json:"timestamp"`
	Fields    []jaegerKV `json:"fields"`
}

type jaegerProcess struct {
	ServiceName string     `json:"serviceName"`
	Tags        []jaegerKV `json:"tags"`
}

func (r *jaegerTracesResponse) toTraceSummaries() []TraceSummary {
	if r == nil {
		return nil
	}
	out := make([]TraceSummary, 0, len(r.Data))
	for _, td := range r.Data {
		summary := td.toTraceSummary()
		out = append(out, summary)
	}
	return out
}

func (td *jaegerTraceData) toTraceSummary() TraceSummary {
	summary := TraceSummary{
		TraceID:   td.TraceID,
		SpanCount: len(td.Spans),
	}

	// Find root span (no CHILD_OF reference)
	for _, s := range td.Spans {
		isRoot := true
		for _, ref := range s.References {
			if ref.RefType == "CHILD_OF" {
				isRoot = false
				break
			}
		}
		if isRoot {
			if proc, ok := td.Processes[s.ProcessID]; ok {
				summary.RootService = proc.ServiceName
			}
			summary.RootSpan = s.OperationName
			summary.StartTime = time.UnixMicro(s.StartTime)
			summary.Duration = float64(s.Duration) / 1000.0 // μs → ms
		}

		// Check for errors
		for _, tag := range s.Tags {
			if tag.Key == "error" {
				if v, ok := tag.Value.(bool); ok && v {
					summary.HasError = true
				}
			}
		}
	}

	return summary
}

func (td *jaegerTraceData) toTrace() *Trace {
	spans := make([]Span, 0, len(td.Spans))
	for _, s := range td.Spans {
		span := Span{
			TraceID:       s.TraceID,
			SpanID:        s.SpanID,
			OperationName: s.OperationName,
			StartTime:     time.UnixMicro(s.StartTime),
			Duration:      float64(s.Duration) / 1000.0, // μs → ms
			Tags:          kvToMap(s.Tags),
		}

		// Find parent span ID
		for _, ref := range s.References {
			if ref.RefType == "CHILD_OF" {
				span.ParentSpanID = ref.SpanID
				break
			}
		}

		// Resolve service name from process
		if proc, ok := td.Processes[s.ProcessID]; ok {
			span.ServiceName = proc.ServiceName
		}

		// Determine status
		for _, tag := range s.Tags {
			if tag.Key == "error" {
				if v, ok := tag.Value.(bool); ok && v {
					span.StatusCode = StatusError
				}
			}
			if tag.Key == "otel.status_code" {
				if v, ok := tag.Value.(string); ok && v == "ERROR" {
					span.StatusCode = StatusError
				}
			}
		}

		// Convert logs
		for _, l := range s.Logs {
			span.Logs = append(span.Logs, SpanLog{
				Timestamp: time.UnixMicro(l.Timestamp),
				Fields:    kvToMap(l.Fields),
			})
		}

		spans = append(spans, span)
	}

	return &Trace{TraceID: td.TraceID, Spans: spans}
}

func kvToMap(kvs []jaegerKV) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = fmt.Sprintf("%v", kv.Value)
	}
	return m
}

type jaegerDependenciesResponse struct {
	Data []jaegerDependency `json:"data"`
}

type jaegerDependency struct {
	Parent    string `json:"parent"`
	Child     string `json:"child"`
	CallCount int64  `json:"callCount"`
}

var _ TelemetryQuerier = (*JaegerClient)(nil)
