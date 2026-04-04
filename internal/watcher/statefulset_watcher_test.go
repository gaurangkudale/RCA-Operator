package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func newTestStatefulSetWatcher(namespaces []string) (*StatefulSetWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewStatefulSetWatcher(nil, em, logr.Discard(), StatefulSetWatcherConfig{
		AgentName:       "agent-test",
		WatchNamespaces: namespaces,
	})
	return w, em
}

func stalledStatefulSet(namespace, uid string, generation int64, ready, updated int32) *appsv1.StatefulSet {
	replicas := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis",
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Spec: appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: generation,
			ReadyReplicas:      ready,
			UpdatedReplicas:    updated,
			CurrentRevision:    "rev-1",
			UpdateRevision:     "rev-2", // != CurrentRevision → rollout in progress
		},
	}
}

func healthyStatefulSet(namespace, name, uid string, generation int64, replicas int32) *appsv1.StatefulSet {
	r := replicas
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Spec: appsv1.StatefulSetSpec{Replicas: &r},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: generation,
			ReadyReplicas:      replicas,
			UpdatedReplicas:    replicas,
			CurrentRevision:    "rev-2",
			UpdateRevision:     "rev-2", // equal → rollout complete
		},
	}
}

func TestStatefulSetWatcher_DetectsStalledRollout(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC)
	w, em := newTestStatefulSetWatcher(nil)
	w.clock = func() time.Time { return now }

	sts := stalledStatefulSet("production", "uid-1", 5, 1, 1)
	w.detectStalled(sts)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(StalledStatefulSetEvent)
	if !ok {
		t.Fatalf("expected StalledStatefulSetEvent, got %T", em.events[0])
	}
	if ev.StatefulSetName != "redis" {
		t.Errorf("StatefulSetName: want %q, got %q", "redis", ev.StatefulSetName)
	}
	if ev.DesiredReplicas != 3 {
		t.Errorf("DesiredReplicas: want 3, got %d", ev.DesiredReplicas)
	}
	if ev.ReadyReplicas != 1 {
		t.Errorf("ReadyReplicas: want 1, got %d", ev.ReadyReplicas)
	}
}

func TestStatefulSetWatcher_DedupSameGeneration(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 5, 0, 0, time.UTC)
	w, em := newTestStatefulSetWatcher(nil)
	w.clock = func() time.Time { return now }

	sts := stalledStatefulSet("production", "uid-dedup", 4, 1, 1)
	w.detectStalled(sts)
	w.detectStalled(sts)
	w.detectStalled(sts)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event (dedup), got %d", len(em.events))
	}
}

func TestStatefulSetWatcher_RecoveryClearsGate(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 10, 0, 0, time.UTC)
	w, em := newTestStatefulSetWatcher(nil)
	w.clock = func() time.Time { return now }

	uid := "uid-recover"

	stalled := stalledStatefulSet("production", uid, 2, 1, 1)
	w.detectStalled(stalled)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}

	healthy := healthyStatefulSet("production", "redis", uid, 2, 3)
	w.detectStalled(healthy)

	// After recovery, a new stall on same generation fires again.
	stalled2 := stalledStatefulSet("production", uid, 2, 1, 1)
	w.detectStalled(stalled2)
	if len(em.events) != 2 {
		t.Fatalf("expected 2 events after recovery, got %d", len(em.events))
	}
}

func TestStatefulSetWatcher_HealthyNoEvent(t *testing.T) {
	w, em := newTestStatefulSetWatcher(nil)
	sts := healthyStatefulSet("production", "redis", "uid-healthy", 3, 3)
	w.detectStalled(sts)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for healthy statefulset, got %d", len(em.events))
	}
}

func TestStatefulSetWatcher_NamespaceFilter(t *testing.T) {
	now := time.Date(2026, 4, 5, 9, 20, 0, 0, time.UTC)
	w, em := newTestStatefulSetWatcher([]string{"production"})
	w.clock = func() time.Time { return now }

	sts := stalledStatefulSet("staging", "uid-ns", 1, 0, 0)
	w.onAdd(sts)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for unwatched namespace, got %d", len(em.events))
	}

	sts.Namespace = "production"
	sts.UID = "uid-ns-prod"
	w.onAdd(sts)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event for watched namespace, got %d", len(em.events))
	}
}

func TestStatefulSetWatcher_DedupKey(t *testing.T) {
	ev := StalledStatefulSetEvent{
		BaseEvent:       BaseEvent{Namespace: "production"},
		StatefulSetName: "redis",
	}
	want := "StalledStatefulSet:production:redis"
	if got := ev.DedupKey(); got != want {
		t.Errorf("DedupKey: want %q, got %q", want, got)
	}
}
