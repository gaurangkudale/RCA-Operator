package watcher

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	exitCategoryPermissionDenied = "PermissionDenied"
	imagePullErrReason           = "ErrImagePull"
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
	if event.LastExitCode != 126 || event.ExitCodeCategory != exitCategoryPermissionDenied {
		t.Fatalf("unexpected crash loop exit-code context: code=%d category=%s", event.LastExitCode, event.ExitCodeCategory)
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

// ── classifyExitCode ──────────────────────────────────────────────────────────

func TestClassifyExitCode(t *testing.T) {
	cases := []struct {
		code     int32
		wantCat  string
		wantDesc string
	}{
		{1, "GeneralError", "General application error"},
		{2, "ShellMisuse", "Misuse of shell builtins"},
		{126, exitCategoryPermissionDenied, "Command invoked cannot execute"},
		{127, "CommandNotFound", "Command not found"},
		{130, "Interrupted", "Script terminated by Control-C"},
		{134, "Abort", "Process aborted (SIGABRT)"},
		{139, "SegmentationFault", "Segmentation fault (SIGSEGV)"},
		{143, "Terminated", "Terminated by SIGTERM"},
		{255, "OutOfRange", "Exit status out of range"},
		{42, "NonZeroExit", "Unclassified non-zero exit code"},  // default
		{137, "NonZeroExit", "Unclassified non-zero exit code"}, // SIGKILL — not in the list
	}
	for _, tc := range cases {
		cat, desc := classifyExitCode(tc.code)
		if cat != tc.wantCat {
			t.Errorf("classifyExitCode(%d) category: got %q, want %q", tc.code, cat, tc.wantCat)
		}
		if desc != tc.wantDesc {
			t.Errorf("classifyExitCode(%d) description: got %q, want %q", tc.code, desc, tc.wantDesc)
		}
	}
}

// ── lastTerminatedState ───────────────────────────────────────────────────────

func TestLastTerminatedState(t *testing.T) {
	t.Run("prefers LastTerminationState", func(t *testing.T) {
		last := &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}
		current := &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}
		status := corev1.ContainerStatus{
			LastTerminationState: corev1.ContainerState{Terminated: last},
			State:                corev1.ContainerState{Terminated: current},
		}
		got := lastTerminatedState(status)
		if got != last {
			t.Errorf("expected LastTerminationState.Terminated to be returned")
		}
	})

	t.Run("falls through to State.Terminated when LastTerminationState is nil", func(t *testing.T) {
		current := &corev1.ContainerStateTerminated{ExitCode: 137}
		status := corev1.ContainerStatus{
			State: corev1.ContainerState{Terminated: current},
		}
		got := lastTerminatedState(status)
		if got != current {
			t.Errorf("expected State.Terminated to be returned")
		}
	})

	t.Run("returns nil when both are nil", func(t *testing.T) {
		status := corev1.ContainerStatus{}
		got := lastTerminatedState(status)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

// ── classifiedExitInfo ────────────────────────────────────────────────────────

func TestClassifiedExitInfo(t *testing.T) {
	t.Run("returns false when no terminated state", func(t *testing.T) {
		_, _, _, _, ok := classifiedExitInfo(corev1.ContainerStatus{})
		if ok {
			t.Error("expected ok=false when no terminated state")
		}
	})

	t.Run("returns false for exit code 0", func(t *testing.T) {
		status := corev1.ContainerStatus{
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"},
			},
		}
		_, _, _, _, ok := classifiedExitInfo(status)
		if ok {
			t.Error("expected ok=false for exit code 0")
		}
	})

	t.Run("returns false for OOMKilled reason", func(t *testing.T) {
		status := corev1.ContainerStatus{
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"},
			},
		}
		_, _, _, _, ok := classifiedExitInfo(status)
		if ok {
			t.Error("expected ok=false for OOMKilled reason")
		}
	})

	t.Run("returns classification for non-zero non-OOM exit", func(t *testing.T) {
		status := corev1.ContainerStatus{
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: 126, Reason: "Error"},
			},
		}
		exitCode, reason, category, _, ok := classifiedExitInfo(status)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if exitCode != 126 {
			t.Errorf("exitCode: got %d, want 126", exitCode)
		}
		if reason != "Error" {
			t.Errorf("reason: got %q, want Error", reason)
		}
		if category != exitCategoryPermissionDenied {
			t.Errorf("category: got %q, want PermissionDenied", category)
		}
	})
}

