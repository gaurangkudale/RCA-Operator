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

func TestDetectCrashLoopIncludesExitCodeContext(t *testing.T) {
	now := time.Date(2026, 3, 11, 18, 15, 0, 0, time.UTC)
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{
		AgentName:                 "agent-a",
		CrashLoopRestartThreshold: 3,
	})
	w.clock = func() time.Time { return now }

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "development", Name: "svc", UID: types.UID("pod-cl")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 2,
		}}},
	}
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "development", Name: "svc", UID: types.UID("pod-cl")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 3,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: string(EventTypeCrashLoopBackOff),
			}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 126,
				Reason:   "Error",
			}},
		}}},
	}

	w.detectCrashLoop(oldPod, newPod)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 crash loop event, got %d", len(emitter.events))
	}
	event, ok := emitter.events[0].(CrashLoopBackOffEvent)
	if !ok {
		t.Fatalf("expected CrashLoopBackOffEvent, got %T", emitter.events[0])
	}
	if event.LastExitCode != 126 || event.ExitCodeCategory != "PermissionDenied" {
		t.Fatalf("unexpected crash loop exit-code context: code=%d category=%s", event.LastExitCode, event.ExitCodeCategory)
	}
}

func TestDetectContainerExitCodeEmitsClassifiedEvent(t *testing.T) {
	now := time.Date(2026, 3, 11, 18, 20, 0, 0, time.UTC)
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "development", Name: "svc", UID: types.UID("pod-ec")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 1,
		}}},
	}
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "development", Name: "svc", UID: types.UID("pod-ec")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 2,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 127,
				Reason:   "Error",
			}},
		}}},
	}

	w.detectContainerExitCode(oldPod, newPod)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(emitter.events))
	}
	event, ok := emitter.events[0].(ContainerExitCodeEvent)
	if !ok {
		t.Fatalf("expected ContainerExitCodeEvent, got %T", emitter.events[0])
	}
	if event.ExitCode != 127 || event.Category != "CommandNotFound" {
		t.Fatalf("unexpected exit code classification: code=%d category=%s", event.ExitCode, event.Category)
	}
}

func TestDetectContainerExitCodeSkipsCrashLoopPods(t *testing.T) {
	now := time.Date(2026, 3, 11, 18, 22, 0, 0, time.UTC)
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "development", Name: "svc", UID: types.UID("pod-dup")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 3,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: string(EventTypeCrashLoopBackOff),
			}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 127,
				Reason:   "Error",
			}},
		}}},
	}

	w.detectContainerExitCode(nil, newPod)

	if len(emitter.events) != 0 {
		t.Fatalf("expected no standalone exit-code event for crash loop pod, got %d", len(emitter.events))
	}
}

func TestDetectGracePeriodViolationEmitsOnceAfterDeadline(t *testing.T) {
	now := time.Date(2026, 3, 11, 18, 25, 0, 0, time.UTC)
	emitter := &recordingEmitter{}
	w := NewPodWatcher(nil, emitter, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	deletionTime := metav1.NewTime(now.Add(-40 * time.Second))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:                  "development",
			Name:                       "slow-terminating-pod",
			UID:                        types.UID("pod-gp"),
			DeletionTimestamp:          &deletionTime,
			DeletionGracePeriodSeconds: int64Ptr(30),
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
				StartedAt: metav1.NewTime(now.Add(-10 * time.Minute)),
			}},
		}}},
	}

	w.detectGracePeriodViolation(pod)
	w.detectGracePeriodViolation(pod)

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 grace period violation event, got %d", len(emitter.events))
	}
	event, ok := emitter.events[0].(GracePeriodViolationEvent)
	if !ok {
		t.Fatalf("expected GracePeriodViolationEvent, got %T", emitter.events[0])
	}
	if event.GracePeriodSeconds != 30 {
		t.Fatalf("expected grace period 30s, got %d", event.GracePeriodSeconds)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
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
