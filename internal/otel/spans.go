package otel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "rca-operator"

// Tracer returns the package-level OTel tracer. Safe to call even when OTel is
// disabled — the global no-op tracer is returned.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// StartReconcileSpan starts a span covering a single reconcile loop iteration.
func StartReconcileSpan(ctx context.Context, kind, name, namespace string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "Reconcile",
		trace.WithAttributes(
			attribute.String("k8s.resource.kind", kind),
			attribute.String("k8s.resource.name", name),
			attribute.String("k8s.namespace", namespace),
		),
	)
}

// StartSignalSpan starts a span for processing a single signal event.
func StartSignalSpan(ctx context.Context, eventType, namespace, pod string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "ProcessSignal",
		trace.WithAttributes(
			attribute.String("rca.event_type", eventType),
			attribute.String("k8s.namespace", namespace),
			attribute.String("k8s.pod.name", pod),
		),
	)
}

// StartRuleSpan starts a span for a single rule evaluation.
func StartRuleSpan(ctx context.Context, ruleName string, priority int) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "EvaluateRule",
		trace.WithAttributes(
			attribute.String("rca.rule.name", ruleName),
			attribute.Int("rca.rule.priority", priority),
		),
	)
}

// StartIncidentSpan starts a span for incident creation or update.
func StartIncidentSpan(ctx context.Context, incidentType, fingerprint string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "EnsureIncident",
		trace.WithAttributes(
			attribute.String("rca.incident_type", incidentType),
			attribute.String("rca.fingerprint", fingerprint),
		),
	)
}

// TraceIDFromContext returns the W3C trace-id hex string from the current span,
// or "" if tracing is not active.
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.HasTraceID() {
		return sc.TraceID().String()
	}
	return ""
}
