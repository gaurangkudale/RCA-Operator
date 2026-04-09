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

// SigNozClient queries the SigNoz Query Service REST API.
// SigNoz provides a unified backend for traces, logs, and metrics via ClickHouse.
type SigNozClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewSigNozClient creates a SigNoz client for the given query service endpoint.
// The endpoint should be the base URL without trailing slash, e.g. "http://signoz-query-service:8080".
func NewSigNozClient(endpoint string, httpClient *http.Client) *SigNozClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &SigNozClient{
		endpoint:   endpoint,
		httpClient: httpClient,
	}
}

func (c *SigNozClient) FindTracesByService(ctx context.Context, service string, start, end time.Time, limit int) ([]TraceSummary, error) {
	if limit <= 0 {
		limit = 20
	}

	params := url.Values{}
	params.Set("service", service)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.doGet(ctx, "/api/v3/traces", params)
	if err != nil {
		return nil, fmt.Errorf("signoz FindTracesByService: %w", err)
	}

	var resp signozTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signoz FindTracesByService unmarshal: %w", err)
	}

	return resp.toTraceSummaries(), nil
}

func (c *SigNozClient) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	body, err := c.doGet(ctx, "/api/v3/traces/"+url.PathEscape(traceID), nil)
	if err != nil {
		return nil, fmt.Errorf("signoz GetTrace: %w", err)
	}

	var resp signozTraceDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signoz GetTrace unmarshal: %w", err)
	}

	return resp.toTrace(traceID), nil
}

func (c *SigNozClient) FindErrorTraces(ctx context.Context, service string, window time.Duration) ([]TraceSummary, error) {
	end := time.Now()
	start := end.Add(-window)

	params := url.Values{}
	params.Set("service", service)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("limit", "50")
	params.Set("tags", `{"error":"true"}`)

	body, err := c.doGet(ctx, "/api/v3/traces", params)
	if err != nil {
		return nil, fmt.Errorf("signoz FindErrorTraces: %w", err)
	}

	var resp signozTracesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signoz FindErrorTraces unmarshal: %w", err)
	}

	return resp.toTraceSummaries(), nil
}

func (c *SigNozClient) QueryMetric(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]MetricSeries, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))

	body, err := c.doGet(ctx, "/api/v3/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("signoz QueryMetric: %w", err)
	}

	var resp signozMetricResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signoz QueryMetric unmarshal: %w", err)
	}

	return resp.toMetricSeries(), nil
}

func (c *SigNozClient) GetServiceMetrics(ctx context.Context, service string, window time.Duration) (*ServiceMetrics, error) {
	end := time.Now()
	start := end.Add(-window)

	params := url.Values{}
	params.Set("service", service)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))

	body, err := c.doGet(ctx, "/api/v1/services", params)
	if err != nil {
		return nil, fmt.Errorf("signoz GetServiceMetrics: %w", err)
	}

	var services []signozServiceOverview
	if err := json.Unmarshal(body, &services); err != nil {
		return nil, fmt.Errorf("signoz GetServiceMetrics unmarshal: %w", err)
	}

	for _, svc := range services {
		if svc.ServiceName == service {
			return &ServiceMetrics{
				ServiceName: svc.ServiceName,
				RequestRate: svc.CallRate,
				ErrorRate:   svc.ErrorRate,
				P50Latency:  svc.P50,
				P95Latency:  svc.P95,
				P99Latency:  svc.P99,
			}, nil
		}
	}

	return &ServiceMetrics{ServiceName: service}, nil
}

func (c *SigNozClient) SearchLogs(ctx context.Context, filter LogFilter) ([]LogEntry, error) {
	params := url.Values{}
	if filter.ServiceName != "" {
		params.Set("serviceName", filter.ServiceName)
	}
	if filter.Severity != "" {
		params.Set("severity", filter.Severity)
	}
	if filter.Keyword != "" {
		params.Set("q", filter.Keyword)
	}
	if filter.TraceID != "" {
		params.Set("traceID", filter.TraceID)
	}
	params.Set("start", strconv.FormatInt(filter.Start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(filter.End.UnixNano(), 10))
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.doGet(ctx, "/api/v3/logs", params)
	if err != nil {
		return nil, fmt.Errorf("signoz SearchLogs: %w", err)
	}

	var resp signozLogsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signoz SearchLogs unmarshal: %w", err)
	}

	return resp.toLogEntries(), nil
}

func (c *SigNozClient) GetDependencies(ctx context.Context, window time.Duration) ([]DependencyEdge, error) {
	params := url.Values{}
	params.Set("start", strconv.FormatInt(time.Now().Add(-window).UnixNano(), 10))
	params.Set("end", strconv.FormatInt(time.Now().UnixNano(), 10))

	body, err := c.doGet(ctx, "/api/v1/services/dependencies", params)
	if err != nil {
		return nil, fmt.Errorf("signoz GetDependencies: %w", err)
	}

	var deps []signozDependency
	if err := json.Unmarshal(body, &deps); err != nil {
		return nil, fmt.Errorf("signoz GetDependencies unmarshal: %w", err)
	}

	edges := make([]DependencyEdge, 0, len(deps))
	for _, d := range deps {
		edges = append(edges, DependencyEdge{
			Parent:    d.Parent,
			Child:     d.Child,
			CallCount: d.CallCount,
		})
	}
	return edges, nil
}

