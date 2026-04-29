package jaeger

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestGetDependenciesDecodesJaegerEnvelope(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dependencies" {
			t.Fatalf("path = %q, want /api/dependencies", r.URL.Path)
		}
		if got := r.URL.Query().Get("lookback"); got != "86400000" {
			t.Fatalf("lookback = %q, want 86400000", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"parent":"frontend","child":"checkout","callCount":42}]}`))
	}))
	defer upstream.Close()

	deps, err := New(upstream.URL).GetDependencies(context.Background(), 24*time.Hour, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("GetDependencies error = %v", err)
	}
	if len(deps) != 1 || deps[0].Parent != "frontend" || deps[0].Child != "checkout" || deps[0].CallCount != 42 {
		t.Fatalf("dependencies = %+v, want frontend->checkout x42", deps)
	}
}

func TestGetDependenciesDecodesRawArray(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"parent":"quote","child":"product-catalog","callCount":7}]`))
	}))
	defer upstream.Close()

	deps, err := New(upstream.URL).GetDependencies(context.Background(), time.Hour, time.Time{})
	if err != nil {
		t.Fatalf("GetDependencies error = %v", err)
	}
	if len(deps) != 1 || deps[0].Parent != "quote" || deps[0].Child != "product-catalog" || deps[0].CallCount != 7 {
		t.Fatalf("dependencies = %+v, want quote->product-catalog x7", deps)
	}
}

func TestGetDependenciesReturnsTimeoutError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	c := New(upstream.URL).WithHTTPClient(&http.Client{Timeout: 1 * time.Millisecond})
	deps, err := c.GetDependencies(context.Background(), time.Hour, time.Time{})
	if err == nil {
		t.Fatalf("GetDependencies error = nil, want timeout error")
	}
	if deps != nil {
		t.Fatalf("GetDependencies deps = %+v, want nil", deps)
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("GetDependencies error = %T %[1]v, want wrapped url.Error", err)
	}
}
