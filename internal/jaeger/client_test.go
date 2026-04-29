package jaeger

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetTraceTreatsTimeoutAsMiss(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	c := New(upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	trace, err := c.GetTrace(ctx, "abcdef0123456789")
	if err != nil {
		t.Fatalf("GetTrace error = %v, want nil tolerant miss", err)
	}
	if trace != nil {
		t.Fatalf("GetTrace trace = %+v, want nil", trace)
	}
}

func TestGetTraceForDashboardReturnsTimeoutError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	c := New(upstream.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	trace, err := c.GetTraceForDashboard(ctx, "abcdef0123456789")
	if err == nil {
		t.Fatalf("GetTraceForDashboard error = nil, want timeout error")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("GetTraceForDashboard error = %v, want timeout/network error", err)
	}
	if trace != nil {
		t.Fatalf("GetTraceForDashboard trace = %+v, want nil", trace)
	}
}
