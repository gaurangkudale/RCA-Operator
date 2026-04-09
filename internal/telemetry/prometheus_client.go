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
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PrometheusClient queries the Prometheus HTTP API for metrics data.
// Prometheus is a metrics-only backend; trace and log methods return nil.
type PrometheusClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewPrometheusClient creates a Prometheus client for the given endpoint.
// The endpoint should be the base URL without trailing slash, e.g. "http://prometheus:9090".
func NewPrometheusClient(endpoint string, httpClient *http.Client) *PrometheusClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &PrometheusClient{
		endpoint:   endpoint,
		httpClient: httpClient,
	}
}

// FindTracesByService is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]TraceSummary, error) {
	return nil, nil
}

// GetTrace is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) GetTrace(_ context.Context, _ string) (*Trace, error) {
	return nil, nil
}

// FindErrorTraces is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]TraceSummary, error) {
	return nil, nil
}

func (c *PrometheusClient) QueryMetric(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]MetricSeries, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatFloat(float64(start.Unix()), 'f', 0, 64))
	params.Set("end", strconv.FormatFloat(float64(end.Unix()), 'f', 0, 64))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', 0, 64))

	body, err := c.doGet(ctx, "/api/v1/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("prometheus QueryMetric: %w", err)
	}

	var resp promQueryRangeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("prometheus QueryMetric unmarshal: %w", err)
	}

	if resp.Status != "success" {
		return nil, fmt.Errorf("prometheus QueryMetric: status=%s error=%s", resp.Status, resp.Error)
	}

	return resp.Data.toMetricSeries(), nil
}

func (c *PrometheusClient) GetServiceMetrics(ctx context.Context, service string, window time.Duration) (*ServiceMetrics, error) {
	end := time.Now()
	start := end.Add(-window)
	step := max(window/60, 15*time.Second)

	metrics := &ServiceMetrics{ServiceName: service}

	// safeVal returns v only if it is a finite, non-NaN number, otherwise 0.
	safeVal := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	}

	// Request rate
	rateQuery := fmt.Sprintf(`sum(rate(http_server_request_duration_seconds_count{service_name="%s"}[5m]))`, service)
	rateSeries, err := c.QueryMetric(ctx, rateQuery, start, end, step)
	if err == nil && len(rateSeries) > 0 && len(rateSeries[0].Datapoints) > 0 {
		metrics.RequestRate = safeVal(lastValue(rateSeries[0].Datapoints))
	}

	// Error rate
	errQuery := fmt.Sprintf(`sum(rate(http_server_request_duration_seconds_count{service_name="%s",http_response_status_code=~"5.."}[5m]))`, service)
	errSeries, err := c.QueryMetric(ctx, errQuery, start, end, step)
	if err == nil && len(errSeries) > 0 && len(errSeries[0].Datapoints) > 0 {
		metrics.ErrorRate = safeVal(lastValue(errSeries[0].Datapoints))
	}

	// P99 latency — histogram_quantile returns NaN when no observations exist
	latQuery := fmt.Sprintf(`histogram_quantile(0.99, sum(rate(http_server_request_duration_seconds_bucket{service_name="%s"}[5m])) by (le))`, service)
	latSeries, err := c.QueryMetric(ctx, latQuery, start, end, step)
	if err == nil && len(latSeries) > 0 && len(latSeries[0].Datapoints) > 0 {
		if v := safeVal(lastValue(latSeries[0].Datapoints)); v > 0 {
			metrics.P99Latency = v * 1000 // s → ms
		}
	}

	// CPU usage
	cpuQuery := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{pod=~"%s.*"}[5m]))`, service)
	cpuSeries, err := c.QueryMetric(ctx, cpuQuery, start, end, step)
	if err == nil && len(cpuSeries) > 0 && len(cpuSeries[0].Datapoints) > 0 {
		metrics.CPUUsage = safeVal(lastValue(cpuSeries[0].Datapoints))
	}

	// Memory usage
	memQuery := fmt.Sprintf(`sum(container_memory_working_set_bytes{pod=~"%s.*"})`, service)
	memSeries, err := c.QueryMetric(ctx, memQuery, start, end, step)
	if err == nil && len(memSeries) > 0 && len(memSeries[0].Datapoints) > 0 {
		metrics.MemoryUsage = safeVal(lastValue(memSeries[0].Datapoints))
	}

	// Active connections (OTel HTTP semantic conventions: http.server.active_requests)
	connQuery := fmt.Sprintf(`sum(http_server_active_requests{service_name="%s"})`, service)
	connSeries, err := c.QueryMetric(ctx, connQuery, start, end, step)
	if err == nil && len(connSeries) > 0 && len(connSeries[0].Datapoints) > 0 {
		metrics.ActiveConnections = safeVal(lastValue(connSeries[0].Datapoints))
	}

	return metrics, nil
}

// SearchLogs is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) SearchLogs(_ context.Context, _ LogFilter) ([]LogEntry, error) {
	return nil, nil
}

// GetDependencies is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) GetDependencies(_ context.Context, _ time.Duration) ([]DependencyEdge, error) {
	return nil, nil
}

// CorrelateByTraceID is not supported by Prometheus (metrics-only backend).
func (c *PrometheusClient) CorrelateByTraceID(_ context.Context, _ string) (*CorrelatedSignals, error) {
	return nil, nil
}

func (c *PrometheusClient) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
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

func lastValue(dps []Datapoint) float64 {
	if len(dps) == 0 {
		return 0
	}
	return dps[len(dps)-1].Value
}

// --- Prometheus response types ---

type promQueryRangeResponse struct {
	Status string         `json:"status"`
	Error  string         `json:"error,omitempty"`
	Data   promResultData `json:"data"`
}

type promResultData struct {
	ResultType string             `json:"resultType"`
	Result     []promMatrixResult `json:"result"`
}

type promMatrixResult struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

func (d *promResultData) toMetricSeries() []MetricSeries {
	out := make([]MetricSeries, 0, len(d.Result))
	for _, r := range d.Result {
		series := MetricSeries{
			Labels:     r.Metric,
			Datapoints: make([]Datapoint, 0, len(r.Values)),
		}
		for _, v := range r.Values {
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
			if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
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

var _ TelemetryQuerier = (*PrometheusClient)(nil)
