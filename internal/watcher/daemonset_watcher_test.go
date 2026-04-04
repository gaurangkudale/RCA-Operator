package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func newTestDaemonSetWatcher(namespaces []string) (*DaemonSetWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewDaemonSetWatcher(nil, em, logr.Discard(), DaemonSetWatcherConfig{
		AgentName:       "agent-test",
		WatchNamespaces: namespaces,
	})
	return w, em
}

func stalledDaemonSet(namespace, name, uid string, generation int64, desired, ready, updated int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     generation,
			DesiredNumberScheduled: desired,
			NumberReady:            ready,
			UpdatedNumberScheduled: updated,
		},
	}
}

func healthyDaemonSet(namespace, name, uid string, generation int64, count int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     generation,
			DesiredNumberScheduled: count,
			NumberReady:            count,
			UpdatedNumberScheduled: count,
		},
	}
}

func TestDaemonSetWatcher_DetectsStalledRollout(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC)
	w, em := newTestDaemonSetWatcher(nil)
	w.clock = func() time.Time { return now }

	ds := stalledDaemonSet("production", "fluentd", "uid-1", 5, 3, 1, 1)
	w.detectStalled(ds)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(StalledDaemonSetEvent)
	if !ok {
		t.Fatalf("expected StalledDaemonSetEvent, got %T", em.events[0])
	}
	if ev.DaemonSetName != "fluentd" {
		t.Errorf("DaemonSetName: want %q, got %q", "fluentd", ev.DaemonSetName)
	}
	if ev.DesiredNumberScheduled != 3 {
		t.Errorf("DesiredNumberScheduled: want 3, got %d", ev.DesiredNumberScheduled)
	}
}

func TestDaemonSetWatcher_DedupSameGeneration(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 5, 0, 0, time.UTC)
	w, em := newTestDaemonSetWatcher(nil)
	w.clock = func() time.Time { return now }

	ds := stalledDaemonSet("production", "fluentd", "uid-dedup", 4, 3, 1, 1)
	w.detectStalled(ds)
	w.detectStalled(ds)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event (dedup), got %d", len(em.events))
	}
}

func TestDaemonSetWatcher_RecoveryClearsGate(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 10, 0, 0, time.UTC)
	w, em := newTestDaemonSetWatcher(nil)
	w.clock = func() time.Time { return now }

	uid := "uid-recover"
	stalled := stalledDaemonSet("production", "fluentd", uid, 2, 3, 1, 1)
	w.detectStalled(stalled)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}

	healthy := healthyDaemonSet("production", "fluentd", uid, 2, 3)
	w.detectStalled(healthy)

	stalled2 := stalledDaemonSet("production", "fluentd", uid, 2, 3, 1, 1)
	w.detectStalled(stalled2)
	if len(em.events) != 2 {
		t.Fatalf("expected 2 events after recovery, got %d", len(em.events))
	}
}

func TestDaemonSetWatcher_HealthyNoEvent(t *testing.T) {
	w, em := newTestDaemonSetWatcher(nil)
	ds := healthyDaemonSet("production", "fluentd", "uid-healthy", 3, 3)
	w.detectStalled(ds)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for healthy daemonset, got %d", len(em.events))
	}
}

func TestDaemonSetWatcher_ZeroDesired_NoEvent(t *testing.T) {
	w, em := newTestDaemonSetWatcher(nil)
	ds := stalledDaemonSet("production", "fluentd", "uid-zero", 1, 0, 0, 0)
	w.detectStalled(ds)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events when desired=0, got %d", len(em.events))
	}
}

func TestDaemonSetWatcher_NamespaceFilter(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 20, 0, 0, time.UTC)
	w, em := newTestDaemonSetWatcher([]string{"production"})
	w.clock = func() time.Time { return now }

	ds := stalledDaemonSet("staging", "fluentd", "uid-ns", 1, 3, 0, 0)
	w.onAdd(ds)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for unwatched namespace, got %d", len(em.events))
	}
}

func TestDaemonSetWatcher_DedupKey(t *testing.T) {
	ev := StalledDaemonSetEvent{
		BaseEvent:     BaseEvent{Namespace: "production"},
		DaemonSetName: "fluentd",
	}
	want := "StalledDaemonSet:production:fluentd"
	if got := ev.DedupKey(); got != want {
		t.Errorf("DedupKey: want %q, got %q", want, got)
	}
}