// ── statusByContainer ─────────────────────────────────────────────────────────

func TestStatusByContainer(t *testing.T) {
	t.Run("nil pod returns nil", func(t *testing.T) {
		if statusByContainer(nil) != nil {
			t.Error("expected nil for nil pod")
		}
	})

	t.Run("builds map by container name", func(t *testing.T) {
		pod := &corev1.Pod{
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 3},
				{Name: "sidecar", RestartCount: 0},
			}},
		}
		m := statusByContainer(pod)
		if len(m) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(m))
		}
		if m["app"].RestartCount != 3 {
			t.Errorf("app restart count: got %d", m["app"].RestartCount)
		}
	})
}

// ── isPodReady ────────────────────────────────────────────────────────────────

func TestIsPodReady(t *testing.T) {
	t.Run("nil pod returns false", func(t *testing.T) {
		if isPodReady(nil) {
			t.Error("expected false for nil pod")
		}
	})
	t.Run("non-Running pod returns false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
		if isPodReady(pod) {
			t.Error("expected false for Pending pod")
		}
	})
	t.Run("Running pod with no PodReady condition returns false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if isPodReady(pod) {
			t.Error("expected false when PodReady condition absent")
		}
	})
	t.Run("Running pod with PodReady=False returns false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionFalse,
			}},
		}}
		if isPodReady(pod) {
			t.Error("expected false for PodReady=False")
		}
	})
	t.Run("Running pod with PodReady=True returns true", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
			}},
		}}
		if !isPodReady(pod) {
			t.Error("expected true for Running+Ready=True pod")
		}
	})
}

// ── podReadySince ─────────────────────────────────────────────────────────────

func TestPodReadySince(t *testing.T) {
	fallback := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	transitionTime := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

	t.Run("nil pod returns fallback", func(t *testing.T) {
		if got := podReadySince(nil, fallback); !got.Equal(fallback) {
			t.Errorf("got %v, want fallback", got)
		}
	})
	t.Run("Ready=True with valid transition time returns transition", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(transitionTime),
		}}}}
		if got := podReadySince(pod, fallback); !got.Equal(transitionTime) {
			t.Errorf("got %v, want transitionTime", got)
		}
	})
	t.Run("Ready=True but zero transition time returns fallback", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Time{},
		}}}}
		if got := podReadySince(pod, fallback); !got.Equal(fallback) {
			t.Errorf("got %v, want fallback", got)
		}
	})
	t.Run("Ready=False returns fallback", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(transitionTime),
		}}}}
		if got := podReadySince(pod, fallback); !got.Equal(fallback) {
			t.Errorf("got %v, want fallback", got)
		}
	})
	t.Run("no PodReady condition returns fallback", func(t *testing.T) {
		pod := &corev1.Pod{}
		if got := podReadySince(pod, fallback); !got.Equal(fallback) {
			t.Errorf("got %v, want fallback", got)
		}
	})
}

// ── gracePeriodSeconds ────────────────────────────────────────────────────────

func TestGracePeriodSeconds(t *testing.T) {
	t.Run("nil pod returns 30", func(t *testing.T) {
		if got := gracePeriodSeconds(nil); got != 30 {
			t.Errorf("got %d, want 30", got)
		}
	})
	t.Run("nil DeletionGracePeriodSeconds returns 30", func(t *testing.T) {
		pod := &corev1.Pod{}
		if got := gracePeriodSeconds(pod); got != 30 {
			t.Errorf("got %d, want 30", got)
		}
	})
	t.Run("zero DeletionGracePeriodSeconds returns 30", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionGracePeriodSeconds: int64Ptr(0)}}
		if got := gracePeriodSeconds(pod); got != 30 {
			t.Errorf("got %d, want 30", got)
		}
	})
	t.Run("positive DeletionGracePeriodSeconds returns it", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionGracePeriodSeconds: int64Ptr(60)}}
		if got := gracePeriodSeconds(pod); got != 60 {
			t.Errorf("got %d, want 60", got)
		}
	})
}

// ── hasRunningContainers ──────────────────────────────────────────────────────

