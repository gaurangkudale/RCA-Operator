package watcher

import (
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
)

// ── toPod ─────────────────────────────────────────────────────────────────────

func TestToPod_DirectPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	got, ok := toPod(pod)
	if !ok || got != pod {
		t.Errorf("toPod(*Pod): got (%v, %v), want (pod, true)", got, ok)
	}
}

func TestToPod_DeletedFinalStateUnknown(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	tomb := toolscache.DeletedFinalStateUnknown{Key: "ns/p", Obj: pod}
	got, ok := toPod(tomb)
	if !ok || got != pod {
		t.Errorf("toPod(DeletedFinalState): got (%v, %v), want (pod, true)", got, ok)
	}
}

func TestToPod_WrongType(t *testing.T) {
	got, ok := toPod("not-a-pod")
	if ok || got != nil {
		t.Errorf("toPod(string): expected (nil, false), got (%v, %v)", got, ok)
	}
}

// ── toDeployment ─────────────────────────────────────────────────────────────

func TestToDeployment_DirectDeployment(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d"}}
	got, ok := toDeployment(dep)
	if !ok || got != dep {
		t.Errorf("toDeployment(*Deployment): got (%v, %v), want (dep, true)", got, ok)
	}
}

func TestToDeployment_DeletedFinalStateUnknown(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "d"}}
	tomb := toolscache.DeletedFinalStateUnknown{Key: "ns/d", Obj: dep}
	got, ok := toDeployment(tomb)
	if !ok || got != dep {
		t.Errorf("toDeployment(DeletedFinalState): got (%v, %v), want (dep, true)", got, ok)
	}
}

func TestToDeployment_WrongType(t *testing.T) {
	got, ok := toDeployment(42)
	if ok || got != nil {
		t.Errorf("toDeployment(int): expected (nil, false), got (%v, %v)", got, ok)
	}
}

// ── toNode ────────────────────────────────────────────────────────────────────

func TestToNode_DirectNode(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	got, ok := toNode(node)
	if !ok || got != node {
		t.Errorf("toNode(*Node): got (%v, %v), want (node, true)", got, ok)
	}
}

func TestToNode_DeletedFinalStateUnknown(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	tomb := toolscache.DeletedFinalStateUnknown{Key: "n", Obj: node}
	got, ok := toNode(tomb)
	if !ok || got != node {
		t.Errorf("toNode(DeletedFinalState): got (%v, %v), want (node, true)", got, ok)
	}
}

func TestToNode_WrongType(t *testing.T) {
	got, ok := toNode(struct{}{})
	if ok || got != nil {
		t.Errorf("toNode(struct{}): expected (nil, false), got (%v, %v)", got, ok)
	}
}

// ── updatePendingState ────────────────────────────────────────────────────────

func TestUpdatePendingState_ClearsAlertedWhenNotPending(t *testing.T) {
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})

	uid := types.UID("uid-pend")
	w.mu.Lock()
	w.pendingAlerted[uid] = true
	w.mu.Unlock()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: uid},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	w.updatePendingState(pod)

	w.mu.Lock()
	alerted := w.pendingAlerted[uid]
	w.mu.Unlock()
	if alerted {
		t.Error("expected pendingAlerted to be cleared when pod is not Pending")
	}
}

func TestUpdatePendingState_KeepsAlertedWhenPending(t *testing.T) {
	em := &recordingEmitter{}
	w := NewPodWatcher(nil, em, logr.Discard(), PodWatcherConfig{AgentName: "ag"})

	uid := types.UID("uid-still-pend")
	w.mu.Lock()
	w.pendingAlerted[uid] = true
	w.mu.Unlock()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: uid},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	w.updatePendingState(pod)

	w.mu.Lock()
	alerted := w.pendingAlerted[uid]
	w.mu.Unlock()
	if !alerted {
		t.Error("expected pendingAlerted to remain when pod is still Pending")
	}
}

// ── markPendingAlerted / clearPendingAlerted ──────────────────────────────────

func TestMarkAndClearPendingAlerted(t *testing.T) {
	w := NewPodWatcher(nil, &recordingEmitter{}, logr.Discard(), PodWatcherConfig{})
	uid := types.UID("uid-mark")

	if !w.markPendingAlerted(uid) {
		t.Error("first markPendingAlerted should return true")
	}
	if w.markPendingAlerted(uid) {
		t.Error("second markPendingAlerted should return false (already alerted)")
	}

	w.clearPendingAlerted(uid)
	if !w.markPendingAlerted(uid) {
		t.Error("markPendingAlerted should return true again after clear")
	}
}

// ── clearGraceAlerted ─────────────────────────────────────────────────────────

func TestClearGraceAlerted(t *testing.T) {
	w := NewPodWatcher(nil, &recordingEmitter{}, logr.Discard(), PodWatcherConfig{})
	uid := types.UID("uid-grace-clear")

	w.mu.Lock()
	w.graceAlerted[uid] = true
	w.mu.Unlock()

	w.clearGraceAlerted(uid)

	w.mu.Lock()
	alerted := w.graceAlerted[uid]
	w.mu.Unlock()
	if alerted {
		t.Error("expected graceAlerted to be cleared")
	}
}

// ── toK8sEvent ────────────────────────────────────────────────────────────────

func TestToK8sEvent_DirectEvent(t *testing.T) {
	ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e"}}
	got, ok := toK8sEvent(ev)
	if !ok || got != ev {
		t.Errorf("toK8sEvent(*Event): got (%v, %v), want (ev, true)", got, ok)
	}
}

func TestToK8sEvent_DeletedFinalStateUnknown(t *testing.T) {
	ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e"}}
	tomb := toolscache.DeletedFinalStateUnknown{Key: "ns/e", Obj: ev}
	got, ok := toK8sEvent(tomb)
	if !ok || got != ev {
		t.Errorf("toK8sEvent(DeletedFinalState): got (%v, %v), want (ev, true)", got, ok)
	}
}

func TestToK8sEvent_WrongType(t *testing.T) {
	got, ok := toK8sEvent(true)
	if ok || got != nil {
		t.Errorf("toK8sEvent(bool): expected (nil, false), got (%v, %v)", got, ok)
	}
}
