package otelingest

import (
	"regexp"
	"sync"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// captureEmitter collects every event emitted during a test run.
type captureEmitter struct {
	mu     sync.Mutex
	events []watcher.CorrelatorEvent
}

func (c *captureEmitter) Emit(e watcher.CorrelatorEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureEmitter) all() []watcher.CorrelatorEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]watcher.CorrelatorEvent, len(c.events))
	copy(out, c.events)
	return out
}

// ---- helpers -----------------------------------------------------------

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func kvBool(k string, v bool) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v}}}
}

func k8sResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		kvStr("service.name", "checkout"),
		kvStr("k8s.namespace.name", "demo"),
		kvStr("k8s.pod.name", "api-0"),
		kvStr("k8s.node.name", "node-a"),
	}}
}

func defaultCfg() Config {
	cfg := DefaultConfig()
	return cfg
}

// ---- translator tests --------------------------------------------------

func TestTranslator_ErrorStatusEmitsSpanError(t *testing.T) {
	em := &captureEmitter{}
	tr := NewTranslator(defaultCfg(), em)

	start := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Millisecond)
	rss := []*tracepb.ResourceSpans{{
		Resource: k8sResource(),
		ScopeSpans: []*tracepb.ScopeSpans{{
			Spans: []*tracepb.Span{{
				TraceId:           []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
				SpanId:            []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
				Name:              "GET /checkout",
				Kind:              tracepb.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: uint64(start.UnixNano()),
				EndTimeUnixNano:   uint64(end.UnixNano()),
				Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "upstream timeout"},
			}},
		}},
	}}

	n := tr.TranslateResourceSpans(rss)
	if n != 1 {
		t.Fatalf("expected 1 emitted event, got %d", n)
	}
	events := em.all()
	evt, ok := events[0].(watcher.OTelSpanErrorEvent)
	if !ok {
		t.Fatalf("expected OTelSpanErrorEvent, got %T", events[0])
	}
	if evt.Namespace != "demo" || evt.PodName != "api-0" || evt.NodeName != "node-a" {
		t.Errorf("k8s resource attrs not propagated: %+v", evt.BaseEvent)
	}
	if evt.StatusCode != "STATUS_CODE_ERROR" {
		t.Errorf("status code = %q", evt.StatusCode)
	}
	if evt.SpanKind != "SERVER" {
		t.Errorf("span kind = %q", evt.SpanKind)
	}
	if evt.TraceID == "" || evt.SpanID == "" {
		t.Error("trace/span IDs not hex-encoded")
	}
}

func TestTranslator_HTTPStatus500EmitsEvenOnUnsetStatus(t *testing.T) {
	em := &captureEmitter{}
	tr := NewTranslator(defaultCfg(), em)

	rss := []*tracepb.ResourceSpans{{
		Resource: k8sResource(),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: []byte{0xaa}, SpanId: []byte{0xbb},
			Name:       "POST /order",
			Status:     &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET},
			Attributes: []*commonpb.KeyValue{kvInt("http.status_code", 503)},
		}}}},
	}}

	n := tr.TranslateResourceSpans(rss)
	if n != 1 {
		t.Fatalf("expected 1 emitted event, got %d", n)
	}
	if _, ok := em.all()[0].(watcher.OTelSpanErrorEvent); !ok {
		t.Fatalf("expected OTelSpanErrorEvent, got %T", em.all()[0])
	}
}

func TestTranslator_HTTPStatus200DoesNotEmit(t *testing.T) {
	em := &captureEmitter{}
	tr := NewTranslator(defaultCfg(), em)

	rss := []*tracepb.ResourceSpans{{
		Resource: k8sResource(),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: []byte{0xaa}, SpanId: []byte{0xbb},
			Name:       "GET /healthz",
			Status:     &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET},
			Attributes: []*commonpb.KeyValue{kvInt("http.status_code", 200)},
		}}}},
	}}

	if n := tr.TranslateResourceSpans(rss); n != 0 {
		t.Fatalf("expected 0 emitted events, got %d", n)
	}
}

