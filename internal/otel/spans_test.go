package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installRecorder replaces the global OTel TracerProvider with an in-memory
// recording provider and returns the recorder plus a cleanup function.
func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// attrValue returns the string value of the first attribute matching key, or "".
func attrValue(attrs []attribute.KeyValue, key string) string {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}

func attrInt(attrs []attribute.KeyValue, key string) int64 {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value.AsInt64()
		}
	}
	return 0
}

// ─────────────────────────────────────────────────────────────────────────────
// Tracer()
// ─────────────────────────────────────────────────────────────────────────────

func TestTracer_ReturnsNonNil(t *testing.T) {
	tr := Tracer()
	if tr == nil {
		t.Fatal("Tracer() returned nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StartReconcileSpan
// ─────────────────────────────────────────────────────────────────────────────

func TestStartReconcileSpan_AttributesAndName(t *testing.T) {
	rec := installRecorder(t)

	ctx, span := StartReconcileSpan(context.Background(), "IncidentReport", "incident-abc", "default")
	span.End()

	_ = ctx // ctx carries the span for downstream propagation
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "Reconcile" {
		t.Errorf("span name: got %q, want %q", s.Name(), "Reconcile")
	}
	if got := attrValue(s.Attributes(), "k8s.resource.kind"); got != "IncidentReport" {
		t.Errorf("k8s.resource.kind: got %q", got)
	}
	if got := attrValue(s.Attributes(), "k8s.resource.name"); got != "incident-abc" {
		t.Errorf("k8s.resource.name: got %q", got)
	}
	if got := attrValue(s.Attributes(), "k8s.namespace"); got != "default" {
		t.Errorf("k8s.namespace: got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StartSignalSpan
// ─────────────────────────────────────────────────────────────────────────────

func TestStartSignalSpan_AttributesAndName(t *testing.T) {
	rec := installRecorder(t)

	_, span := StartSignalSpan(context.Background(), "CrashLoop", "production", "my-pod-abc")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "ProcessSignal" {
		t.Errorf("span name: got %q, want %q", s.Name(), "ProcessSignal")
	}
	if got := attrValue(s.Attributes(), "rca.event_type"); got != "CrashLoop" {
		t.Errorf("rca.event_type: got %q", got)
	}
	if got := attrValue(s.Attributes(), "k8s.namespace"); got != "production" {
		t.Errorf("k8s.namespace: got %q", got)
	}
	if got := attrValue(s.Attributes(), "k8s.pod.name"); got != "my-pod-abc" {
		t.Errorf("k8s.pod.name: got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StartRuleSpan
// ─────────────────────────────────────────────────────────────────────────────

func TestStartRuleSpan_AttributesAndName(t *testing.T) {
	rec := installRecorder(t)

	_, span := StartRuleSpan(context.Background(), "crashloop-plus-oom", 10)
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "EvaluateRule" {
		t.Errorf("span name: got %q, want %q", s.Name(), "EvaluateRule")
	}
	if got := attrValue(s.Attributes(), "rca.rule.name"); got != "crashloop-plus-oom" {
		t.Errorf("rca.rule.name: got %q", got)
	}
	if got := attrInt(s.Attributes(), "rca.rule.priority"); got != 10 {
		t.Errorf("rca.rule.priority: got %d, want 10", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// StartIncidentSpan
// ─────────────────────────────────────────────────────────────────────────────

func TestStartIncidentSpan_AttributesAndName(t *testing.T) {
	rec := installRecorder(t)

	_, span := StartIncidentSpan(context.Background(), "OOMKilled", "fp-abc123")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 ended span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "EnsureIncident" {
		t.Errorf("span name: got %q, want %q", s.Name(), "EnsureIncident")
	}
	if got := attrValue(s.Attributes(), "rca.incident_type"); got != "OOMKilled" {
		t.Errorf("rca.incident_type: got %q", got)
	}
	if got := attrValue(s.Attributes(), "rca.fingerprint"); got != "fp-abc123" {
		t.Errorf("rca.fingerprint: got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TraceIDFromContext
// ─────────────────────────────────────────────────────────────────────────────

func TestTraceIDFromContext_WithActiveSpan(t *testing.T) {
	installRecorder(t)

	ctx, span := StartReconcileSpan(context.Background(), "IncidentReport", "ir-1", "ns")
	defer span.End()

	id := TraceIDFromContext(ctx)
	if id == "" {
		t.Error("expected non-empty trace ID from active span context")
	}
	if len(id) != 32 {
		t.Errorf("W3C trace ID should be 32 hex chars, got %d: %q", len(id), id)
	}
}

func TestTraceIDFromContext_NoSpan(t *testing.T) {
	// With no active span the global no-op tracer returns an empty trace ID.
	id := TraceIDFromContext(context.Background())
	if id != "" {
		t.Errorf("expected empty trace ID with no active span, got %q", id)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Span context propagation
// ─────────────────────────────────────────────────────────────────────────────

// TestSpanContextPropagation verifies that a child span created from a
// parent-span context shares the same trace ID.
func TestSpanContextPropagation(t *testing.T) {
	installRecorder(t)

	parentCtx, parentSpan := StartReconcileSpan(context.Background(), "IncidentReport", "ir-1", "ns")
	parentID := TraceIDFromContext(parentCtx)
	parentSpan.End()

	childCtx, childSpan := StartSignalSpan(parentCtx, "CrashLoop", "ns", "pod-1")
	childID := TraceIDFromContext(childCtx)
	childSpan.End()

	if parentID == "" || childID == "" {
		t.Fatal("both parent and child should have non-empty trace IDs")
	}
	if parentID != childID {
		t.Errorf("child span trace ID %q should equal parent trace ID %q", childID, parentID)
	}
}
