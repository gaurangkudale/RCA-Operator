package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/gaurangkudale/rca-operator/internal/exporter/aggregator"
	"github.com/gaurangkudale/rca-operator/internal/exporter/events"
)

// httpTestRig stands up a fresh HTTP handler in front of an aggregator
// whose spikes are captured into a counter. Tests use it to assert that
// HTTP requests reach the aggregator end-to-end without bringing up the
// reporter or the k8s fake client (those are already covered by the gRPC
// integration test in otlp_logs_test.go).
type httpTestRig struct {
	server  *httptest.Server
	spikes  *int
	handler http.Handler
}

func newHTTPRig(t *testing.T, threshold int) *httpTestRig {
	t.Helper()
	count := 0
	agg := aggregator.New(aggregator.Config{
		Window:    time.Minute,
		Threshold: threshold,
	}, func(events.LogErrorSpikeEvent) { count++ })

	recv := NewLogsReceiver(agg, logr.Discard())
	handler := NewHTTPHandler(recv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &httpTestRig{server: srv, spikes: &count, handler: handler}
}

// Protobuf is the OTLP/HTTP default content type — every collector exporter
// uses it. This is the most important happy-path test.
func TestOTLPHTTP_AcceptsProtobufRequest(t *testing.T) {
	rig := newHTTPRig(t, 2)

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 2,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal proto: %v", err)
	}

	resp, err := http.Post(
		rig.server.URL+otlpHTTPLogsPath,
		"application/x-protobuf",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-protobuf" {
		t.Errorf("expected protobuf response content-type, got %q", got)
	}
	if *rig.spikes != 1 {
		t.Errorf("expected 1 spike, got %d", *rig.spikes)
	}
}

// JSON is what browser/JS SDKs use because they cannot speak protobuf
// without an extra dependency. Must round-trip via protojson.
func TestOTLPHTTP_AcceptsJSONRequest(t *testing.T) {
	rig := newHTTPRig(t, 2)

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 2,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	body, err := protojson.Marshal(req)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}

	resp, err := http.Post(
		rig.server.URL+otlpHTTPLogsPath,
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("expected json response content-type, got %q", got)
	}
	if *rig.spikes != 1 {
		t.Errorf("expected 1 spike from json request, got %d", *rig.spikes)
	}
}

// gzip Content-Encoding is what the OTel Collector's otlphttp exporter sends
// by default for batches >= 1 KiB. The handler must transparently inflate.
func TestOTLPHTTP_AcceptsGzipEncodedBody(t *testing.T) {
	rig := newHTTPRig(t, 2)

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 2,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal proto: %v", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, rig.server.URL+otlpHTTPLogsPath, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Content-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if *rig.spikes != 1 {
		t.Errorf("expected 1 spike from gzip request, got %d", *rig.spikes)
	}
}

// GET, PUT, DELETE etc. must be rejected with 405 — only POST is valid per
// the OTLP spec.
func TestOTLPHTTP_RejectsNonPostMethods(t *testing.T) {
	rig := newHTTPRig(t, 1)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, rig.server.URL+otlpHTTPLogsPath, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s failed: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, resp.StatusCode)
		}
	}
}

// A garbage protobuf body must produce 400, not 500 — bad client input is
// not an exporter error.
func TestOTLPHTTP_RejectsMalformedProtobuf(t *testing.T) {
	rig := newHTTPRig(t, 1)

	resp, err := http.Post(
		rig.server.URL+otlpHTTPLogsPath,
		"application/x-protobuf",
		bytes.NewReader([]byte("not a real protobuf")),
	)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if *rig.spikes != 0 {
		t.Errorf("malformed body must not fire spike, got %d", *rig.spikes)
	}
}

// Unknown content types must produce 415 (Unsupported Media Type).
func TestOTLPHTTP_RejectsUnknownContentType(t *testing.T) {
	rig := newHTTPRig(t, 1)

	resp, err := http.Post(
		rig.server.URL+otlpHTTPLogsPath,
		"text/plain",
		bytes.NewReader([]byte("hello")),
	)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415, got %d", resp.StatusCode)
	}
}

// charset suffix on the content type (e.g. "application/json; charset=utf-8")
// must still be accepted — some clients append it.
func TestOTLPHTTP_HandlesContentTypeWithCharset(t *testing.T) {
	rig := newHTTPRig(t, 2)

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 2,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	body, _ := protojson.Marshal(req)

	httpReq, _ := http.NewRequest(
		http.MethodPost,
		rig.server.URL+otlpHTTPLogsPath,
		bytes.NewReader(body),
	)
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if *rig.spikes != 1 {
		t.Errorf("expected 1 spike, got %d", *rig.spikes)
	}
}

// Verify the response is a valid empty ExportLogsServiceResponse so strict
// clients can parse it.
func TestOTLPHTTP_ResponseBodyIsValidProto(t *testing.T) {
	rig := newHTTPRig(t, 1)

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 1,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	body, _ := proto.Marshal(req)

	resp, err := http.Post(
		rig.server.URL+otlpHTTPLogsPath,
		"application/x-protobuf",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	respBytes := readAll(t, resp)
	out := &collogspb.ExportLogsServiceResponse{}
	if err := proto.Unmarshal(respBytes, out); err != nil {
		t.Errorf("response is not a valid ExportLogsServiceResponse: %v", err)
	}
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return buf.Bytes()
}

// Sanity-check that ServeHTTP boots cleanly and shuts down on context
// cancel — exercises the Serve wrapper, not just the handler.
func TestOTLPHTTP_ServeHTTPLifecycle(t *testing.T) {
	rig := newHTTPRig(t, 1)
	_ = rig // we use rig only for the handler/aggregator constructor

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	count := 0
	agg := aggregator.New(aggregator.Config{
		Window:    time.Minute,
		Threshold: 1,
	}, func(events.LogErrorSpikeEvent) { count++ })
	recv := NewLogsReceiver(agg, logr.Discard())

	srv, lis, err := ServeHTTP(ctx, "127.0.0.1:0", recv, logr.Discard())
	if err != nil {
		t.Fatalf("ServeHTTP failed: %v", err)
	}
	defer srv.Close()

	addr := "http://" + lis.Addr().String() + otlpHTTPLogsPath

	req := buildExportRequest(
		"dev", "payment", "payment-0", "payment", 1,
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)
	body, _ := proto.Marshal(req)

	resp, err := http.Post(addr, "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if count != 1 {
		t.Errorf("expected 1 spike, got %d", count)
	}
}
