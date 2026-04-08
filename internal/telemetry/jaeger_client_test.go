package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJaegerClient_FindTracesByService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traces" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("service") != "auth-svc" {
			t.Errorf("unexpected service: %s", r.URL.Query().Get("service"))
		}

		resp := jaegerTracesResponse{
			Data: []jaegerTraceData{
				{
					TraceID: "aaa111",
					Spans: []jaegerSpan{
						{
							TraceID:       "aaa111",
							SpanID:        "s1",
							OperationName: "GET /login",
							StartTime:     time.Now().UnixMicro(),
							Duration:      250000, // 250ms
							ProcessID:     "p1",
							Tags: []jaegerKV{
								{Key: "http.status_code", Type: "int64", Value: float64(200)},
							},
						},
						{
							TraceID:       "aaa111",
							SpanID:        "s2",
							OperationName: "SELECT user",
							StartTime:     time.Now().UnixMicro(),
							Duration:      50000, // 50ms
							ProcessID:     "p2",
							References: []jaegerRef{
								{RefType: "CHILD_OF", TraceID: "aaa111", SpanID: "s1"},
							},
							Tags: []jaegerKV{
								{Key: "error", Type: "bool", Value: true},
							},
						},
					},
					Processes: map[string]jaegerProcess{
						"p1": {ServiceName: "auth-svc"},
						"p2": {ServiceName: "user-db"},
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

	client := NewJaegerClient(server.URL, nil)
	traces, err := client.FindTracesByService(context.Background(), "auth-svc", time.Now().Add(-1*time.Hour), time.Now(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0].TraceID != "aaa111" {
		t.Errorf("expected traceID aaa111, got %s", traces[0].TraceID)
	}
	if traces[0].RootService != "auth-svc" {
		t.Errorf("expected root service auth-svc, got %s", traces[0].RootService)
	}
	if traces[0].RootSpan != "GET /login" {
		t.Errorf("expected root span GET /login, got %s", traces[0].RootSpan)
	}
	if !traces[0].HasError {
		t.Error("expected HasError=true due to error tag on child span")
	}
	if traces[0].SpanCount != 2 {
		t.Errorf("expected 2 spans, got %d", traces[0].SpanCount)
	}
}

func TestJaegerClient_GetTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traces/aaa111" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := jaegerTracesResponse{
			Data: []jaegerTraceData{
				{
					TraceID: "aaa111",
					Spans: []jaegerSpan{
						{
							TraceID:       "aaa111",
							SpanID:        "s1",
							OperationName: "GET /login",
							StartTime:     time.Now().UnixMicro(),
							Duration:      250000,
							ProcessID:     "p1",
						},
						{
							TraceID:       "aaa111",
							SpanID:        "s2",
							OperationName: "SELECT user",
							StartTime:     time.Now().UnixMicro(),
							Duration:      50000,
							ProcessID:     "p2",
							References: []jaegerRef{
								{RefType: "CHILD_OF", TraceID: "aaa111", SpanID: "s1"},
							},
							Tags: []jaegerKV{
								{Key: "otel.status_code", Type: "string", Value: "ERROR"},
							},
							Logs: []jaegerSpanLog{
								{
									Timestamp: time.Now().UnixMicro(),
									Fields: []jaegerKV{
										{Key: "message", Type: "string", Value: "connection refused"},
									},
								},
							},
						},
					},
					Processes: map[string]jaegerProcess{
						"p1": {ServiceName: "auth-svc"},
						"p2": {ServiceName: "user-db"},
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

	client := NewJaegerClient(server.URL, nil)
	trace, err := client.GetTrace(context.Background(), "aaa111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trace == nil {
		t.Fatal("expected trace, got nil")
		return
	}
	if len(trace.Spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(trace.Spans))
	}

	// Check parent span
	if trace.Spans[0].ServiceName != "auth-svc" {
		t.Errorf("expected service auth-svc, got %s", trace.Spans[0].ServiceName)
	}

	// Check child span with error
	child := trace.Spans[1]
	if child.ParentSpanID != "s1" {
		t.Errorf("expected parent span s1, got %s", child.ParentSpanID)
	}
	if child.StatusCode != StatusError {
		t.Errorf("expected StatusError, got %d", child.StatusCode)
	}
	if len(child.Logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(child.Logs))
	}
	if child.Logs[0].Fields["message"] != "connection refused" {
		t.Errorf("expected log message 'connection refused', got %s", child.Logs[0].Fields["message"])
	}
}

func TestJaegerClient_GetTrace_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := jaegerTracesResponse{Data: []jaegerTraceData{}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewJaegerClient(server.URL, nil)
	trace, err := client.GetTrace(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trace != nil {
		t.Errorf("expected nil trace for nonexistent ID, got %v", trace)
	}
}

func TestJaegerClient_GetDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dependencies" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("endTs") == "" {
			t.Error("expected endTs parameter")
		}
		if r.URL.Query().Get("lookback") == "" {
			t.Error("expected lookback parameter")
		}

		resp := jaegerDependenciesResponse{
			Data: []jaegerDependency{
				{Parent: "api-gateway", Child: "auth-svc", CallCount: 500},
				{Parent: "api-gateway", Child: "payment-svc", CallCount: 300},
				{Parent: "payment-svc", Child: "postgres-db", CallCount: 200},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewJaegerClient(server.URL, nil)
	edges, err := client.GetDependencies(context.Background(), 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(edges))
	}
	if edges[0].Parent != "api-gateway" || edges[0].Child != "auth-svc" {
		t.Errorf("unexpected first edge: %+v", edges[0])
	}
	if edges[2].CallCount != 200 {
		t.Errorf("expected callCount 200, got %d", edges[2].CallCount)
	}
}

func TestJaegerClient_UnsupportedMethods(t *testing.T) {
	client := NewJaegerClient("http://unused", nil)

	// These should return nil (no error, no data) since Jaeger doesn't support them
	metrics, err := client.QueryMetric(context.Background(), "up", time.Now(), time.Now(), time.Minute)
	if err != nil || metrics != nil {
		t.Error("QueryMetric should return nil, nil for Jaeger")
	}

	svcMetrics, err := client.GetServiceMetrics(context.Background(), "test", time.Minute)
	if err != nil || svcMetrics != nil {
		t.Error("GetServiceMetrics should return nil, nil for Jaeger")
	}

	logs, err := client.SearchLogs(context.Background(), LogFilter{})
	if err != nil || logs != nil {
		t.Error("SearchLogs should return nil, nil for Jaeger")
	}
}

func TestJaegerClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("service unavailable")); err != nil {
			t.Logf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewJaegerClient(server.URL, nil)
	_, err := client.GetDependencies(context.Background(), 5*time.Minute)
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}
