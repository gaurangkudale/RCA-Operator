package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestDeploymentWatcher is the test constructor: no cache needed because all
// tests call detectStalledRollout directly rather than going through the informer.
func newTestDeploymentWatcher(namespaces []string) (*DeploymentWatcher, *recordingEmitter) {
	em := &recordingEmitter{}
	w := NewDeploymentWatcher(nil, em, logr.Discard(), DeploymentWatcherConfig{
		AgentName:       "agent-test",
		WatchNamespaces: namespaces,
	})
	return w, em
}

// stalledDeployment builds a Deployment whose Progressing condition is
// Status=False / Reason=ProgressDeadlineExceeded, representing a stalled rollout.
func stalledDeployment(namespace, name, uid string, generation int64, desiredReplicas, readyReplicas int32, condMessage string, condTime time.Time) *appsv1.Deployment {
	replicas := desiredReplicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: generation,
			ReadyReplicas:      readyReplicas,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             reasonProgressDeadlineExceeded,
					Message:            condMessage,
					LastTransitionTime: metav1.NewTime(condTime),
				},
			},
		},
	}
}

// healthyDeployment builds a Deployment that is fully available with its
// Progressing condition set to Status=True, representing a completed rollout.
func healthyDeployment(namespace, name, uid string, generation int64, replicas int32) *appsv1.Deployment {
	r := replicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Spec: appsv1.DeploymentSpec{Replicas: &r},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: generation,
			ReadyReplicas:      replicas,
			AvailableReplicas:  replicas,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             "NewReplicaSetAvailable",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
}

// deploymentWithoutProgressingCondition builds a Deployment that has no
// Progressing condition at all (e.g. freshly created, not yet reconciled).
func deploymentWithoutProgressingCondition(namespace, name, uid string) *appsv1.Deployment {
	r := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 0},
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestDeploymentWatcher_DetectsStalledRollout verifies that a Deployment whose
// Progressing condition is ProgressDeadlineExceeded causes a StalledRolloutEvent
// to be emitted.
func TestDeploymentWatcher_DetectsStalledRollout(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 0, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil) // watch all namespaces
	w.clock = func() time.Time { return now }

	dep := stalledDeployment("production", "payment-service", "uid-1", 5, 3, 0, "progress deadline exceeded", now.Add(-2*time.Minute))
	w.detectStalledRollout(nil, dep)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 StalledRolloutEvent, got %d", len(em.events))
	}
	if _, ok := em.events[0].(StalledRolloutEvent); !ok {
		t.Fatalf("expected StalledRolloutEvent, got %T", em.events[0])
	}
}

// TestDeploymentWatcher_EventFieldsPopulated verifies that all fields on the
// emitted StalledRolloutEvent are correctly populated from the Deployment object.
func TestDeploymentWatcher_EventFieldsPopulated(t *testing.T) {
	condTime := time.Date(2026, 3, 12, 8, 55, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return condTime.Add(time.Minute) }

	dep := stalledDeployment("production", "payment-service", "uid-fields", 7, 3, 1, "timed out waiting for rollout", condTime)
	w.detectStalledRollout(nil, dep)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev, ok := em.events[0].(StalledRolloutEvent)
	if !ok {
		t.Fatalf("expected StalledRolloutEvent, got %T", em.events[0])
	}

	if ev.Namespace != "production" {
		t.Errorf("Namespace: want %q, got %q", "production", ev.Namespace)
	}
	if ev.DeploymentName != "payment-service" {
		t.Errorf("DeploymentName: want %q, got %q", "payment-service", ev.DeploymentName)
	}
	if ev.PodName != "payment-service" {
		t.Errorf("PodName (routing key): want %q, got %q", "payment-service", ev.PodName)
	}
	if ev.AgentName != "agent-test" {
		t.Errorf("AgentName: want %q, got %q", "agent-test", ev.AgentName)
	}
	if ev.Revision != 7 {
		t.Errorf("Revision: want 7, got %d", ev.Revision)
	}
	if ev.DesiredReplicas != 3 {
		t.Errorf("DesiredReplicas: want 3, got %d", ev.DesiredReplicas)
	}
	if ev.ReadyReplicas != 1 {
		t.Errorf("ReadyReplicas: want 1, got %d", ev.ReadyReplicas)
	}
	if ev.Reason != reasonProgressDeadlineExceeded {
		t.Errorf("Reason: want %q, got %q", reasonProgressDeadlineExceeded, ev.Reason)
	}
	if ev.Message != "timed out waiting for rollout" {
		t.Errorf("Message: want %q, got %q", "timed out waiting for rollout", ev.Message)
	}
	// At should equal the condition's LastTransitionTime, not w.clock().
	if !ev.At.Equal(condTime) {
		t.Errorf("At: want %v (condTime), got %v", condTime, ev.At)
	}
}

