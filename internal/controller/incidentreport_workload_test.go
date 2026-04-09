package controller

import (
	"context"
	"testing"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ptr32 returns a pointer to the given int32 — convenience for replica fields.
func ptr32(v int32) *int32 { return &v }

// ─────────────────────────────────────────────────────────────────────────────
// nodeIncidentStillPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestNodeIncidentStillPresent_NotReady(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}
	r := makeReconciler(t, time.Now(), node)
	still, err := r.nodeIncidentStillPresent(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected incident still present when NodeReady=False")
	}
}

func TestNodeIncidentStillPresent_Ready(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	r := makeReconciler(t, time.Now(), node)
	still, err := r.nodeIncidentStillPresent(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected incident resolved when NodeReady=True")
	}
}

func TestNodeIncidentStillPresent_MemoryPressure(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
			},
		},
	}
	r := makeReconciler(t, time.Now(), node)
	still, err := r.nodeIncidentStillPresent(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected incident still present when MemoryPressure=True")
	}
}

func TestNodeIncidentStillPresent_NodeMissing(t *testing.T) {
	r := makeReconciler(t, time.Now()) // no node seeded
	still, err := r.nodeIncidentStillPresent(context.Background(), "node-gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false when node does not exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// statefulSetIncidentStillPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestStatefulSetIncidentStillPresent_Stalled(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr32(3)},
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: "rev-1",
			UpdateRevision:  "rev-2",
			UpdatedReplicas: 1, // < 3 desired
		},
	}
	r := makeReconciler(t, time.Now(), sts)
	still, err := r.statefulSetIncidentStillPresent(context.Background(), "default", "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected incident still present for stalled StatefulSet rollout")
	}
}

func TestStatefulSetIncidentStillPresent_RolledOut(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr32(3)},
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: "rev-2",
			UpdateRevision:  "rev-2",
			UpdatedReplicas: 3,
		},
	}
	r := makeReconciler(t, time.Now(), sts)
	still, err := r.statefulSetIncidentStillPresent(context.Background(), "default", "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected incident resolved when StatefulSet fully rolled out")
	}
}

func TestStatefulSetIncidentStillPresent_Missing(t *testing.T) {
	r := makeReconciler(t, time.Now())
	still, err := r.statefulSetIncidentStillPresent(context.Background(), "default", "gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false when StatefulSet does not exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// daemonSetIncidentStillPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestDaemonSetIncidentStillPresent_Stalled(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "default"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 5,
			UpdatedNumberScheduled: 2, // < 5 desired
		},
	}
	r := makeReconciler(t, time.Now(), ds)
	still, err := r.daemonSetIncidentStillPresent(context.Background(), "default", "fluentd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected incident still present for stalled DaemonSet rollout")
	}
}

func TestDaemonSetIncidentStillPresent_AllUpdated(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "default"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 5,
			UpdatedNumberScheduled: 5,
		},
	}
	r := makeReconciler(t, time.Now(), ds)
	still, err := r.daemonSetIncidentStillPresent(context.Background(), "default", "fluentd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected incident resolved when DaemonSet fully updated")
	}
}

func TestDaemonSetIncidentStillPresent_Missing(t *testing.T) {
	r := makeReconciler(t, time.Now())
	still, err := r.daemonSetIncidentStillPresent(context.Background(), "default", "gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false when DaemonSet does not exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// jobIncidentStillPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestJobIncidentStillPresent_Failed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "batch-export", Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobFailed,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	r := makeReconciler(t, time.Now(), job)
	still, err := r.jobIncidentStillPresent(context.Background(), "default", "batch-export")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected incident still present for Failed Job")
	}
}

func TestJobIncidentStillPresent_Completed(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "batch-export", Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	r := makeReconciler(t, time.Now(), job)
	still, err := r.jobIncidentStillPresent(context.Background(), "default", "batch-export")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected incident resolved for completed Job")
	}
}

func TestJobIncidentStillPresent_Missing(t *testing.T) {
	r := makeReconciler(t, time.Now())
	still, err := r.jobIncidentStillPresent(context.Background(), "default", "gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false when Job does not exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// cronJobIncidentStillPresent
// ─────────────────────────────────────────────────────────────────────────────

func TestCronJobIncidentStillPresent_Exists(t *testing.T) {
	// CronJob exists → time-based window handles resolution, always false.
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "default"},
	}
	r := makeReconciler(t, time.Now(), cj)
	still, err := r.cronJobIncidentStillPresent(context.Background(), "default", "nightly")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false for existing CronJob (time-based resolution handles it)")
	}
}

