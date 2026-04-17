package signals

import (
	"testing"
	"time"

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
			if sig.Input.TraceID != traceID {
				t.Errorf("TraceID = %q, want %q", sig.Input.TraceID, traceID)
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
		if sig.Input.TraceID != "" {
			t.Errorf("TraceID should be empty for %s, got %q", ev.Type(), sig.Input.TraceID)
		}
	}
}