func TestHasRunningContainers(t *testing.T) {
	running := &corev1.ContainerStateRunning{}

	t.Run("nil pod returns false", func(t *testing.T) {
		if hasRunningContainers(nil) {
			t.Error("expected false for nil pod")
		}
	})
	t.Run("running init container returns true", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Running: running},
			}},
		}}
		if !hasRunningContainers(pod) {
			t.Error("expected true when init container is running")
		}
	})
	t.Run("running regular container returns true", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Running: running},
			}},
		}}
		if !hasRunningContainers(pod) {
			t.Error("expected true when regular container is running")
		}
	})
	t.Run("no running containers returns false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		}}
		if hasRunningContainers(pod) {
			t.Error("expected false when no container is running")
		}
	})
}

// ── hasImagePullFailure ───────────────────────────────────────────────────────

func TestHasImagePullFailure(t *testing.T) {
	t.Run("ImagePullBackOff regular container returns true", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			}},
		}}
		if !hasImagePullFailure(pod) {
			t.Error("expected true for ImagePullBackOff")
		}
	})
	t.Run("ErrImagePull init container returns true", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: imagePullErrReason}},
			}},
		}}
		if !hasImagePullFailure(pod) {
			t.Error("expected true for ErrImagePull in init container")
		}
	})
	t.Run("CrashLoopBackOff returns false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		}}
		if hasImagePullFailure(pod) {
			t.Error("expected false for CrashLoopBackOff")
		}
	})
}

// ── toNamespaceSet ────────────────────────────────────────────────────────────

func TestToNamespaceSet(t *testing.T) {
	t.Run("empty slice returns nil", func(t *testing.T) {
		if toNamespaceSet(nil) != nil {
			t.Error("expected nil for empty input")
		}
		if toNamespaceSet([]string{}) != nil {
			t.Error("expected nil for empty slice")
		}
	})
	t.Run("all-blank strings returns nil", func(t *testing.T) {
		if toNamespaceSet([]string{"", ""}) != nil {
			t.Error("expected nil when all entries are blank")
		}
	})
	t.Run("valid namespaces are stored", func(t *testing.T) {
		m := toNamespaceSet([]string{"development", "staging", ""})
		if len(m) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(m))
		}
		if _, ok := m["development"]; !ok {
			t.Error("expected development in set")
		}
		if _, ok := m["staging"]; !ok {
			t.Error("expected staging in set")
		}
	})
}

// ── shouldWatchNamespace ──────────────────────────────────────────────────────

func TestShouldWatchNamespace(t *testing.T) {
	t.Run("watch-all mode (no namespaces configured)", func(t *testing.T) {
		w := NewPodWatcher(nil, &recordingEmitter{}, logr.Discard(), PodWatcherConfig{AgentName: "a"})
		if !w.shouldWatchNamespace("any-namespace") {
			t.Error("expected true in watch-all mode")
		}
	})
	t.Run("configured namespace matches", func(t *testing.T) {
		w := NewPodWatcher(nil, &recordingEmitter{}, logr.Discard(), PodWatcherConfig{
			AgentName:       "a",
			WatchNamespaces: []string{"production"},
		})
		if !w.shouldWatchNamespace("production") {
			t.Error("expected true for watched namespace")
		}
	})
	t.Run("unlisted namespace is rejected", func(t *testing.T) {
		w := NewPodWatcher(nil, &recordingEmitter{}, logr.Discard(), PodWatcherConfig{
			AgentName:       "a",
			WatchNamespaces: []string{"production"},
		})
		if w.shouldWatchNamespace("development") {
			t.Error("expected false for unwatched namespace")
		}
	})
}

// ── detectOOMKilled ───────────────────────────────────────────────────────────

func TestDetectOOMKilled_EmitsOnOOMReason(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-a", UID: "uid-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 1,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137,
				Reason:   "OOMKilled",
			}},
		}}},
	}

	w.detectOOMKilled(nil, pod)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 OOMKilledEvent, got %d", len(em.events))
	}
	if em.events[0].Type() != EventTypeOOMKilled {
		t.Errorf("got %s, want OOMKilled", em.events[0].Type())
	}
}