func (c *SigNozClient) CorrelateByTraceID(ctx context.Context, traceID string) (*CorrelatedSignals, error) {
	trace, err := c.GetTrace(ctx, traceID)
	if err != nil {
		return nil, fmt.Errorf("signoz CorrelateByTraceID trace: %w", err)
	}

	logs, err := c.SearchLogs(ctx, LogFilter{
		TraceID: traceID,
		Start:   time.Now().Add(-1 * time.Hour),
		End:     time.Now(),
		Limit:   100,
	})
	if err != nil {
		return nil, fmt.Errorf("signoz CorrelateByTraceID logs: %w", err)
	}

	return &CorrelatedSignals{
		TraceID: traceID,
		Trace:   trace,
		Logs:    logs,
	}, nil
}

// doGet performs a GET request against the SigNoz query service.
func (c *SigNozClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
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

	return body, nil
}

// --- SigNoz response types ---

type signozTracesResponse struct {
	Traces []signozTraceSummary `json:"traces"`
}

type signozTraceSummary struct {
	TraceID         string `json:"traceID"`
	RootServiceName string `json:"rootServiceName"`
	RootSpanName    string `json:"rootSpanName"`
	StartTimeUnix   int64  `json:"startTimeUnixNano"`
	DurationNano    int64  `json:"durationNano"`
	SpanCount       int    `json:"numSpans"`
	HasError        bool   `json:"hasError"`
}

func (r *signozTracesResponse) toTraceSummaries() []TraceSummary {
	if r == nil {
		return nil
	}
	out := make([]TraceSummary, 0, len(r.Traces))
	for _, t := range r.Traces {
		out = append(out, TraceSummary{
			TraceID:     t.TraceID,
			RootService: t.RootServiceName,
			RootSpan:    t.RootSpanName,
			StartTime:   time.Unix(0, t.StartTimeUnix),
			Duration:    float64(t.DurationNano) / 1e6, // ns → ms
			SpanCount:   t.SpanCount,
			HasError:    t.HasError,
		})
	}
	return out
}

type signozTraceDetailResponse struct {
	Spans []signozSpan `json:"spans"`
}

type signozSpan struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	ParentSpanID  string            `json:"parentSpanID"`
	OperationName string            `json:"operationName"`
	ServiceName   string            `json:"serviceName"`
	StartTimeUnix int64             `json:"startTimeUnixNano"`
	DurationNano  int64             `json:"durationNano"`
	StatusCode    int               `json:"statusCode"`
	StatusMessage string            `json:"statusMessage"`
	Tags          map[string]string `json:"tagMap"`
}

func (r *signozTraceDetailResponse) toTrace(traceID string) *Trace {
	if r == nil {
		return nil
	}
	spans := make([]Span, 0, len(r.Spans))
	for _, s := range r.Spans {
		spans = append(spans, Span{
			TraceID:       s.TraceID,
			SpanID:        s.SpanID,
			ParentSpanID:  s.ParentSpanID,
			OperationName: s.OperationName,
			ServiceName:   s.ServiceName,
			StartTime:     time.Unix(0, s.StartTimeUnix),
			Duration:      float64(s.DurationNano) / 1e6, // ns → ms
			StatusCode:    SpanStatusCode(s.StatusCode),
			StatusMessage: s.StatusMessage,
			Tags:          s.Tags,
		})
	}
	return &Trace{TraceID: traceID, Spans: spans}
}

type signozMetricResponse struct {
	Status string           `json:"status"`
	Data   signozMetricData `json:"data"`
}

type signozMetricData struct {
	ResultType string               `json:"resultType"`
	Result     []signozMetricResult `json:"result"`
}

type signozMetricResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

func (r *signozMetricResponse) toMetricSeries() []MetricSeries {
	if r == nil {
		return nil
	}
	out := make([]MetricSeries, 0, len(r.Data.Result))
	for _, result := range r.Data.Result {
		series := MetricSeries{
			Labels:     result.Metric,
			Datapoints: make([]Datapoint, 0, len(result.Values)),
		}
		for _, v := range result.Values {
			if len(v) < 2 {
				continue
			}
			ts, ok := v[0].(float64)
			if !ok {
				continue
			}
			valStr, ok := v[1].(string)
			if !ok {
				continue
			}
			val, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				continue
			}
			series.Datapoints = append(series.Datapoints, Datapoint{
				Timestamp: time.Unix(int64(ts), 0),
				Value:     val,
			})
		}
		out = append(out, series)
	}
	return out
}

type signozLogsResponse struct {
	Logs []signozLogEntry `json:"logs"`
}

type signozLogEntry struct {
	Timestamp   int64             `json:"timestamp"`
	Body        string            `json:"body"`
	Severity    string            `json:"severityText"`
	ServiceName string            `json:"serviceName"`
	TraceID     string            `json:"traceID"`
	SpanID      string            `json:"spanID"`
	Attributes  map[string]string `json:"attributes_string"`
}

func (r *signozLogsResponse) toLogEntries() []LogEntry {
	if r == nil {
		return nil
	}
	out := make([]LogEntry, 0, len(r.Logs))
	for _, l := range r.Logs {
		out = append(out, LogEntry{
			Timestamp:   time.Unix(0, l.Timestamp),
			Body:        l.Body,
			Severity:    l.Severity,
			ServiceName: l.ServiceName,
			TraceID:     l.TraceID,
			SpanID:      l.SpanID,
			Attributes:  l.Attributes,
		})
	}
	return out
}

type signozDependency struct {
	Parent    string `json:"parent"`
	Child     string `json:"child"`
	CallCount int64  `json:"callCount"`
}

type signozServiceOverview struct {
	ServiceName string  `json:"serviceName"`
	P50         float64 `json:"p50"`
	P95         float64 `json:"p95"`
	P99         float64 `json:"p99"`
	CallRate    float64 `json:"callRate"`
	NumCalls    int64   `json:"numCalls"`
	NumErrors   int64   `json:"numErrors"`
	ErrorRate   float64 `json:"errorRate"`
}

var _ TelemetryQuerier = (*SigNozClient)(nil)
