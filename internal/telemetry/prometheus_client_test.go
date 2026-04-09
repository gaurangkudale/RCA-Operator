package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrometheusClient_QueryMetric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "up" {
			t.Errorf("unexpected query: %s", r.URL.Query().Get("query"))
		}

		resp := promQueryRangeResponse{
			Status: "success",
			Data: promResultData{
				ResultType: "matrix",
				Result: []promMatrixResult{
					{
						Metric: map[string]string{"instance": "node-1"},
						Values: [][]any{
							{float64(1700000000), "1"},
							{float64(1700000060), "1"},
							{float64(1700000120), "0"},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewPrometheusClient(server.URL, nil)
	series, err := client.QueryMetric(context.Background(), "up", time.Now().Add(-5*time.Minute), time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(series))
	}
	if series[0].Labels["instance"] != "node-1" {
		t.Errorf("expected label instance=node-1, got %s", series[0].Labels["instance"])
	}
	if len(series[0].Datapoints) != 3 {
		t.Fatalf("expected 3 datapoints, got %d", len(series[0].Datapoints))
	}
	if series[0].Datapoints[0].Value != 1.0 {
		t.Errorf("expected first value 1.0, got %f", series[0].Datapoints[0].Value)
	}
	if series[0].Datapoints[2].Value != 0.0 {
		t.Errorf("expected third value 0.0, got %f", series[0].Datapoints[2].Value)
	}
}

func TestPrometheusClient_QueryMetric_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := promQueryRangeResponse{
			Status: "error",
			Error:  "invalid query syntax",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewPrometheusClient(server.URL, nil)
	_, err := client.QueryMetric(context.Background(), "bad{query", time.Now().Add(-5*time.Minute), time.Now(), time.Minute)
	if err == nil {
		t.Fatal("expected error for bad query")
	}
}

func TestPrometheusClient_UnsupportedMethods(t *testing.T) {
	client := NewPrometheusClient("http://unused", nil)

	traces, err := client.FindTracesByService(context.Background(), "test", time.Now(), time.Now(), 10)
	if err != nil || traces != nil {
		t.Error("FindTracesByService should return nil, nil for Prometheus")
	}

	trace, err := client.GetTrace(context.Background(), "test")
	if err != nil || trace != nil {
		t.Error("GetTrace should return nil, nil for Prometheus")
	}

	errTraces, err := client.FindErrorTraces(context.Background(), "test", time.Minute)
	if err != nil || errTraces != nil {
		t.Error("FindErrorTraces should return nil, nil for Prometheus")
	}

	logs, err := client.SearchLogs(context.Background(), LogFilter{})
	if err != nil || logs != nil {
		t.Error("SearchLogs should return nil, nil for Prometheus")
	}

	deps, err := client.GetDependencies(context.Background(), time.Minute)
	if err != nil || deps != nil {
		t.Error("GetDependencies should return nil, nil for Prometheus")
	}

	corr, err := client.CorrelateByTraceID(context.Background(), "test")
	if err != nil || corr != nil {
		t.Error("CorrelateByTraceID should return nil, nil for Prometheus")
	}
}

func TestPrometheusClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte("internal error")); err != nil {
			t.Logf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewPrometheusClient(server.URL, nil)
	_, err := client.QueryMetric(context.Background(), "up", time.Now().Add(-5*time.Minute), time.Now(), time.Minute)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestLastValue(t *testing.T) {
	tests := []struct {
		name     string
		dps      []Datapoint
		expected float64
	}{
		{"empty", nil, 0},
		{"single", []Datapoint{{Value: 42.5}}, 42.5},
		{"multiple", []Datapoint{{Value: 1}, {Value: 2}, {Value: 3.14}}, 3.14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastValue(tt.dps)
			if got != tt.expected {
				t.Errorf("lastValue() = %f, want %f", got, tt.expected)
			}
		})
	}
}
