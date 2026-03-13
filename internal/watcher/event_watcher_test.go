package watcher

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func makeK8sEvent(namespace, name, reason, message, objKind, objName, objUID string, ts time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      objKind,
			Name:      objName,
			Namespace: namespace,
			UID:       types.UID(objUID),
		},
		Reason:        reason,
		Message:       message,
		LastTimestamp: metav1.NewTime(ts),
		Source:        corev1.EventSource{Host: "node-1"},
	}
}

func newTestEventWatcher(namespaces []string, dedupWindow time.Duration) (*EventWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewEventWatcher(nil, em, logr.Discard(), EventWatcherConfig{
		AgentName:       "agent-test",
		WatchNamespaces: namespaces,
		DedupWindow:     dedupWindow,
	})
	return w, em
}

// ── Test 19: each Reason → correct event type ────────────────────────────────

func TestEventWatcher_ReasonRouting(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		reason       string
		objKind      string
		message      string
		wantType     EventType
		wantNonEmpty bool
	}{
		{
			name:         "OOMKilling on pod emits OOMKilledEvent",
			reason:       reasonOOMKilling,
			objKind:      "Pod",
			message:      "Memory limit exceeded",
			wantType:     EventTypeOOMKilled,
			wantNonEmpty: true,
		},
		{
			name:         "Evicted on pod emits PodEvictedEvent",
			reason:       reasonEvicted,
			objKind:      "Pod",
			message:      "The node was low on resource: memory",
			wantType:     EventTypePodEvicted,
			wantNonEmpty: true,
		},
		{
			name:         "Unhealthy on pod emits ProbeFailureEvent",
			reason:       reasonUnhealthy,
			objKind:      "Pod",
			message:      "Liveness probe failed: HTTP probe failed",
			wantType:     EventTypeProbeFailure,
			wantNonEmpty: true,
		},
		{
			name:         "NodeNotReady on node emits NodeNotReadyEvent",
			reason:       reasonNodeNotReady,
			objKind:      "Node",
			message:      "Node condition changed",
			wantType:     EventTypeNodeNotReady,
			wantNonEmpty: true,
		},
		{
			name:         "NodeConditionChanged on node emits NodeNotReadyEvent",
			reason:       reasonNodeConditionChanged,
			objKind:      "Node",
			message:      "Node condition changed",
			wantType:     EventTypeNodeNotReady,
			wantNonEmpty: true,
		},
		{
			name:         "Unknown reason emits nothing",
			reason:       "SomeOtherReason",
			objKind:      "Pod",
			message:      "irrelevant",
			wantNonEmpty: false,
		},
		{
			name:         "OOMKilling on non-Pod kind emits nothing",
			reason:       reasonOOMKilling,
			objKind:      "Node",
			message:      "OOM on node",
			wantNonEmpty: false,
		},
		{
			name:         "CPUThrottlingHigh on pod emits CPUThrottlingEvent",
			reason:       reasonCPUThrottlingHigh,
			objKind:      "Pod",
			message:      "25% throttling of CPU in namespace default",
			wantType:     EventTypeCPUThrottling,
			wantNonEmpty: true,
		},
		{
			name:         "CPUThrottlingHigh on non-Pod kind emits nothing",
			reason:       reasonCPUThrottlingHigh,
			objKind:      "Node",
			message:      "25% throttling",
			wantNonEmpty: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, em := newTestEventWatcher(nil, time.Hour)
			w.clock = func() time.Time { return now }

			ev := makeK8sEvent("default", "ev-1", tc.reason, tc.message,
				tc.objKind, "target-obj", "obj-uid-1", now)
			w.route(ev)

			if tc.wantNonEmpty {
				if len(em.events) != 1 {
					t.Fatalf("expected 1 event, got %d", len(em.events))
				}
				if em.events[0].Type() != tc.wantType {
					t.Errorf("event type: got %q, want %q", em.events[0].Type(), tc.wantType)
				}
			} else {
				if len(em.events) != 0 {
					t.Errorf("expected no events, got %d", len(em.events))
				}
			}
		})
	}
}

// ── Test 20: same event within dedup window → only 1 emit ────────────────────

func TestEventWatcher_DedupWithinWindow(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, em := newTestEventWatcher(nil, 2*time.Minute)
	w.clock = func() time.Time { return now }

	ev := makeK8sEvent("default", "ev-1", reasonEvicted, "oom eviction",
		"Pod", "my-pod", "pod-uid-1", now)

	w.route(ev) // first → should emit
	w.route(ev) // second within window → should be suppressed
	w.route(ev) // third within window → should be suppressed

	if len(em.events) != 1 {
		t.Errorf("dedup within window: expected 1 event, got %d", len(em.events))
	}
}

// ── Test 21: same event after window expires → 2nd emit goes through ─────────

func TestEventWatcher_DedupWindowExpiry(t *testing.T) {
	base := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	tick := base
	w, em := newTestEventWatcher(nil, 2*time.Minute)
	w.clock = func() time.Time { return tick }

	ev := makeK8sEvent("default", "ev-1", reasonEvicted, "oom eviction",
		"Pod", "my-pod", "pod-uid-1", base)

	w.route(ev) // T+0 → emits

	tick = base.Add(3 * time.Minute) // advance past dedup window
	w.route(ev)                      // T+3min → should emit again

	if len(em.events) != 2 {
		t.Errorf("after window expiry: expected 2 events, got %d", len(em.events))
	}
}