func TestDetectOOMKilled_SkipsNonOOMReason(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	// exit code 137 with reason="Error" (manual SIGKILL) — must NOT emit OOMKilledEvent
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-b", UID: "uid-2"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 1,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137,
				Reason:   "Error",
			}},
		}}},
	}

	w.detectOOMKilled(nil, pod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events for non-OOMKilled reason, got %d", len(em.events))
	}
}

func TestDetectOOMKilled_DedupSameRestartCount(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	terminated := &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-c", UID: "uid-3"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "app",
			RestartCount:         2,
			LastTerminationState: corev1.ContainerState{Terminated: terminated},
		}}},
	}
	newPod := oldPod.DeepCopy() // same restart count

	w.detectOOMKilled(oldPod, newPod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events when restart count unchanged, got %d", len(em.events))
	}
}

// ── detectImagePullBackOff ────────────────────────────────────────────────────

func TestDetectImagePullBackOff_EmitsForNewReason(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-a", UID: "uid-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}},
	}

	w.detectImagePullBackOff(nil, pod)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 ImagePullBackOffEvent, got %d", len(em.events))
	}
	if em.events[0].Type() != EventTypeImagePullBackOff {
		t.Errorf("got %s, want ImagePullBackOff", em.events[0].Type())
	}
}

func TestDetectImagePullBackOff_EmitsForErrImagePull(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-b", UID: "uid-2"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: imagePullErrReason}},
		}}},
	}

	w.detectImagePullBackOff(nil, pod)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 event for ErrImagePull, got %d", len(em.events))
	}
	got := em.events[0].(ImagePullBackOffEvent)
	if got.Reason != imagePullErrReason {
		t.Errorf("reason: got %q, want ErrImagePull", got.Reason)
	}
}

func TestDetectImagePullBackOff_DedupSameReason(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-c", UID: "uid-3"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}},
	}
	newPod := oldPod.DeepCopy()

	w.detectImagePullBackOff(oldPod, newPod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events for same reason in old+new pod, got %d", len(em.events))
	}
}

// ── detectCrashLoop — below threshold / no state ──────────────────────────────

func TestDetectCrashLoop_BelowThreshold(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:                 "agent-a",
		CrashLoopRestartThreshold: 3,
	})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-a", UID: "uid-1"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 2, // one below threshold
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: string(EventTypeCrashLoopBackOff),
			}},
		}}},
	}

	w.detectCrashLoop(nil, pod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events below restart threshold, got %d", len(em.events))
	}
}

func TestDetectCrashLoop_NoCrashLoopState(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:                 "agent-a",
		CrashLoopRestartThreshold: 3,
	})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-b", UID: "uid-2"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 5,
			// State is Running, not CrashLoopBackOff
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}

	w.detectCrashLoop(nil, pod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events when not in CrashLoopBackOff state, got %d", len(em.events))
	}
}

func TestDetectCrashLoop_DedupSameRestartCount(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:                 "agent-a",
		CrashLoopRestartThreshold: 3,
	})
	w.clock = func() time.Time { return now }

	waitingState := corev1.ContainerStateWaiting{Reason: string(EventTypeCrashLoopBackOff)}
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-c", UID: "uid-3"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "app",
			RestartCount: 4,
			State:        corev1.ContainerState{Waiting: &waitingState},
		}}},
	}
	newPod := oldPod.DeepCopy() // same restart count, same state

	w.detectCrashLoop(oldPod, newPod)

	if len(em.events) != 0 {
		t.Errorf("expected 0 events when already in CrashLoopBackOff with same restart count, got %d", len(em.events))
	}
}

// ── onPodDelete ───────────────────────────────────────────────────────────────

func TestOnPodDelete_EmitsPodDeletedEvent(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-a", UID: "uid-1"},
	}

	w.onPodDelete(pod)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 PodDeletedEvent, got %d", len(em.events))
	}
	if em.events[0].Type() != EventTypePodDeleted {
		t.Errorf("got %s, want PodDeleted", em.events[0].Type())
	}
}

func TestOnPodDelete_CleansUpInternalState(t *testing.T) {
	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "agent-a"})
	w.clock = func() time.Time { return now }

	uid := types.UID("uid-cleanup")
	w.mu.Lock()
	w.pendingAlerted[uid] = true
	w.graceAlerted[uid] = true
	w.readySince[uid] = now
	w.healthyAlerted[uid] = true
	w.mu.Unlock()

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-x", UID: uid}}
	w.onPodDelete(pod)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pendingAlerted[uid] || w.graceAlerted[uid] || w.readySince[uid] != (time.Time{}) || w.healthyAlerted[uid] {
		t.Error("expected all internal state for the pod to be cleaned up after delete")
	}
}