// TestDeploymentWatcher_DedupSuppressesRepeatSameGeneration verifies that calling
// detectStalledRollout multiple times for the same (UID, generation) pair emits
// exactly one event — subsequent calls are silently deduped.
func TestDeploymentWatcher_DedupSuppressesRepeatSameGeneration(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 5, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return now }

	dep := stalledDeployment("staging", "frontend", "uid-dedup", 4, 2, 0, "timeout", now)

	// Simulate three informer updates for the same stalled state.
	w.detectStalledRollout(nil, dep)
	w.detectStalledRollout(nil, dep)
	w.detectStalledRollout(nil, dep)

	if len(em.events) != 1 {
		t.Fatalf("expected exactly 1 event (dedup), got %d", len(em.events))
	}
}

// TestDeploymentWatcher_NewGenerationAllowsNewEmission verifies that after an
// initial stall is deduped, a new rollout (higher observedGeneration) that also
// stalls produces a fresh StalledRolloutEvent.
func TestDeploymentWatcher_NewGenerationAllowsNewEmission(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 10, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return now }

	uid := "uid-gen"

	// Generation 3 stalls — first event fires.
	dep3 := stalledDeployment("production", "cart", uid, 3, 1, 0, "timeout", now)
	w.detectStalledRollout(nil, dep3)
	if len(em.events) != 1 {
		t.Fatalf("after gen-3 stall: expected 1 event, got %d", len(em.events))
	}

	// Generation 3 again — deduped.
	w.detectStalledRollout(nil, dep3)
	if len(em.events) != 1 {
		t.Fatalf("after gen-3 repeat: expected still 1 event, got %d", len(em.events))
	}

	// Generation 4 stalls (new deployment push that also stalls) — second event fires.
	dep4 := stalledDeployment("production", "cart", uid, 4, 1, 0, "still stuck", now.Add(10*time.Minute))
	w.detectStalledRollout(nil, dep4)
	if len(em.events) != 2 {
		t.Fatalf("after gen-4 stall: expected 2 events, got %d", len(em.events))
	}

	ev := em.events[1].(StalledRolloutEvent)
	if ev.Revision != 4 {
		t.Errorf("second event Revision: want 4, got %d", ev.Revision)
	}
}

// TestDeploymentWatcher_RecoveryClears_ThenNextStallEmits ensures that when a
// Deployment's Progressing condition returns to Status=True (rollout recovered),
// the dedup gate is cleared so a future stall on the same UID fires again.
func TestDeploymentWatcher_RecoveryClears_ThenNextStallEmits(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 15, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return now }

	uid := "uid-recover"

	// Stall detected — event fires.
	stalled := stalledDeployment("production", "api", uid, 2, 2, 0, "timeout", now)
	w.detectStalledRollout(nil, stalled)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 event after stall, got %d", len(em.events))
	}

	// Same stall, same generation — deduped.
	w.detectStalledRollout(nil, stalled)
	if len(em.events) != 1 {
		t.Fatalf("expected still 1 event (dedup), got %d", len(em.events))
	}

	// Rollout recovers: Progressing=True clears the gate.
	healthy := healthyDeployment("production", "api", uid, 2, 2)
	w.detectStalledRollout(nil, healthy)
	if len(em.events) != 1 {
		t.Fatalf("healthy update should not emit; expected still 1 event, got %d", len(em.events))
	}

	// Same generation stalls again (e.g. another rollout wave) — gate is clear; fires.
	stalledAgain := stalledDeployment("production", "api", uid, 2, 2, 0, "timeout again", now.Add(30*time.Minute))
	w.detectStalledRollout(nil, stalledAgain)
	if len(em.events) != 2 {
		t.Fatalf("expected 2nd event after recovery+restall, got %d", len(em.events))
	}
}

// TestDeploymentWatcher_NamespaceFilter verifies that Deployments in namespaces
// not listed in WatchNamespaces produce no events.
func TestDeploymentWatcher_NamespaceFilter(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 20, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher([]string{"production"}) // only watch "production"
	w.clock = func() time.Time { return now }

	// Deployment in un-watched namespace.
	dep := stalledDeployment("staging", "service-b", "uid-ns", 1, 2, 0, "timeout", now)
	w.onDeploymentAdd(dep)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for unwatched namespace, got %d", len(em.events))
	}

	// Same deployment in the watched namespace should fire.
	dep.Namespace = "production"
	dep.UID = "uid-ns-prod"
	w.onDeploymentAdd(dep)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event for watched namespace, got %d", len(em.events))
	}
}

