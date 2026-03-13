package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestNodeWatcher() (*NodeWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewNodeWatcher(nil, em, logr.Discard(), NodeWatcherConfig{
		AgentName:         "agent-test",
		IncidentNamespace: "production",
	})
	return w, em
}

// nodeWithConditions builds a Node with the given conditions.
func nodeWithConditions(name, uid string, conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Status:     corev1.NodeStatus{Conditions: conditions},
	}
}

// readyCondition returns the Ready condition with the given status.
func readyCondition(status corev1.ConditionStatus, reason, message string, at time.Time) corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:               corev1.NodeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(at),
	}
}

// pressureCondition returns a pressure condition (DiskPressure/MemoryPressure/PIDPressure).
func pressureCondition(condType corev1.NodeConditionType, status corev1.ConditionStatus, message string, at time.Time) corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:               condType,
		Status:             status,
		Message:            message,
		LastTransitionTime: metav1.NewTime(at),
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestNodeWatcher_NotReady_EmitsNodeNotReadyEvent verifies that a Node with
// Ready=False causes a NodeNotReadyEvent to be emitted.
func TestNodeWatcher_NotReady_EmitsNodeNotReadyEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-1", "uid-1",
		readyCondition(corev1.ConditionFalse, "KubeletNotReady", "kubelet stopped posting", now),
	)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(NodeNotReadyEvent)
	if !ok {
		t.Fatalf("expected NodeNotReadyEvent, got %T", em.events[0])
	}
	if ev.NodeName != "node-1" {
		t.Errorf("NodeName: want %q, got %q", "node-1", ev.NodeName)
	}
	if ev.Namespace != "production" {
		t.Errorf("Namespace: want %q, got %q", "production", ev.Namespace)
	}
	if ev.Reason != "KubeletNotReady" {
		t.Errorf("Reason: want %q, got %q", "KubeletNotReady", ev.Reason)
	}
	if !ev.At.Equal(now) {
		t.Errorf("At: want %v, got %v", now, ev.At)
	}
}

// TestNodeWatcher_NotReady_Unknown_EmitsEvent verifies that Ready=Unknown
// (as set when the node-lifecycle-controller marks it unknown) also fires.
func TestNodeWatcher_NotReady_Unknown_EmitsEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 1, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-2", "uid-2",
		readyCondition(corev1.ConditionUnknown, "NodeStatusUnknown", "node controller lost contact", now),
	)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event for Unknown, got %d", len(em.events))
	}
	if _, ok := em.events[0].(NodeNotReadyEvent); !ok {
		t.Fatalf("expected NodeNotReadyEvent, got %T", em.events[0])
	}
}

// TestNodeWatcher_NotReady_DedupSuppressesRepeat verifies that the same NotReady
// condition fires exactly once even when checkConditions is called multiple times.
func TestNodeWatcher_NotReady_DedupSuppressesRepeat(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 2, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-3", "uid-3",
		readyCondition(corev1.ConditionFalse, "KubeletNotReady", "stopped", now),
	)

	w.checkConditions(node)
	w.checkConditions(node)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected exactly 1 event (dedup), got %d", len(em.events))
	}
}

// TestNodeWatcher_NotReady_RecoveryClearsDedup verifies that when a Node recovers
// (Ready=True) the dedup record is cleared, allowing the next failure to fire again.
func TestNodeWatcher_NotReady_RecoveryClearsDedup(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 3, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	uid := "uid-recover"

	// First failure: event fires.
	broken := nodeWithConditions("node-4", uid,
		readyCondition(corev1.ConditionFalse, "KubeletNotReady", "down", now),
	)
	w.checkConditions(broken)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 event after first failure, got %d", len(em.events))
	}

	// Repeat failure: deduped.
	w.checkConditions(broken)
	if len(em.events) != 1 {
		t.Fatalf("expected still 1 event (dedup), got %d", len(em.events))
	}

	// Recovery: Ready=True clears the gate (no event emitted for recovery).
	healthy := nodeWithConditions("node-4", uid,
		readyCondition(corev1.ConditionTrue, "KubeletReady", "ok", now.Add(5*time.Minute)),
	)
	w.checkConditions(healthy)
	if len(em.events) != 1 {
		t.Fatalf("recovery should not emit; expected still 1 event, got %d", len(em.events))
	}

	// Second failure: gate is clear; fires again.
	w.checkConditions(broken)
	if len(em.events) != 2 {
		t.Fatalf("expected 2nd event after recovery+failure, got %d", len(em.events))
	}
}