// ── Test 22: event in unwatched namespace → nothing emitted ──────────────────

func TestEventWatcher_NamespaceFilter(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, em := newTestEventWatcher([]string{"production"}, time.Hour)
	w.clock = func() time.Time { return now }

	ev := makeK8sEvent("staging", "ev-1", reasonEvicted, "evicted",
		"Pod", "my-pod", "pod-uid-1", now)
	w.onEventAdd(ev)

	if len(em.events) != 0 {
		t.Errorf("event from unwatched namespace: expected 0 events, got %d", len(em.events))
	}
}

// ── Test 23: bootstrap scan replays events within 5-min window ───────────────

func TestEventWatcher_BootstrapReplay(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, em := newTestEventWatcher(nil, time.Hour)
	w.clock = func() time.Time { return now }

	recentEv := makeK8sEvent("default", "recent", reasonEvicted, "evicted",
		"Pod", "pod-a", "pod-uid-a", now.Add(-2*time.Minute))
	oldEv := makeK8sEvent("default", "old", reasonEvicted, "evicted",
		"Pod", "pod-b", "pod-uid-b", now.Add(-10*time.Minute))

	// Simulate bootstrap by directly calling route (bootstrapScan uses cache.List
	// which requires a running envtest; here we test the filtering logic directly).
	cutoff := w.clock().Add(-bootstrapLookback)
	for _, ev := range []*corev1.Event{recentEv, oldEv} {
		ts := eventTimestamp(ev, w.clock())
		if !ts.Before(cutoff) {
			w.route(ev)
		}
	}

	if len(em.events) != 1 {
		t.Errorf("bootstrap: expected 1 replayed event (recent only), got %d", len(em.events))
	}
	if em.events[0].Type() != EventTypePodEvicted {
		t.Errorf("bootstrap: expected PodEvicted, got %s", em.events[0].Type())
	}
}

// ── additional unit tests for helper functions ────────────────────────────────

func TestParseProbeType(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"Liveness probe failed: HTTP probe failed with statuscode: 500", "Liveness"},
		{"Readiness probe failed: Get http://...: connection refused", "Readiness"},
		{"Startup probe failed: command returned 1", "Startup"},
		{"liveness probe failed (lowercase)", "Liveness"},
		{"readiness probe failed", "Readiness"},
		{"Some other message", "Unknown"},
		{"", "Unknown"},
	}
	for _, tc := range cases {
		got := parseProbeType(tc.message)
		if got != tc.want {
			t.Errorf("parseProbeType(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestEventTimestamp_Precedence(t *testing.T) {
	fallback := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eventTime := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	lastTime := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	firstTime := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)

	ev := &corev1.Event{}
	if got := eventTimestamp(ev, fallback); !got.Equal(fallback) {
		t.Errorf("all-zero: want fallback, got %v", got)
	}

	ev.FirstTimestamp = metav1.NewTime(firstTime)
	if got := eventTimestamp(ev, fallback); !got.Equal(firstTime) {
		t.Errorf("only FirstTimestamp: got %v, want %v", got, firstTime)
	}

	ev.LastTimestamp = metav1.NewTime(lastTime)
	if got := eventTimestamp(ev, fallback); !got.Equal(lastTime) {
		t.Errorf("LastTimestamp set: got %v, want %v", got, lastTime)
	}

	ev.EventTime = metav1.NewMicroTime(eventTime)
	if got := eventTimestamp(ev, fallback); !got.Equal(eventTime) {
		t.Errorf("EventTime set (highest precedence): got %v, want %v", got, eventTime)
	}
}

func TestSweepDedupMap(t *testing.T) {
	base := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, _ := newTestEventWatcher(nil, 2*time.Minute)
	w.clock = func() time.Time { return base }

	// Seed the map with an old entry and a fresh entry.
	w.dedupSeen["old-key"] = base.Add(-25 * time.Minute)  // older than 2×DedupWindow
	w.dedupSeen["fresh-key"] = base.Add(-1 * time.Minute) // within window

	w.sweepDedupMap(context.Background())

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.dedupSeen["old-key"]; ok {
		t.Error("sweepDedupMap: old-key should have been removed")
	}
	if _, ok := w.dedupSeen["fresh-key"]; !ok {
		t.Error("sweepDedupMap: fresh-key should have been retained")
	}
}

func TestParseContainerFromFieldPath(t *testing.T) {
	cases := []struct {
		fieldPath string
		want      string
	}{
		{"spec.containers{app}", "app"},
		{"spec.containers{sidecar-proxy}", "sidecar-proxy"},
		{"spec.initContainers{init-db}", "init-db"},
		{"", ""},
		{"spec.containers", ""},
		{"nobraces", ""},
	}
	for _, tc := range cases {
		got := parseContainerFromFieldPath(tc.fieldPath)
		if got != tc.want {
			t.Errorf("parseContainerFromFieldPath(%q) = %q, want %q", tc.fieldPath, got, tc.want)
		}
	}
}