func TestTranslator_LatencySpikeEmitsSeparateEvent(t *testing.T) {
	em := &captureEmitter{}
	cfg := defaultCfg()
	cfg.TraceFilters.LatencyP99Ms = 100
	tr := NewTranslator(cfg, em)

	start := time.Now()
	end := start.Add(250 * time.Millisecond)

	rss := []*tracepb.ResourceSpans{{
		Resource: k8sResource(),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: []byte{0xaa}, SpanId: []byte{0xbb},
			Name:              "GET /slow",
			Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR},
			StartTimeUnixNano: uint64(start.UnixNano()),
			EndTimeUnixNano:   uint64(end.UnixNano()),
		}}}},
	}}

	n := tr.TranslateResourceSpans(rss)
	if n != 2 {
		t.Fatalf("expected both error + latency events (2), got %d", n)
	}
	var sawErr, sawLatency bool
	for _, e := range em.all() {
		switch e.(type) {
		case watcher.OTelSpanErrorEvent:
			sawErr = true
		case watcher.OTelSpanLatencySpikeEvent:
			sawLatency = true
		}
	}
	if !sawErr || !sawLatency {
		t.Fatalf("missing event types; err=%v latency=%v", sawErr, sawLatency)
	}
}

func TestTranslator_SpanEventBecomesOwnSignal(t *testing.T) {
	em := &captureEmitter{}
	rss := []*tracepb.ResourceSpans{{
		Resource: k8sResource(),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: []byte{0xaa}, SpanId: []byte{0xbb},
			Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
			Events: []*tracepb.Span_Event{{
				Name: "exception",
				Attributes: []*commonpb.KeyValue{
					kvStr("exception.message", "NPE at user@example.com"),
					kvBool("exception.escaped", true),
				},
			}},
		}}}},
	}}

	// Redact emails so we can verify redaction reaches span-event attrs.
	cfg := defaultCfg()
	cfg.Redaction = []*regexp.Regexp{regexp.MustCompile(`[\w.+-]+@[\w.-]+`)}
	tr := NewTranslator(cfg, em)

	n := tr.TranslateResourceSpans(rss)
	if n != 1 {
		t.Fatalf("expected 1 emitted event, got %d", n)
	}
	evt, ok := em.all()[0].(watcher.OTelSpanEventEvent)
	if !ok {
		t.Fatalf("expected OTelSpanEventEvent, got %T", em.all()[0])
	}
	if evt.EventName != "exception" {
		t.Errorf("event name = %q", evt.EventName)
	}
	msg := evt.Attrs["exception.message"]
	if msg == "" || msg == "NPE at user@example.com" {
		t.Errorf("exception.message not redacted: %q", msg)
	}
}

func TestTranslator_LogBelowSeverityDropped(t *testing.T) {
	em := &captureEmitter{}
	tr := NewTranslator(defaultCfg(), em)

	rls := []*logspb.ResourceLogs{{
		Resource: k8sResource(),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
			SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "normal"}},
		}}}},
	}}

	if n := tr.TranslateResourceLogs(rls); n != 0 {
		t.Fatalf("INFO should be dropped under default WARN filter, got %d emissions", n)
	}
}

func TestTranslator_LogAtOrAboveSeverityEmits(t *testing.T) {
	em := &captureEmitter{}
	cfg := defaultCfg()
	cfg.Redaction = []*regexp.Regexp{regexp.MustCompile(`[\w.+-]+@[\w.-]+`)}
	tr := NewTranslator(cfg, em)

	rls := []*logspb.ResourceLogs{{
		Resource: k8sResource(),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
			SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "failed for user@example.com"}},
			TraceId:        []byte{0x01, 0x02},
			SpanId:         []byte{0x03, 0x04},
		}}}},
	}}

	n := tr.TranslateResourceLogs(rls)
	if n != 1 {
		t.Fatalf("expected 1 emitted log, got %d", n)
	}
	evt, ok := em.all()[0].(watcher.OTelLogMatchEvent)
	if !ok {
		t.Fatalf("expected OTelLogMatchEvent, got %T", em.all()[0])
	}
	if evt.Severity != "ERROR" {
		t.Errorf("severity text = %q, want ERROR", evt.Severity)
	}
	if evt.Body == "failed for user@example.com" {
		t.Errorf("body not redacted: %q", evt.Body)
	}
	if evt.BodyHash == "" {
		t.Errorf("body hash should be non-empty")
	}
	if evt.TraceID == "" || evt.SpanID == "" {
		t.Errorf("trace/span IDs not hex encoded")
	}
}

