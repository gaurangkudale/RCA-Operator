// Package jaeger provides a minimal HTTP client for the Jaeger Query API used
// by the incident graph builder to resolve trace-id → service-call topology.
//
// The client is deliberately tolerant of misses: a 404, a network timeout, or
// an empty response yields (nil, nil) so the graph builder can fall back to
// the signal-only graph without surfacing an error to the reconciler. Only
// structural/unexpected errors (e.g. malformed JSON) are returned.
package jaeger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout is the HTTP timeout applied to every trace lookup.
// Short by design: the incident-graph builder must not block the reconcile
// loop if Jaeger is slow or unavailable.
const DefaultTimeout = 3 * time.Second

// Span is the subset of a Jaeger span the graph builder consumes.
type Span struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	OperationName string            `json:"operationName"`
	References    []SpanReference   `json:"references,omitempty"`
	StartTime     int64             `json:"startTime"`
	Duration      int64             `json:"duration"`
	ProcessID     string            `json:"processID"`
	Tags          []KeyValue        `json:"tags,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	Logs          []json.RawMessage `json:"logs,omitempty"`
}

// SpanReference links a span to its parent in the trace DAG.
type SpanReference struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

// KeyValue is a Jaeger tag.
type KeyValue struct {
	Key   string `json:"key"`
	Type  string `json:"type,omitempty"`
	Value any    `json:"value"`
}

// Process is a Jaeger process (service identity) referenced by ProcessID.
type Process struct {
	ServiceName string     `json:"serviceName"`
	Tags        []KeyValue `json:"tags,omitempty"`
}

// Trace is one trace returned by the Jaeger Query API.
type Trace struct {
	TraceID   string             `json:"traceID"`
	Spans     []Span             `json:"spans"`
	Processes map[string]Process `json:"processes"`
}

// tracesResponse is the envelope returned by GET /api/traces/{id}.
type tracesResponse struct {
	Data   []Trace         `json:"data"`
	Errors json.RawMessage `json:"errors,omitempty"`
}

// Client is a thin wrapper around net/http configured for the Jaeger Query API.
// A zero-value Client is unusable — callers must use New.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client rooted at baseURL (e.g. "http://jaeger-query.observability.svc:16686").
// If baseURL is empty the returned client is a no-op — GetTrace returns (nil, nil)
// on every call — so callers can always construct a client and rely on the
// graph builder's tolerate-miss behavior in environments without Jaeger.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: DefaultTimeout},
	}
}

// WithHTTPClient overrides the default http.Client, primarily for tests that
// want to inject a RoundTripper or a non-default timeout.
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	if hc != nil {
		c.http = hc
	}
	return c
}

// GetTrace fetches the trace with the given hex trace-id. A missing trace
// (404, empty data, timeout, connection error, or an unconfigured client) is
// reported as (nil, nil) so the incident graph builder can fall back
// gracefully. An unexpected non-2xx status or a malformed body returns an
// error.
func (c *Client) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	if c == nil || c.baseURL == "" || traceID == "" {
		return nil, nil
	}

	endpoint, err := url.JoinPath(c.baseURL, "api", "traces", traceID)
	if err != nil {
		return nil, fmt.Errorf("jaeger: build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("jaeger: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Network errors, context deadlines, and client timeouts are all
		// treated as misses — the graph builder falls back to signal-only.
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jaeger: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("jaeger: read body: %w", err)
	}

	var envelope tracesResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("jaeger: decode body: %w", err)
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	trace := envelope.Data[0]
	return &trace, nil
}
