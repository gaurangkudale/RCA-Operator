package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSigNozClient_FindTracesByService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/traces" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("service") != "payment-svc" {
			t.Errorf("unexpected service param: %s", r.URL.Query().Get("service"))
		}

		resp := signozTracesResponse{
			Traces: []signozTraceSummary{
				{
					TraceID:         "abc123",
					RootServiceName: "payment-svc",
					RootSpanName:    "POST /checkout",
					StartTimeUnix:   time.Now().UnixNano(),
					DurationNano:    int64(500 * time.Millisecond),
					SpanCount:       5,
					HasError:        true,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	traces, err := client.FindTracesByService(context.Background(), "payment-svc", time.Now().Add(-1*time.Hour), time.Now(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0].TraceID != "abc123" {
		t.Errorf("expected traceID abc123, got %s", traces[0].TraceID)
	}
	if traces[0].RootService != "payment-svc" {
		t.Errorf("expected root service payment-svc, got %s", traces[0].RootService)
	}
	if !traces[0].HasError {
		t.Error("expected HasError=true")
	}
}

func TestSigNozClient_GetTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/traces/abc123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := signozTraceDetailResponse{
			Spans: []signozSpan{
				{
					TraceID:       "abc123",
					SpanID:        "span1",
					OperationName: "POST /checkout",
					ServiceName:   "payment-svc",
					StartTimeUnix: time.Now().UnixNano(),
					DurationNano:  int64(500 * time.Millisecond),
					StatusCode:    2, // Error
				},
				{
					TraceID:       "abc123",
					SpanID:        "span2",
					ParentSpanID:  "span1",
					OperationName: "SELECT inventory",
					ServiceName:   "inventory-db",
					StartTimeUnix: time.Now().UnixNano(),
					DurationNano:  int64(200 * time.Millisecond),
					StatusCode:    1, // OK
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	trace, err := client.GetTrace(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trace == nil {
		t.Fatal("expected trace, got nil")
		return
	}
	if trace.TraceID != "abc123" {
		t.Errorf("expected traceID abc123, got %s", trace.TraceID)
	}
	if len(trace.Spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(trace.Spans))
	}
	if trace.Spans[0].ServiceName != "payment-svc" {
		t.Errorf("expected service payment-svc, got %s", trace.Spans[0].ServiceName)
	}
	if trace.Spans[0].StatusCode != StatusError {
		t.Errorf("expected StatusError, got %d", trace.Spans[0].StatusCode)
	}
}

func TestSigNozClient_GetDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services/dependencies" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		deps := []signozDependency{
			{Parent: "api-gateway", Child: "payment-svc", CallCount: 1000},
			{Parent: "payment-svc", Child: "postgres-db", CallCount: 500},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(deps); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	edges, err := client.GetDependencies(context.Background(), 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	if edges[0].Parent != "api-gateway" || edges[0].Child != "payment-svc" {
		t.Errorf("unexpected first edge: %v", edges[0])
	}
	if edges[0].CallCount != 1000 {
		t.Errorf("expected callCount 1000, got %d", edges[0].CallCount)
	}
}

func TestSigNozClient_SearchLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/logs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("traceID") != "trace-123" {
			t.Errorf("unexpected traceID param: %s", r.URL.Query().Get("traceID"))
		}

		resp := signozLogsResponse{
			Logs: []signozLogEntry{
				{
					Timestamp:   time.Now().UnixNano(),
					Body:        "Connection timeout to database",
					Severity:    "ERROR",
					ServiceName: "payment-svc",
					TraceID:     "trace-123",
					SpanID:      "span-456",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	logs, err := client.SearchLogs(context.Background(), LogFilter{
		TraceID: "trace-123",
		Start:   time.Now().Add(-1 * time.Hour),
		End:     time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].Severity != "ERROR" {
		t.Errorf("expected severity ERROR, got %s", logs[0].Severity)
	}
	if logs[0].TraceID != "trace-123" {
		t.Errorf("expected traceID trace-123, got %s", logs[0].TraceID)
	}
}

func TestSigNozClient_GetServiceMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		services := []signozServiceOverview{
			{
				ServiceName: "payment-svc",
				P50:         10.5,
				P95:         50.2,
				P99:         120.8,
				CallRate:    42.5,
				NumCalls:    5000,
				NumErrors:   50,
				ErrorRate:   1.0,
			},
			{
				ServiceName: "other-svc",
				P50:         5.0,
				P95:         20.0,
				P99:         40.0,
				CallRate:    100.0,
				NumCalls:    10000,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(services); err != nil {
			t.Logf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	metrics, err := client.GetServiceMetrics(context.Background(), "payment-svc", 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics == nil {
		t.Fatal("expected metrics, got nil")
		return
	}
	if metrics.ServiceName != "payment-svc" {
		t.Errorf("expected service payment-svc, got %s", metrics.ServiceName)
	}
	if metrics.RequestRate != 42.5 {
		t.Errorf("expected requestRate 42.5, got %f", metrics.RequestRate)
	}
	if metrics.P99Latency != 120.8 {
		t.Errorf("expected p99 120.8, got %f", metrics.P99Latency)
	}
}

func TestSigNozClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte("internal error")); err != nil {
			t.Logf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewSigNozClient(server.URL, nil)
	_, err := client.FindTracesByService(context.Background(), "test", time.Now().Add(-1*time.Hour), time.Now(), 10)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