// ── detectGracePeriodViolation ────────────────────────────────────────────────

func TestDetectGracePeriodViolation_NoDeletionTimestamp(t *testing.T) {
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})

	uid := types.UID("uid-grace-1")
	// pre-populate the alerted state; it should be cleared
	w.graceAlerted[uid] = true

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-a", UID: uid}}
	w.detectGracePeriodViolation(pod)

	if len(em.events) != 0 {
		t.Errorf("expected no event when DeletionTimestamp is nil, got %d", len(em.events))
	}
	w.mu.Lock()
	alerted := w.graceAlerted[uid]
	w.mu.Unlock()
	if alerted {
		t.Error("expected graceAlerted to be cleared when DeletionTimestamp is nil")
	}
}

func TestDetectGracePeriodViolation_WithinGracePeriod(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})
	w.clock = func() time.Time { return now }

	deletionTime := metav1.NewTime(now.Add(-5 * time.Second)) // deleted 5s ago
	graceSec := int64(30)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:                  "dev",
			Name:                       "pod-b",
			UID:                        "uid-grace-2",
			DeletionTimestamp:          &deletionTime,
			DeletionGracePeriodSeconds: &graceSec,
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	w.detectGracePeriodViolation(pod)
	if len(em.events) != 0 {
		t.Errorf("expected no event when still within grace period, got %d", len(em.events))
	}
}

func TestDetectGracePeriodViolation_PastDeadlineEmitsEvent(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})
	w.clock = func() time.Time { return now }

	graceSec := int64(10)
	deletionTime := metav1.NewTime(now.Add(-30 * time.Second)) // deleted 30s ago, grace=10s → overdue
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:                  "dev",
			Name:                       "pod-c",
			UID:                        "uid-grace-3",
			DeletionTimestamp:          &deletionTime,
			DeletionGracePeriodSeconds: &graceSec,
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	w.detectGracePeriodViolation(pod)
	if len(em.events) != 1 {
		t.Fatalf("expected 1 GracePeriodViolationEvent, got %d", len(em.events))
	}
	if em.events[0].Type() != EventTypeGracePeriodViolation {
		t.Errorf("expected GracePeriodViolation, got %s", em.events[0].Type())
	}
}

func TestDetectGracePeriodViolation_DedupPreventsDoubleEmit(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})
	w.clock = func() time.Time { return now }

	graceSec := int64(5)
	deletionTime := metav1.NewTime(now.Add(-20 * time.Second))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:                  "dev",
			Name:                       "pod-d",
			UID:                        "uid-grace-4",
			DeletionTimestamp:          &deletionTime,
			DeletionGracePeriodSeconds: &graceSec,
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	w.detectGracePeriodViolation(pod) // first: should emit
	w.detectGracePeriodViolation(pod) // second: dedup gate should block
	if len(em.events) != 1 {
		t.Errorf("expected exactly 1 event (dedup), got %d", len(em.events))
	}
}

// ── trackReadyState edge cases ────────────────────────────────────────────────

func TestTrackReadyState_NotReadyClearsState(t *testing.T) {
	now := time.Date(2026, 3, 14, 16, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	uid := types.UID("uid-notready")
	w.mu.Lock()
	w.readySince[uid] = now.Add(-5 * time.Minute)
	w.healthyAlerted[uid] = true
	w.mu.Unlock()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-x", UID: uid},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}
	w.trackReadyState(nil, pod)

	w.mu.Lock()
	_, hasReady := w.readySince[uid]
	alerted := w.healthyAlerted[uid]
	w.mu.Unlock()

	if hasReady {
		t.Error("expected readySince to be cleared for not-ready pod")
	}
	if alerted {
		t.Error("expected healthyAlerted to be cleared for not-ready pod")
	}
	if len(em.events) != 0 {
		t.Errorf("expected no events for not-ready pod, got %d", len(em.events))
	}
}

func TestTrackReadyState_StabilityWindowNotYetMet(t *testing.T) {
	now := time.Date(2026, 3, 14, 16, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		ReadyStabilityWindow: 5 * time.Minute,
	})
	w.clock = func() time.Time { return now }

	// Pod became ready only 1 minute ago — window is 5 minutes.
	pod := readyPod("dev", "pod-y", "uid-recent", now.Add(-1*time.Minute))
	w.trackReadyState(nil, pod)

	if len(em.events) != 0 {
		t.Errorf("expected no event before stability window, got %d", len(em.events))
	}
}

