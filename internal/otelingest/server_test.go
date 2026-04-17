package otelingest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/protobuf/proto"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// newTestServer builds a Server with a captureEmitter attached. The httpSrv is
// not started; we exercise handlers directly through the Mux.
func newTestServer(t *testing.T) (*Server, *captureEmitter) {
	t.Helper()
	em := &captureEmitter{}
	s := NewServer(DefaultConfig(), em, logr.Discard())
	return s, em
}

// invoke dispatches a request through the Server's Mux without going over the network.
func invoke(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestServer_Healthz(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := invoke(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
}

func TestServer_TracesRejectsGET(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/traces", nil)
	rec := invoke(s, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestServer_LogsRejectsGET(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/logs", nil)
	rec := invoke(s, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestServer_TracesProtobufHappyPath(t *testing.T) {
	s, em := newTestServer(t)

	start := time.Now()
	end := start.Add(5 * time.Millisecond)
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("k8s.namespace.name", "demo"),
				kvStr("k8s.pod.name", "api-0"),
				kvStr("k8s.node.name", "node-a"),
				kvStr("service.name", "checkout"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
				TraceId:           []byte{0x1, 0x2},
				SpanId:            []byte{0x3, 0x4},
				Name:              "server-op",
				Kind:              tracepb.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: uint64(start.UnixNano()),
				EndTimeUnixNano:   uint64(end.UnixNano()),
				Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentTypeProtobuf)
	rec := invoke(s, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != contentTypeProtobuf {
		t.Errorf("response content-type = %q", rec.Header().Get("Content-Type"))
	}
	// Verify response is a decodable ExportTraceServiceResponse.
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid protobuf: %v", err)
	}
	if len(em.all()) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(em.all()))
	}
	if _, ok := em.all()[0].(watcher.OTelSpanErrorEvent); !ok {
		t.Fatalf("expected OTelSpanErrorEvent, got %T", em.all()[0])
	}
}

func TestServer_LogsProtobufHappyPath(t *testing.T) {
	s, em := newTestServer(t)

	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", "checkout"),
				kvStr("k8s.namespace.name", "demo"),
				kvStr("k8s.pod.name", "api-0"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
				SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "boom"}},
			}}}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentTypeProtobuf)
	rec := invoke(s, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp collogspb.ExportLogsServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid protobuf: %v", err)
	}
	if len(em.all()) != 1 {
		t.Fatalf("expected 1 emitted log event, got %d", len(em.all()))
	}
}

func TestServer_TracesJSONContentType(t *testing.T) {
	s, em := newTestServer(t)

	// Minimal hand-written OTLP JSON with an ERROR span.
	payload := `{
		"resourceSpans":[{
			"resource":{"attributes":[
				{"key":"service.name","value":{"stringValue":"checkout"}},
				{"key":"k8s.namespace.name","value":{"stringValue":"demo"}},
				{"key":"k8s.pod.name","value":{"stringValue":"api-0"}}
			]},
			"scopeSpans":[{"spans":[{
				"traceId":"0102030405060708090a0b0c0d0e0f10",
				"spanId":"1112131415161718",
				"name":"server-op",
				"kind":2,
				"status":{"code":2}
			}]}]
		}]
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(payload))
	r.Header.Set("Content-Type", contentTypeJSON)
	rec := invoke(s, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != contentTypeJSON {
		t.Errorf("response content-type = %q", rec.Header().Get("Content-Type"))
	}
	if len(em.all()) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(em.all()))
	}
}

func TestServer_TracesBadProtobufReturns400(t *testing.T) {
	s, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("not-protobuf"))
	r.Header.Set("Content-Type", contentTypeProtobuf)
	rec := invoke(s, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServer_TracesOversizedReturns413(t *testing.T) {
	em := &captureEmitter{}
	cfg := DefaultConfig()
	cfg.MaxRequestBytes = 16 // absurdly small cap
	s := NewServer(cfg, em, logr.Discard())

	// Valid protobuf encoded trace batch is still larger than 16 bytes once
	// it includes any ResourceSpans.
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvStr("service.name", "checkout-with-long-service-name"),
			}},
		}},
	}
	body, _ := proto.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentTypeProtobuf)
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec := invoke(s, r)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServer_NeedLeaderElectionIsFalse(t *testing.T) {
	s, _ := newTestServer(t)
	if s.NeedLeaderElection() {
		t.Fatal("ingest server should run without leader election")
	}
}