func TestTranslator_LogsDedupedAndCappedPerRequest(t *testing.T) {
	em := &captureEmitter{}
	cfg := defaultCfg()
	cfg.LogFilters.MaxSignalsPerRequest = 2
	tr := NewTranslator(cfg, em)

	rls := []*logspb.ResourceLogs{{
		Resource: k8sResource(),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
			{
				SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "same failure"}},
				TraceId:        []byte{0x01},
			},
			{
				SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "same failure"}},
				TraceId:        []byte{0x02},
			},
			{
				SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "second failure"}},
				TraceId:        []byte{0x03},
			},
			{
				SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "third failure"}},
				TraceId:        []byte{0x04},
			},
		}}},
	}}

	n := tr.TranslateResourceLogs(rls)
	if n != 2 {
		t.Fatalf("emitted = %d, want 2 unique capped signals", n)
	}
	if got := len(em.all()); got != 2 {
		t.Fatalf("captured events = %d, want 2", got)
	}
}

// ---- small utility tests -----------------------------------------------

func TestSeverityNumberForText(t *testing.T) {
	cases := map[string]int32{
		"TRACE": 1, "DEBUG": 5, "INFO": 9, "WARN": 13, "WARNING": 13,
		"ERROR": 17, "FATAL": 21, "": 0, "bogus": 0,
	}
	for in, want := range cases {
		if got := severityNumberForText(in); got != want {
			t.Errorf("severityNumberForText(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSeverityTextFromNumber(t *testing.T) {
	if got := severityTextFromNumber(17, "WARN"); got != "WARN" {
		t.Errorf("given text should win, got %q", got)
	}
	if got := severityTextFromNumber(17, ""); got != "ERROR" {
		t.Errorf("17 => ERROR, got %q", got)
	}
	if got := severityTextFromNumber(0, ""); got != "" {
		t.Errorf("0 => empty, got %q", got)
	}
}

func TestStatusCodeText(t *testing.T) {
	if statusCodeText(nil) != "STATUS_CODE_UNSET" {
		t.Error("nil status should be UNSET")
	}
	if statusCodeText(&tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}) != "STATUS_CODE_OK" {
		t.Error("OK mapping")
	}
	if statusCodeText(&tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}) != "STATUS_CODE_ERROR" {
		t.Error("ERROR mapping")
	}
}

func TestSpanKindText(t *testing.T) {
	if spanKindText(tracepb.Span_SPAN_KIND_CLIENT) != "CLIENT" {
		t.Error("CLIENT")
	}
	if spanKindText(tracepb.Span_SPAN_KIND_SERVER) != "SERVER" {
		t.Error("SERVER")
	}
	if spanKindText(tracepb.Span_SPAN_KIND_UNSPECIFIED) != "UNSPECIFIED" {
		t.Error("UNSPECIFIED")
	}
}

func TestApplyConfigDefaults_FillsZeroFields(t *testing.T) {
	cfg := Config{}
	applyConfigDefaults(&cfg)
	if cfg.BindAddress == "" || cfg.ReadTimeout == 0 || cfg.MaxRequestBytes == 0 || cfg.AgentName == "" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestApplyConfigDefaults_PreservesSetFields(t *testing.T) {
	cfg := Config{BindAddress: ":5555", ReadTimeout: 3 * time.Second, AgentName: "x"}
	applyConfigDefaults(&cfg)
	if cfg.BindAddress != ":5555" || cfg.ReadTimeout != 3*time.Second || cfg.AgentName != "x" {
		t.Fatalf("overrides clobbered: %+v", cfg)
	}
}
