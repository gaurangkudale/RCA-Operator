package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

// --- HTTP-level: handleTrace ----------------------------------------------

func TestHandleTrace_RejectsNonGet(t *testing.T) {
	s := &Server{cache: newJSONCache(defaultCacheTTL), jc: &jaeger.Client{}}
	rr := httptest.NewRecorder()
	s.handleTrace(rr, httptest.NewRequest(http.MethodPost, "/api/traces/abcdef0123456789", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestHandleTrace_NoJaegerClient_Returns503(t *testing.T) {
	s := &Server{cache: newJSONCache(defaultCacheTTL)}
	rr := httptest.NewRecorder()
	s.handleTrace(rr, httptest.NewRequest(http.MethodGet, "/api/traces/abcdef0123456789", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandleTrace_RejectsMalformedID(t *testing.T) {
	s := &Server{cache: newJSONCache(defaultCacheTTL), jc: &jaeger.Client{}}
	cases := []string{
		"",                         // empty
		"short",                    // < 8 chars
		"thisIDhasNonHexZZZZZZZZZ", // invalid chars
		strings.Repeat("a", 65),    // too long
	}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			rr := httptest.NewRecorder()
			s.handleTrace(rr, httptest.NewRequest(http.MethodGet, "/api/traces/"+id, nil))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("id=%q: status = %d, want 400", id, rr.Code)
			}
		})
	}
}

func TestLooksLikeTraceID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"abcdef01", true},
		{"ABCDEF0123456789abcdef01", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"abcdef0", false},       // too short
		{"abcdefgh", false},      // 'g'/'h' not hex
		{"abcdef01-2345", false}, // hyphen
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeTraceID(tc.in); got != tc.want {
			t.Errorf("looksLikeTraceID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- buildTraceResponse: shape & invariants -------------------------------

func TestBuildTraceResponse_NilTrace_ReturnsTraceIDOnly(t *testing.T) {
	got := buildTraceResponse("abcdef0123456789", nil)
	if got.TraceID != "abcdef0123456789" {
		t.Errorf("TraceID = %q", got.TraceID)
	}
	if got.Status != "ok" {
		t.Errorf("Status = %q, want ok", got.Status)
	}
	if got.SpanCount != 0 || len(got.Spans) != 0 {
		t.Errorf("expected empty span list for nil trace")
	}
}

func TestBuildTraceResponse_EmptySpans_ReturnsTraceIDOnly(t *testing.T) {
	got := buildTraceResponse("abc", &jaeger.Trace{TraceID: "abc"})
	if got.SpanCount != 0 {
		t.Errorf("SpanCount = %d, want 0", got.SpanCount)
	}
}

// makeTrace builds a small synthetic trace for buildTraceResponse tests.
// All time units are microseconds (matching jaeger.Span.StartTime/Duration).
func makeTrace(id string) *jaeger.Trace {
	return &jaeger.Trace{
		TraceID: id,
		Processes: map[string]jaeger.Process{
			"p1": {ServiceName: "frontend"},
			"p2": {ServiceName: "checkout"},
			"p3": {ServiceName: "cart"},
		},
		Spans: []jaeger.Span{
			// root: frontend, 0..500ms
			{
				SpanID: "s1", ProcessID: "p1", OperationName: "GET /checkout",
				StartTime: 0, Duration: 500_000,
			},
			// child: checkout, 100..400ms
			{
				SpanID: "s2", ProcessID: "p2", OperationName: "checkout",
				StartTime: 100_000, Duration: 300_000,
				References: []jaeger.SpanReference{{RefType: "CHILD_OF", SpanID: "s1"}},
			},
			// grandchild: cart with error, 150..200ms
			{
				SpanID: "s3", ProcessID: "p3", OperationName: "cart.GetCart",
				StartTime: 150_000, Duration: 50_000,
				References: []jaeger.SpanReference{{RefType: "CHILD_OF", SpanID: "s2"}},
				Tags:       []jaeger.KeyValue{{Key: "error", Value: true}, {Key: "http.status_code", Value: float64(500)}},
			},
		},
	}
}

func TestBuildTraceResponse_SummaryAndOrdering(t *testing.T) {
	got := buildTraceResponse("abc", makeTrace("abc"))

	if got.SpanCount != 3 {
		t.Errorf("SpanCount = %d, want 3", got.SpanCount)
	}
	if got.ServiceCount != 3 {
		t.Errorf("ServiceCount = %d, want 3", got.ServiceCount)
	}
	// Total duration = end-start = 500_000us, NOT the sum of spans.
	if got.DurationMicros != 500_000 {
		t.Errorf("DurationMicros = %d, want 500000 (end-start, not sum)", got.DurationMicros)
	}
	if got.Status != "error" {
		t.Errorf("Status = %q, want error (s3 has error tag)", got.Status)
	}
	if len(got.ErrorSpans) != 1 || got.ErrorSpans[0] != "s3" {
		t.Errorf("ErrorSpans = %v, want [s3]", got.ErrorSpans)
	}
	if got.RootService != "frontend" || got.RootOperation != "GET /checkout" {
		t.Errorf("root = %q/%q", got.RootService, got.RootOperation)
	}
	// Spans should be ordered by start time
	if got.Spans[0].SpanID != "s1" || got.Spans[1].SpanID != "s2" || got.Spans[2].SpanID != "s3" {
		t.Errorf("spans not ordered by start time: %v %v %v", got.Spans[0].SpanID, got.Spans[1].SpanID, got.Spans[2].SpanID)
	}
	// Depth: s1=0, s2=1, s3=2
	if got.Spans[0].Depth != 0 || got.Spans[1].Depth != 1 || got.Spans[2].Depth != 2 {
		t.Errorf("depth wrong: %d %d %d", got.Spans[0].Depth, got.Spans[1].Depth, got.Spans[2].Depth)
	}
	// Critical path = root duration in ms
	if got.CriticalPathMs == 0 {
		t.Errorf("CriticalPathMs should be set when root exists")
	}
}

func TestBuildTraceResponse_ServiceBreakdown_NoOverlapDoubleCount(t *testing.T) {
	// Two concurrent spans in the SAME service — total time should be the
	// merged interval, not the sum.
	tr := &jaeger.Trace{
		TraceID:   "abc",
		Processes: map[string]jaeger.Process{"p": {ServiceName: "checkout"}},
		Spans: []jaeger.Span{
			{SpanID: "a", ProcessID: "p", StartTime: 0, Duration: 200_000},
			{SpanID: "b", ProcessID: "p", StartTime: 50_000, Duration: 200_000}, // overlaps a
		},
	}
	got := buildTraceResponse("abc", tr)
	if len(got.ServiceBreakdown) != 1 {
		t.Fatalf("want 1 service entry, got %d", len(got.ServiceBreakdown))
	}
	bd := got.ServiceBreakdown[0]
	if bd.Service != "checkout" {
		t.Errorf("service = %q", bd.Service)
	}
	// Merged interval is [0, 250_000]; sum-of-durations would be 400_000.
	if bd.TotalDurUs != 250_000 {
		t.Errorf("TotalDurUs = %d, want 250000 (overlap-merged, not summed)", bd.TotalDurUs)
	}
	if bd.SpanCount != 2 {
		t.Errorf("SpanCount = %d, want 2", bd.SpanCount)
	}
}

func TestBuildTraceResponse_TruncatesAt500_PreservesErrorSpans(t *testing.T) {
	tr := &jaeger.Trace{
		TraceID:   "big",
		Processes: map[string]jaeger.Process{"p": {ServiceName: "svc"}},
	}
	for i := range 700 {
		sp := jaeger.Span{
			SpanID:    "s" + repeat("x", i),
			ProcessID: "p",
			StartTime: int64(i),
			Duration:  100,
		}
		// Make 5 spans error spans with very short durations so they would
		// otherwise get sorted out of the top-N by duration.
		if i < 5 {
			sp.Tags = []jaeger.KeyValue{{Key: "error", Value: true}}
			sp.Duration = 1 // smallest possible
		}
		tr.Spans = append(tr.Spans, sp)
	}
	got := buildTraceResponse("big", tr)

	if !got.Truncated {
		t.Errorf("expected Truncated=true for >500-span trace")
	}
	if len(got.Spans) != maxSpansReturned {
		t.Errorf("len(Spans) = %d, want %d", len(got.Spans), maxSpansReturned)
	}
	// All error spans preserved despite tiny duration.
	keptError := 0
	for _, sp := range got.Spans {
		if sp.IsError {
			keptError++
		}
	}
	if keptError != 5 {
		t.Errorf("expected 5 error spans preserved, got %d", keptError)
	}
}

// --- Tag extraction ---------------------------------------------------------

func TestExtractTags_HTTPDBKubernetesAndOTel(t *testing.T) {
	sp := &jaeger.Span{
		Tags: []jaeger.KeyValue{
			{Key: "span.kind", Value: "client"},
			{Key: "http.method", Value: "POST"},
			{Key: "http.url", Value: "https://api/checkout"},
			{Key: "http.status_code", Value: "503"},
			{Key: "db.system", Value: "postgresql"},
			{Key: "db.statement", Value: "SELECT 1"},
			{Key: "k8s.pod.name", Value: "checkout-abc"},
			{Key: "k8s.namespace.name", Value: "shop"},
			{Key: "k8s.node.name", Value: "node-1"},
			{Key: "k8s.container.name", Value: "checkout"},
			{Key: "peer.service", Value: "cart"},
			{Key: "custom.user.id", Value: "42"},
		},
	}
	ts := &traceSpan{}
	extractTags(sp, ts)

	if ts.Kind != "CLIENT" {
		t.Errorf("Kind = %q, want CLIENT (uppercased)", ts.Kind)
	}
	if ts.HTTPMethod != "POST" || ts.HTTPURL != "https://api/checkout" {
		t.Errorf("HTTP = %q %q", ts.HTTPMethod, ts.HTTPURL)
	}
	if ts.StatusCode != "503" || !ts.IsError {
		t.Errorf("HTTP 503 should set StatusCode and flag IsError")
	}
	if ts.DBSystem != "postgresql" || ts.DBStatement != "SELECT 1" {
		t.Errorf("DB = %q %q", ts.DBSystem, ts.DBStatement)
	}
	if ts.PodName != "checkout-abc" || ts.PodNamespace != "shop" {
		t.Errorf("k8s pod tags missing")
	}
	if ts.NodeName != "node-1" || ts.ContainerName != "checkout" {
		t.Errorf("k8s node/container tags missing")
	}
	if ts.PeerService != "cart" {
		t.Errorf("PeerService = %q", ts.PeerService)
	}
	if v := ts.Tags["custom.user.id"]; v != "42" {
		t.Errorf("custom tag should land in freeform Tags, got %q", v)
	}
}

func TestExtractTags_FlagsErrorOnOTelStatusError(t *testing.T) {
	sp := &jaeger.Span{Tags: []jaeger.KeyValue{
		{Key: "otel.status_code", Value: "ERROR"},
	}}
	ts := &traceSpan{}
	extractTags(sp, ts)
	if !ts.IsError {
		t.Errorf("OTel status_code=ERROR should flag IsError")
	}
}

func TestExtractTags_FlagsErrorOnGRPCNonZero(t *testing.T) {
	sp := &jaeger.Span{Tags: []jaeger.KeyValue{
		{Key: "rpc.grpc.status_code", Value: float64(14)}, // UNAVAILABLE
	}}
	ts := &traceSpan{}
	extractTags(sp, ts)
	if !ts.IsError {
		t.Errorf("non-zero gRPC status should flag IsError")
	}
	if !strings.HasPrefix(ts.StatusCode, "grpc:") {
		t.Errorf("StatusCode = %q, want grpc:N prefix", ts.StatusCode)
	}
}

func TestExtractProcessTags_FillsK8sFromProcessLevel(t *testing.T) {
	p := jaeger.Process{Tags: []jaeger.KeyValue{
		{Key: "k8s.pod.name", Value: "p1"},
		{Key: "k8s.namespace.name", Value: "ns1"},
		{Key: "k8s.node.name", Value: "n1"},
		{Key: "k8s.container.name", Value: "c1"},
	}}
	ts := &traceSpan{}
	extractProcessTags(p, ts)
	if ts.PodName != "p1" || ts.PodNamespace != "ns1" || ts.NodeName != "n1" || ts.ContainerName != "c1" {
		t.Errorf("k8s process tags not extracted: %+v", ts)
	}
}

// --- exception event extraction --------------------------------------------

func TestExtractErrorEvents_PullsExceptionFromLogs(t *testing.T) {
	// Jaeger encodes span logs as JSON arrays of fields. Our extractor uses
	// a forgiving substring scan rather than a real parser.
	logBody := `{"timestamp":1,"fields":[{"key":"event","value":"exception"},{"key":"exception.type","value":"NullPointerException"},{"key":"exception.message","value":"npe at line 42"}]}`
	sp := &jaeger.Span{Logs: []json.RawMessage{json.RawMessage(logBody)}}
	ts := &traceSpan{}
	extractErrorEvents(sp, ts)
	if ts.ExceptionType != "NullPointerException" {
		t.Errorf("ExceptionType = %q", ts.ExceptionType)
	}
	if !strings.Contains(ts.ExceptionMsg, "npe") {
		t.Errorf("ExceptionMsg = %q", ts.ExceptionMsg)
	}
	if !ts.IsError {
		t.Errorf("exception event should flag IsError")
	}
}

// --- helpers --------------------------------------------------------------

func TestStringValue_HandlesAllJSONTypes(t *testing.T) {
	cases := map[any]string{
		"hello":      "hello",
		true:         "true",
		false:        "false",
		float64(42):  "42",
		float64(3.5): "3.5",
		int(7):       "7",
		int64(123):   "123",
	}
	for in, want := range cases {
		if got := stringValue(in); got != want {
			t.Errorf("stringValue(%v) = %q, want %q", in, got, want)
		}
	}
	// Unknown types collapse to empty.
	if got := stringValue(struct{}{}); got != "" {
		t.Errorf("unknown type should produce empty string, got %q", got)
	}
}

func TestIsHTTPError(t *testing.T) {
	cases := map[string]bool{
		"": false, "200": false, "302": false, "404": true, "500": true, "503": true, "1xx": false,
	}
	for in, want := range cases {
		if got := isHTTPError(in); got != want {
			t.Errorf("isHTTPError(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTruncate_AddsEllipsisOnlyWhenTruncating(t *testing.T) {
	if got := truncate("hi", 10); got != "hi" {
		t.Errorf("short string should pass through, got %q", got)
	}
	got := truncate("abcdefghij", 5)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

func TestMergeIntervals_HandlesOverlapAdjacencyAndDisjoint(t *testing.T) {
	cases := []struct {
		name string
		in   [][2]int64
		want [][2]int64
	}{
		{name: "empty", in: nil, want: nil},
		{name: "single", in: [][2]int64{{0, 5}}, want: [][2]int64{{0, 5}}},
		{name: "overlap", in: [][2]int64{{0, 5}, {3, 8}}, want: [][2]int64{{0, 8}}},
		{name: "adjacent merges", in: [][2]int64{{0, 5}, {5, 10}}, want: [][2]int64{{0, 10}}},
		{name: "disjoint", in: [][2]int64{{0, 5}, {10, 15}}, want: [][2]int64{{0, 5}, {10, 15}}},
		{name: "unsorted input", in: [][2]int64{{10, 15}, {0, 5}}, want: [][2]int64{{0, 5}, {10, 15}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeIntervals(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got %v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("idx %d: got %v want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestJSONStringValue_FindsValueAfterKey(t *testing.T) {
	got := jsonStringValue(`{"key":"exception.type","value":"NullPointerException"}`, "exception.type")
	if got != "NullPointerException" {
		t.Errorf("got %q", got)
	}
	if got := jsonStringValue(`no match here`, "exception.type"); got != "" {
		t.Errorf("missing key should produce empty, got %q", got)
	}
}

// repeat is a tiny inline strings.Repeat — kept local so the test file can
// run before any production helper of the same name lands.
func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}