// TestNodeWatcher_DiskPressure_EmitsNodePressureEvent verifies that DiskPressure=True
// causes a NodePressureEvent with PressureType="DiskPressure".
func TestNodeWatcher_DiskPressure_EmitsNodePressureEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 10, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-5", "uid-5",
		pressureCondition(corev1.NodeDiskPressure, corev1.ConditionTrue, "disk full", now),
	)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(NodePressureEvent)
	if !ok {
		t.Fatalf("expected NodePressureEvent, got %T", em.events[0])
	}
	if ev.PressureType != "DiskPressure" {
		t.Errorf("PressureType: want %q, got %q", "DiskPressure", ev.PressureType)
	}
	if ev.NodeName != "node-5" {
		t.Errorf("NodeName: want %q, got %q", "node-5", ev.NodeName)
	}
	if ev.Message != "disk full" {
		t.Errorf("Message: want %q, got %q", "disk full", ev.Message)
	}
	if ev.AgentName != "agent-test" {
		t.Errorf("AgentName: want %q, got %q", "agent-test", ev.AgentName)
	}
}

// TestNodeWatcher_MemoryPressure_EmitsNodePressureEvent verifies MemoryPressure detection.
func TestNodeWatcher_MemoryPressure_EmitsNodePressureEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 11, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-6", "uid-6",
		pressureCondition(corev1.NodeMemoryPressure, corev1.ConditionTrue, "low memory", now),
	)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(NodePressureEvent)
	if !ok {
		t.Fatalf("expected NodePressureEvent, got %T", em.events[0])
	}
	if ev.PressureType != "MemoryPressure" {
		t.Errorf("PressureType: want %q, got %q", "MemoryPressure", ev.PressureType)
	}
}

// TestNodeWatcher_PIDPressure_EmitsNodePressureEvent verifies PIDPressure detection.
func TestNodeWatcher_PIDPressure_EmitsNodePressureEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 12, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-7", "uid-7",
		pressureCondition(corev1.NodePIDPressure, corev1.ConditionTrue, "pid table full", now),
	)
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(NodePressureEvent)
	if !ok {
		t.Fatalf("expected NodePressureEvent, got %T", em.events[0])
	}
	if ev.PressureType != "PIDPressure" {
		t.Errorf("PressureType: want %q, got %q", "PIDPressure", ev.PressureType)
	}
}

// TestNodeWatcher_MultiplePressuresFireIndependently verifies that DiskPressure and
// MemoryPressure on the same node each fire their own event, independently deduped.
func TestNodeWatcher_MultiplePressuresFireIndependently(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 15, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-8", "uid-8",
		pressureCondition(corev1.NodeDiskPressure, corev1.ConditionTrue, "disk full", now),
		pressureCondition(corev1.NodeMemoryPressure, corev1.ConditionTrue, "low memory", now),
	)
	w.checkConditions(node)

	if len(em.events) != 2 {
		t.Fatalf("expected 2 independent pressure events, got %d", len(em.events))
	}

	// Verify each event type is present.
	types := map[string]bool{}
	for _, ev := range em.events {
		if p, ok := ev.(NodePressureEvent); ok {
			types[p.PressureType] = true
		}
	}
	if !types["DiskPressure"] {
		t.Error("DiskPressure event missing")
	}
	if !types["MemoryPressure"] {
		t.Error("MemoryPressure event missing")
	}
}

// TestNodeWatcher_PressureRecovery_ClearsThenRefires verifies that a pressure
// condition that resolves (False) and then reactivates (True) fires a second event.
func TestNodeWatcher_PressureRecovery_ClearsThenRefires(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 20, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	uid := "uid-pres-recover"

	active := nodeWithConditions("node-9", uid,
		pressureCondition(corev1.NodeDiskPressure, corev1.ConditionTrue, "disk full", now),
	)
	w.checkConditions(active)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 event on activation, got %d", len(em.events))
	}

	// Dedup — same condition, no new event.
	w.checkConditions(active)
	if len(em.events) != 1 {
		t.Fatalf("expected still 1 event (dedup), got %d", len(em.events))
	}

	// Resolved: DiskPressure=False.
	resolved := nodeWithConditions("node-9", uid,
		pressureCondition(corev1.NodeDiskPressure, corev1.ConditionFalse, "", now.Add(10*time.Minute)),
	)
	w.checkConditions(resolved)
	if len(em.events) != 1 {
		t.Fatalf("resolution should not emit; expected still 1 event, got %d", len(em.events))
	}

	// Pressure picks up again — gate is clear; fires.
	w.checkConditions(active)
	if len(em.events) != 2 {
		t.Fatalf("expected 2nd event after recovery+reactivation, got %d", len(em.events))
	}
}

