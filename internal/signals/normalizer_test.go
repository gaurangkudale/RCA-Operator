package signals

import (
	"testing"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func TestNormalize_PopulatesTraceIDForOTelEvents(t *testing.T) {
	const traceID = "abcdef0123456789abcdef0123456789"
	base := watcher.BaseEvent{
		At:        time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
		AgentName: "ag",
		Namespace: "dev",
		PodName:   "svc-abc",
	}

	cases := []struct {
		name  string
		event watcher.CorrelatorEvent
	}{
		{"OTelSpanError", watcher.OTelSpanErrorEvent{BaseEvent: base, TraceID: traceID, SpanID: "111"}},
		{"OTelSpanLatencySpike", watcher.OTelSpanLatencySpikeEvent{BaseEvent: base, TraceID: traceID, SpanID: "111"}},
		{"OTelLogMatch", watcher.OTelLogMatchEvent{BaseEvent: base, TraceID: traceID, ServiceName: "svc", BodyHash: "h"}},
		{"OTelSpanEvent", watcher.OTelSpanEventEvent{BaseEvent: base, TraceID: traceID, SpanID: "111", EventName: "exception"}},
	}

	n := NewNormalizer(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, ok := n.Normalize(tc.event)
			if !ok {
				t.Fatalf("expected normalize to succeed for %s", tc.name)
			}
			if sig.TraceID != traceID {
				t.Errorf("TraceID = %q, want %q", sig.TraceID, traceID)
			}
		})
	}
}

func TestNormalize_NoTraceIDForKubernetesEvents(t *testing.T) {
	base := watcher.BaseEvent{
		At:        time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
		AgentName: "ag",
		Namespace: "dev",
		PodName:   "svc-abc",
	}

	cases := []watcher.CorrelatorEvent{
		watcher.CrashLoopBackOffEvent{BaseEvent: base, RestartCount: 4, Threshold: 3},
		watcher.OOMKilledEvent{BaseEvent: base, ContainerName: "app", ExitCode: 137},
		watcher.ImagePullBackOffEvent{BaseEvent: base, ContainerName: "app", Reason: "ErrImagePull"},
	}

	n := NewNormalizer(nil)
	for _, ev := range cases {
		sig, ok := n.Normalize(ev)
		if !ok {
			t.Fatalf("expected normalize to succeed for %s", ev.Type())
		}
		if sig.TraceID != "" {
			t.Errorf("TraceID should be empty for %s, got %q", ev.Type(), sig.TraceID)
		}
	}
}

func TestNormalize_OTelLogMatch_UsesServiceScopeWithoutPod(t *testing.T) {
	n := NewNormalizer(nil)
	event := watcher.OTelLogMatchEvent{
		BaseEvent: watcher.BaseEvent{
			At:        time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
			AgentName: "ag",
			Namespace: "",
			PodName:   "",
		},
		ServiceName: "proxy-service",
		TraceID:     "abcdef0123456789abcdef0123456789",
		BodyHash:    "hash",
		ResourceAttrs: map[string]string{
			"service.namespace": "rca-demo",
		},
	}

	sig, ok := n.Normalize(event)
	if !ok {
		t.Fatal("expected normalize to succeed")
	}
	if sig.Namespace != "rca-demo" {
		t.Fatalf("Namespace = %q, want %q", sig.Namespace, "rca-demo")
	}
	if sig.Scope.Level != incident.ScopeLevelWorkload {
		t.Fatalf("Scope.Level = %q, want %q", sig.Scope.Level, incident.ScopeLevelWorkload)
	}
	if sig.Scope.WorkloadRef == nil {
		t.Fatal("Scope.WorkloadRef should be populated for OTel service events")
	}
	if sig.Scope.WorkloadRef.Kind != "Service" || sig.Scope.WorkloadRef.Name != "proxy-service" {
		t.Fatalf("unexpected workload ref: kind=%q name=%q", sig.Scope.WorkloadRef.Kind, sig.Scope.WorkloadRef.Name)
	}
	if len(sig.AffectedResources) != 1 {
		t.Fatalf("AffectedResources length = %d, want 1", len(sig.AffectedResources))
	}
	if sig.AffectedResources[0] != (rcav1alpha1.AffectedResource{APIVersion: "v1", Kind: "Service", Namespace: "rca-demo", Name: "proxy-service"}) {
		t.Fatalf("unexpected affected resource: %#v", sig.AffectedResources[0])
	}
}

func TestNormalize_OTelSpanError_PrefersDeploymentResourceAttr(t *testing.T) {
	n := NewNormalizer(nil)
	event := watcher.OTelSpanErrorEvent{
		BaseEvent: watcher.BaseEvent{
			At:        time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
			AgentName: "ag",
			Namespace: "",
			PodName:   "",
		},
		ServiceName: "payment-service",
		TraceID:     "abcdef0123456789abcdef0123456789",
		SpanID:      "span-1",
		ResourceAttrs: map[string]string{
			"k8s.namespace.name":  "rca-demo",
			"k8s.deployment.name": "payment-service",
		},
	}

	sig, ok := n.Normalize(event)
	if !ok {
		t.Fatal("expected normalize to succeed")
	}
	if sig.Scope.WorkloadRef == nil {
		t.Fatal("Scope.WorkloadRef should be populated")
	}
	if sig.Scope.WorkloadRef.Kind != "Deployment" || sig.Scope.WorkloadRef.Name != "payment-service" {
		t.Fatalf("unexpected workload ref: kind=%q name=%q", sig.Scope.WorkloadRef.Kind, sig.Scope.WorkloadRef.Name)
	}
}

func TestNormalize_OTelSpanError_DropsClientSpans(t *testing.T) {
	n := NewNormalizer(nil)
	event := watcher.OTelSpanErrorEvent{
		BaseEvent: watcher.BaseEvent{
			At:        time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC),
			AgentName: "ag",
			Namespace: "rca-demo",
			PodName:   "load-tester-abc",
		},
		TraceID:    "abcdef0123456789abcdef0123456789",
		SpanID:     "span-client",
		SpanKind:   "CLIENT",
		StatusCode: "STATUS_CODE_ERROR",
	}

	if _, ok := n.Normalize(event); ok {
		t.Fatal("expected CLIENT span error to be ignored")
	}
}