func TestCronJobIncidentStillPresent_Missing(t *testing.T) {
	r := makeReconciler(t, time.Now())
	still, err := r.cronJobIncidentStillPresent(context.Background(), "default", "gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still {
		t.Error("expected false when CronJob does not exist")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// incidentStillPresent — routing via Scope.ResourceRef
// ─────────────────────────────────────────────────────────────────────────────

func TestIncidentStillPresent_NodeRef_NotReady(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-bad"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}
	report := &rcav1alpha1.IncidentReport{
		Spec: rcav1alpha1.IncidentReportSpec{
			Scope: rcav1alpha1.IncidentScope{
				ResourceRef: &rcav1alpha1.IncidentObjectRef{Kind: "Node", Name: "node-bad"},
			},
		},
	}
	r := makeReconciler(t, time.Now(), node)
	still, err := r.incidentStillPresent(context.Background(), report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected still present for NotReady node via ResourceRef")
	}
}

func TestIncidentStillPresent_StatefulSetRef_Stalled(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr32(2)},
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: "v1",
			UpdateRevision:  "v2",
			UpdatedReplicas: 0,
		},
	}
	report := &rcav1alpha1.IncidentReport{
		Spec: rcav1alpha1.IncidentReportSpec{
			Scope: rcav1alpha1.IncidentScope{
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					Kind:      "StatefulSet",
					Namespace: "default",
					Name:      "cache",
				},
			},
		},
	}
	r := makeReconciler(t, time.Now(), sts)
	still, err := r.incidentStillPresent(context.Background(), report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !still {
		t.Error("expected still present for stalled StatefulSet via ResourceRef")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AutoInvestigate — wiring test
// ─────────────────────────────────────────────────────────────────────────────

// TestTransitionToActive_AutoInvestigateNilSafe verifies the nil guard:
// with AutoInvestigate=true but Investigator=nil the reconciler must not panic.
// (The controller holds *rca.Investigator; importing rca here would create a
// cycle, so we test only the nil-guard path.)
func TestTransitionToActive_AutoInvestigateNilSafe(t *testing.T) {
	// With AutoInvestigate=true but Investigator=nil the goroutine should NOT fire.
	// This tests the nil-guard: `if r.AutoInvestigate && r.Investigator != nil`.
	now := lifecycleTestNow
	start := metav1.NewTime(now.Add(-2 * time.Minute))
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "ir-auto", Namespace: "default"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-x", IncidentType: "CrashLoop", Fingerprint: "fp"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseDetecting,
			IncidentType: "CrashLoop",
			Severity:     "P2",
			StartTime:    &start,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "gone-pod", Namespace: "default"},
			},
		},
	}
	// No pod seeded → incidentStillPresent returns false → resolves before Active.
	// Seed a pod so the incident can transition to Active.
	pod := pendingPod("default", "gone-pod")
	r := makeReconciler(t, now, report, pod)
	r.AutoInvestigate = true
	r.Investigator = nil // explicitly nil — guard must hold

	// Should not panic.
	reconcileNN(t, r, "default", "ir-auto")
}

// ─────────────────────────────────────────────────────────────────────────────
// reconcileResolved — refactored signature (returns error, not ctrl.Result)
// ─────────────────────────────────────────────────────────────────────────────

func TestReconcileResolved_RecordsMetricOnce(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "ir-res",
			Namespace:   "default",
			Annotations: map[string]string{},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a", IncidentType: "OOMKilled", Fingerprint: "fp"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "OOMKilled",
			Severity:     "P2",
			Notified:     false, // no notification to send
		},
	}
	r := makeReconciler(t, lifecycleTestNow, report)

	// Call reconcileResolved directly (new signature: returns error only).
	if err := r.reconcileResolved(context.Background(), report); err != nil {
		t.Fatalf("reconcileResolved: %v", err)
	}

	// The resolved-metric annotation should now be set.
	got := &rcav1alpha1.IncidentReport{}
	if err := r.Get(context.Background(),
		types.NamespacedName{Namespace: "default", Name: "ir-res"}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations[resolvedMetricRecordedKey] != annotationTrue {
		t.Errorf("expected annotation %q=%q, got %q",
			resolvedMetricRecordedKey, annotationTrue, got.Annotations[resolvedMetricRecordedKey])
	}
}