// TestDeploymentWatcher_HealthyDeployment_NoEvent verifies that a fully
// available Deployment (Progressing=True) does not cause any emission.
func TestDeploymentWatcher_HealthyDeployment_NoEvent(t *testing.T) {
	w, em := newTestDeploymentWatcher(nil)

	dep := healthyDeployment("production", "auth-service", "uid-healthy", 3, 2)
	w.detectStalledRollout(nil, dep)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for healthy deployment, got %d", len(em.events))
	}
}

// TestDeploymentWatcher_NoProgressingCondition_NoEvent verifies that a Deployment
// with no Progressing condition (e.g. freshly created, not yet reconciled by
// the deployment controller) does not emit any event.
func TestDeploymentWatcher_NoProgressingCondition_NoEvent(t *testing.T) {
	w, em := newTestDeploymentWatcher(nil)

	dep := deploymentWithoutProgressingCondition("production", "new-service", "uid-no-cond")
	w.detectStalledRollout(nil, dep)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events when no Progressing condition present, got %d", len(em.events))
	}
}

// TestDeploymentWatcher_TransientProgressingFalse_NoEvent verifies that a
// Progressing=False condition with a reason other than ProgressDeadlineExceeded
// (a transient state during a rollout update) does not produce an event.
func TestDeploymentWatcher_TransientProgressingFalse_NoEvent(t *testing.T) {
	now := time.Date(2026, 3, 12, 9, 30, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return now }

	r := int32(3)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "production", UID: "uid-transient"},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2,
			ReadyReplicas:      1,
			Conditions: []appsv1.DeploymentCondition{
				{
					// "ReplicaSetUpdated" is a normal transient reason during a rollout;
					// it must not be confused with a stall.
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             "ReplicaSetUpdated",
					LastTransitionTime: metav1.NewTime(now),
				},
			},
		},
	}

	w.detectStalledRollout(nil, dep)

	if len(em.events) != 0 {
		t.Fatalf("expected 0 events for transient Progressing=False, got %d", len(em.events))
	}
}

// TestDeploymentWatcher_FallbackTimestamp_UsesClockWhenCondTimeIsZero verifies
// that when the Progressing condition's LastTransitionTime is zero the watcher
// falls back to using its clock, rather than emitting a zero-valued At field.
func TestDeploymentWatcher_FallbackTimestamp_UsesClockWhenCondTimeIsZero(t *testing.T) {
	fallback := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return fallback }

	r := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "production", UID: "uid-ts"},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentProgressing,
					Status: corev1.ConditionFalse,
					Reason: reasonProgressDeadlineExceeded,
					// LastTransitionTime intentionally left zero.
				},
			},
		},
	}

	w.detectStalledRollout(nil, dep)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev := em.events[0].(StalledRolloutEvent)
	if !ev.At.Equal(fallback) {
		t.Errorf("At: want fallback clock %v, got %v", fallback, ev.At)
	}
}

// TestDeploymentWatcher_SpecReplicasNil_DefaultsToOne verifies that when
// spec.replicas is nil the emitted event carries DesiredReplicas=1, matching
// the Kubernetes default of one replica.
func TestDeploymentWatcher_SpecReplicasNil_DefaultsToOne(t *testing.T) {
	now := time.Date(2026, 3, 12, 10, 5, 0, 0, time.UTC)
	w, em := newTestDeploymentWatcher(nil)
	w.clock = func() time.Time { return now }

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "production", UID: "uid-nil-rep"},
		Spec:       appsv1.DeploymentSpec{Replicas: nil}, // nil → default 1
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:               appsv1.DeploymentProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             reasonProgressDeadlineExceeded,
					LastTransitionTime: metav1.NewTime(now),
				},
			},
		},
	}

	w.detectStalledRollout(nil, dep)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.events))
	}
	ev := em.events[0].(StalledRolloutEvent)
	if ev.DesiredReplicas != 1 {
		t.Errorf("DesiredReplicas: want 1 (default), got %d", ev.DesiredReplicas)
	}
}

// TestDeploymentWatcher_DedupKey_IsStable verifies that the DedupKey produced
// by StalledRolloutEvent is deterministic and includes both the namespace and
// deployment name.
func TestDeploymentWatcher_DedupKey_IsStable(t *testing.T) {
	ev := StalledRolloutEvent{
		BaseEvent:      BaseEvent{Namespace: "production", PodName: "payment-service"},
		DeploymentName: "payment-service",
		Revision:       5,
	}

	key := ev.DedupKey()
	want := "StalledRollout:production:payment-service"
	if key != want {
		t.Errorf("DedupKey: want %q, got %q", want, key)
	}
}
