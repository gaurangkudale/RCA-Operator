package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type recordingEmitter struct {
	events []CorrelatorEvent
}

func (r *recordingEmitter) Emit(event CorrelatorEvent) {
	r.events = append(r.events, event)
}

func TestTrackReadyStateEmitsHealthyForLongReadyPodOnScan(t *testing.T) {
	now := time.Date(2026, 3, 11, 18, 0, 0, 0, time.UTC)
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{
		AgentName:            "agent-a",
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	pod := readyPod("development", "flaky-app-demo", "uid-1", now.Add(-5*time.Minute))
	w.trackReadyState(nil, pod)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 healthy event, got %d", len(emitter.events))
	}
	if _, ok := emitter.events[0].(PodHealthyEvent); !ok {
		t.Fatalf("expected PodHealthyEvent, got %T", emitter.events[0])
	}
}

func TestTrackReadyStateDoesNotResetReadyTimerAcrossScans(t *testing.T) {
	start := time.Date(2026, 3, 11, 18, 10, 0, 0, time.UTC)
	now := start
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{
		AgentName:            "agent-a",
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	pod := readyPod("development", "flaky-app-demo", "uid-2", start)
	w.trackReadyState(nil, pod)
	if len(emitter.events) != 0 {
		t.Fatalf("expected no healthy event before stability window, got %d", len(emitter.events))
	}

	now = start.Add(61 * time.Second)
	w.trackReadyState(nil, pod)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 healthy event after stability window, got %d", len(emitter.events))
	}
}

func readyPod(namespace, name, uid string, readySince time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			UID:               types.UID(uid),
			CreationTimestamp: metav1.NewTime(readySince.Add(-time.Minute)),
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:               corev1.PodReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(readySince),
				},
			},
		},
	}
}