func TestReconcileResolved_IdempotentMetric(t *testing.T) {
	// If the annotation is already set, no second patch should happen (idempotent).
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ir-res2",
			Namespace: "default",
			Annotations: map[string]string{
				resolvedMetricRecordedKey: annotationTrue,
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a", IncidentType: "OOMKilled", Fingerprint: "fp"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "OOMKilled",
			Severity:     "P2",
		},
	}
	r := makeReconciler(t, lifecycleTestNow, report)

	// Call twice — should not error or double-count.
	for i := range 2 {
		if err := r.reconcileResolved(context.Background(), report); err != nil {
			t.Fatalf("reconcileResolved call %d: %v", i+1, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// isPodReady helper
// ─────────────────────────────────────────────────────────────────────────────

func TestIsPodReady_NilPod(t *testing.T) {
	if isPodReady(nil) {
		t.Error("nil pod should not be ready")
	}
}

func TestIsPodReady_PendingPod(t *testing.T) {
	p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
	if isPodReady(p) {
		t.Error("Pending pod should not be ready")
	}
}

func TestIsPodReady_RunningNotReady(t *testing.T) {
	p := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			}},
		},
	}
	if isPodReady(p) {
		t.Error("Running pod with Ready=False should not be ready")
	}
}

func TestIsPodReady_FullyReady(t *testing.T) {
	p := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	if !isPodReady(p) {
		t.Error("Running pod with Ready=True should be ready")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// stabilizationWindow helper
// ─────────────────────────────────────────────────────────────────────────────

func TestStabilizationWindow_Default(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	if got := stabilizationWindow(report); got != 30*time.Second {
		t.Errorf("default stabilization window: got %v, want 30s", got)
	}
}

func TestStabilizationWindow_Custom(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			StabilizationWindowSeconds: 120,
		},
	}
	if got := stabilizationWindow(report); got != 120*time.Second {
		t.Errorf("custom stabilization window: got %v, want 120s", got)
	}
}

func TestStabilizationWindow_NilReport(t *testing.T) {
	if got := stabilizationWindow(nil); got != 30*time.Second {
		t.Errorf("nil report stabilization window: got %v, want 30s", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Reconcile — full-cycle via ResourceRef (StatefulSet, DaemonSet, Job)
// ─────────────────────────────────────────────────────────────────────────────

func reportWithWorkloadRef(ns, name, kind, resourceName string, now time.Time) *rcav1alpha1.IncidentReport {
	start := metav1.NewTime(now.Add(-2 * time.Minute))
	return &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "agent-a",
			IncidentType: "StalledRollout",
			Fingerprint:  "fp",
			Scope: rcav1alpha1.IncidentScope{
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					Kind:      kind,
					Namespace: ns,
					Name:      resourceName,
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseDetecting,
			IncidentType: "StalledRollout",
			Severity:     "P2",
			StartTime:    &start,
		},
	}
}

func TestReconcile_Detecting_StatefulSetStalled_TransitionsToActive(t *testing.T) {
	now := lifecycleTestNow
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr32(3)},
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: "v1",
			UpdateRevision:  "v2",
			UpdatedReplicas: 1,
		},
	}
	report := reportWithWorkloadRef("default", "ir-sts", "StatefulSet", "db", now)
	r := makeReconciler(t, now, report, sts)

	reconcileNN(t, r, "default", "ir-sts")

	got := fetchReport(t, r, "default", "ir-sts")
	if got.Status.Phase != phaseActive {
		t.Errorf("expected Active phase, got %q", got.Status.Phase)
	}
}

func TestReconcile_Detecting_JobFailed_TransitionsToActive(t *testing.T) {
	now := lifecycleTestNow
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "export", Namespace: "default"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:   batchv1.JobFailed,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	report := reportWithWorkloadRef("default", "ir-job", "Job", "export", now)
	r := makeReconciler(t, now, report, job)

	reconcileNN(t, r, "default", "ir-job")

	got := fetchReport(t, r, "default", "ir-job")
	if got.Status.Phase != phaseActive {
		t.Errorf("expected Active phase, got %q", got.Status.Phase)
	}
}

func TestReconcile_Detecting_DaemonSetStalled_TransitionsToActive(t *testing.T) {
	now := lifecycleTestNow
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "fluentd", Namespace: "default"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 4,
			UpdatedNumberScheduled: 1,
		},
	}
	report := reportWithWorkloadRef("default", "ir-ds", "DaemonSet", "fluentd", now)
	r := makeReconciler(t, now, report, ds)

	reconcileNN(t, r, "default", "ir-ds")

	got := fetchReport(t, r, "default", "ir-ds")
	if got.Status.Phase != phaseActive {
		t.Errorf("expected Active phase, got %q", got.Status.Phase)
	}
}