func TestTrackReadyState_OldNotReadyTransitionUpdatesTimer(t *testing.T) {
	now := time.Date(2026, 3, 14, 16, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	uid := types.UID("uid-transition")
	// Simulate "new" pod that just became ready (ready since = now, not long enough).
	newPod := readyPod("dev", "pod-z", string(uid), now.Add(-10*time.Second))
	// oldPod was NOT ready.
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "pod-z", UID: uid},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}

	// Pre-populate readySince with a far-past time (simulating prior ready state).
	w.mu.Lock()
	w.readySince[uid] = now.Add(-10 * time.Minute)
	w.mu.Unlock()

	// Because oldPod was not-ready, the transition should reset readySince to now.
	w.trackReadyState(oldPod, newPod)

	// After reset, window not met → no event.
	if len(em.events) != 0 {
		t.Errorf("expected no event because timer was reset on ready-transition, got %d", len(em.events))
	}
}

// ── onPodAdd / onPodUpdate ────────────────────────────────────────────────────

func TestOnPodAdd_SkipsUnwatchedNamespace(t *testing.T) {
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:       "ag",
		WatchNamespaces: []string{"production"},
	})

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "staging", Name: "pod-x"}}
	w.onPodAdd(pod)
	if len(em.events) != 0 {
		t.Errorf("expected no events for unwatched namespace, got %d", len(em.events))
	}
}

func TestOnPodUpdate_SkipsUnwatchedNamespace(t *testing.T) {
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:       "ag",
		WatchNamespaces: []string{"production"},
	})

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "staging", Name: "pod-y"}}
	w.onPodUpdate(nil, pod)
	if len(em.events) != 0 {
		t.Errorf("expected no events for unwatched namespace, got %d", len(em.events))
	}
}

// ── detectPodHealthy ──────────────────────────────────────────────────────────

func TestDetectPodHealthy_EmitsWhenStable(t *testing.T) {
	now := time.Date(2026, 3, 14, 17, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	// Pod ready for 5 minutes — exceeds 1-minute stability window.
	pod := readyPod("dev", "stable-pod", "uid-stable", now.Add(-5*time.Minute))
	w.detectPodHealthy(nil, pod)

	if len(em.events) != 1 {
		t.Fatalf("expected 1 PodHealthyEvent, got %d", len(em.events))
	}
}

func TestOnPodAdd_WatchedNamespaceCallsDetectors(t *testing.T) {
	now := time.Date(2026, 3, 14, 18, 0, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		WatchNamespaces:      []string{"production"},
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	// A running+ready pod, ready for 5 minutes — should trigger a PodHealthyEvent.
	pod := readyPod("production", "stable-pod", "uid-stable-add", now.Add(-5*time.Minute))
	w.onPodAdd(pod)

	// At least detectPodHealthy ran and the stability window is satisfied.
	found := false
	for _, ev := range em.events {
		if ev.Type() == EventTypePodHealthy {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected PodHealthyEvent from onPodAdd for stable pod in watched namespace")
	}
}

func TestOnPodUpdate_WatchedNamespaceCallsDetectors(t *testing.T) {
	now := time.Date(2026, 3, 14, 18, 5, 0, 0, time.UTC)
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{
		AgentName:            "ag",
		WatchNamespaces:      []string{"production"},
		ReadyStabilityWindow: time.Minute,
	})
	w.clock = func() time.Time { return now }

	newPod := readyPod("production", "stable-pod-2", "uid-stable-upd", now.Add(-5*time.Minute))
	w.onPodUpdate(nil, newPod)

	found := false
	for _, ev := range em.events {
		if ev.Type() == EventTypePodHealthy {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected PodHealthyEvent from onPodUpdate for stable pod in watched namespace")
	}
}