// TestNodeWatcher_HealthyNode_NoEvent verifies that a node with all conditions
// healthy produces no events.
func TestNodeWatcher_HealthyNode_NoEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 25, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return now }

	node := nodeWithConditions("node-10", "uid-10",
		readyCondition(corev1.ConditionTrue, "KubeletReady", "ok", now),
		pressureCondition(corev1.NodeDiskPressure, corev1.ConditionFalse, "", now),
		pressureCondition(corev1.NodeMemoryPressure, corev1.ConditionFalse, "", now),
		pressureCondition(corev1.NodePIDPressure, corev1.ConditionFalse, "", now),
	)
	w.checkConditions(node)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for fully healthy node, got %d", len(em.events))
	}
}

// TestNodeWatcher_EmptyConditions_NoEvent verifies that a Node with no conditions
// (e.g. freshly registered) does not cause a panic or spurious event.
func TestNodeWatcher_EmptyConditions_NoEvent(t *testing.T) {
	w, em := newTestNodeWatcher()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-new", UID: "uid-new"},
	}
	w.checkConditions(node)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for node with no conditions, got %d", len(em.events))
	}
}

// TestNodeWatcher_FallbackTimestamp_UsesClockWhenCondTimeIsZero verifies that a
// zero-valued LastTransitionTime falls back to the watcher's clock.
func TestNodeWatcher_FallbackTimestamp_UsesClockWhenCondTimeIsZero(t *testing.T) {
	fallback := time.Date(2026, 3, 12, 11, 0, 0, 0, time.UTC)
	w, em := newTestNodeWatcher()
	w.clock = func() time.Time { return fallback }

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-ts", UID: "uid-ts"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:    corev1.NodeReady,
					Status:  corev1.ConditionFalse,
					Reason:  "KubeletNotReady",
					Message: "stopped",
					// LastTransitionTime intentionally zero.
				},
			},
		},
	}
	w.checkConditions(node)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev := em.events[0].(NodeNotReadyEvent)
	if !ev.At.Equal(fallback) {
		t.Errorf("At: want fallback clock %v, got %v", fallback, ev.At)
	}
}

// TestNodeWatcher_IncidentNamespaceDefault verifies that an empty IncidentNamespace
// is defaulted to "default" by the constructor.
func TestNodeWatcher_IncidentNamespaceDefault(t *testing.T) {
	em := &recordingEmitter{}
	w := NewNodeWatcher(nil, em, logr.Discard(), NodeWatcherConfig{
		AgentName:         "agent",
		IncidentNamespace: "", // intentionally empty — should default
	})

	if w.config.IncidentNamespace != "default" {
		t.Errorf("IncidentNamespace default: want %q, got %q", "default", w.config.IncidentNamespace)
	}
}

// TestNodeWatcher_DedupKey_IsStable verifies that the dedup keys produced by
// nodeAlertKey are deterministic and include UID + condition type.
func TestNodeWatcher_DedupKey_IsStable(t *testing.T) {
	key := nodeAlertKey("abc-uid-123", "DiskPressure")
	want := "abc-uid-123:DiskPressure"
	if key != want {
		t.Errorf("alertKey: want %q, got %q", want, key)
	}
}

// TestNodePressureEvent_DedupKey_IncludesNodeNameAndPressureType verifies composites.
func TestNodePressureEvent_DedupKey_IncludesNodeNameAndPressureType(t *testing.T) {
	ev := NodePressureEvent{
		BaseEvent:    BaseEvent{NodeName: "node-1"},
		PressureType: "MemoryPressure",
	}
	want := "NodePressure:node-1:MemoryPressure"
	if ev.DedupKey() != want {
		t.Errorf("DedupKey: want %q, got %q", want, ev.DedupKey())
	}
}
