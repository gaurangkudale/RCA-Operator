package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

var lifecycleTestNow = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

func newLifecycleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("rcav1alpha1 scheme: %v", err)
	}
	return s
}

// makeReconciler returns a reconciler backed by a fake client seeded with objs.
func makeReconciler(t *testing.T, now time.Time, objs ...client.Object) *IncidentReportReconciler {
	t.Helper()
	s := newLifecycleScheme(t)
	b := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(objs...)
	return &IncidentReportReconciler{
		Client: b.Build(),
		Scheme: s,
		nowFn:  func() time.Time { return now },
	}
}

// reconcileNN calls Reconcile for the given namespace/name and returns the result.
func reconcileNN(t *testing.T, r *IncidentReportReconciler, ns, name string) ctrl.Result { //nolint:unparam
	t.Helper()
	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	return result
}

// fetchReport reads the IncidentReport from the reconciler's fake client.
func fetchReport(t *testing.T, r *IncidentReportReconciler, ns, name string) *rcav1alpha1.IncidentReport { //nolint:unparam
	t.Helper()
	got := &rcav1alpha1.IncidentReport{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, got); err != nil {
		t.Fatalf("failed to fetch IncidentReport %s/%s: %v", ns, name, err)
	}
	return got
}

// readyPod builds a Running+Ready pod.
func readyPod(ns, name string) *corev1.Pod { //nolint:unparam
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

// pendingPod builds a Pending (not ready) pod.
func pendingPod(ns, name string) *corev1.Pod { //nolint:unparam
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}

// detectingReport builds a Detecting-phase IncidentReport. startOffset is
// applied to now to compute StartTime (e.g. -10*time.Second = started 10s ago).
func detectingReport(ns, name, podName string, startOffset time.Duration, now time.Time) *rcav1alpha1.IncidentReport { //nolint:unparam
	start := metav1.NewTime(now.Add(startOffset))
	return &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseDetecting,
			IncidentType: "CrashLoop",
			Severity:     "P3",
			StartTime:    &start,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: podName, Namespace: ns},
			},
		},
	}
}

// activeReport builds an Active-phase IncidentReport. lastSeenOffset is applied
// to now to set the annotationLastSeen annotation.
func activeReport(ns, name, podName string, lastSeenOffset time.Duration, now time.Time) *rcav1alpha1.IncidentReport {
	start := metav1.NewTime(now.Add(-10 * time.Minute))
	return &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				annotationLastSeen: now.Add(lastSeenOffset).Format(time.RFC3339),
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "CrashLoop",
			Severity:     "P3",
			StartTime:    &start,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: podName, Namespace: ns},
			},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestReconcile_NotFound(t *testing.T) {
	r := makeReconciler(t, lifecycleTestNow) // no objects seeded
	result := reconcileNN(t, r, "default", "does-not-exist")
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for missing incident; got %v", result.RequeueAfter)
	}
}

func TestReconcile_Resolved_NoOp(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "resolved-incident", Namespace: "default"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "CrashLoop",
		},
	}
	r := makeReconciler(t, lifecycleTestNow, report)
	result := reconcileNN(t, r, "default", "resolved-incident")

	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for Resolved incident; got %v", result.RequeueAfter)
	}
	got := fetchReport(t, r, "default", "resolved-incident")
	if got.Status.Phase != phaseResolved {
		t.Errorf("Phase=%q; want Resolved", got.Status.Phase)
	}
}

// TestReconcile_Detecting_RequeuesWhileStabilizing verifies that an incident
// within the stabilisation window results in a RequeueAfter and stays Detecting.
func TestReconcile_Detecting_RequeuesWhileStabilizing(t *testing.T) {
	// Started 10 seconds ago — 20 seconds remain in the 30s window.
	report := detectingReport("default", "test-incident", "test-pod", -10*time.Second, lifecycleTestNow)
	pod := pendingPod("default", "test-pod")

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	if result.RequeueAfter <= 0 {
		t.Fatalf("expected positive RequeueAfter; got %v", result.RequeueAfter)
	}
	if result.RequeueAfter > stabilizationDelay {
		t.Errorf("RequeueAfter=%v should be <= stabilizationDelay=%v", result.RequeueAfter, stabilizationDelay)
	}
	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseDetecting {
		t.Errorf("Phase=%q; want Detecting (still stabilising)", got.Status.Phase)
	}
}

// TestReconcile_Detecting_TransitionsToActive verifies promotion to Active after
// the stabilisation window elapses with the pod still unhealthy.
func TestReconcile_Detecting_TransitionsToActive(t *testing.T) {
	// Started 45 seconds ago — past the 30s stabilisation window.
	report := detectingReport("default", "test-incident", "test-pod", -45*time.Second, lifecycleTestNow)
	pod := pendingPod("default", "test-pod")

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseActive {
		t.Errorf("Phase=%q; want Active", got.Status.Phase)
	}
	if result.RequeueAfter != healthyResolveWindow {
		t.Errorf("RequeueAfter=%v; want %v", result.RequeueAfter, healthyResolveWindow)
	}
	if len(got.Status.Timeline) == 0 {
		t.Error("expected at least one timeline entry for Active transition")
	}
}

// TestReconcile_Detecting_ResolvesIfPodHealthy verifies Detecting → Resolved
// transition when the stabilisation window has fully elapsed, the pod is
// Running+Ready, and no signal arrived during the window (clean recovery).
func TestReconcile_Detecting_ResolvesIfPodHealthy(t *testing.T) {
	// Started 45s ago — past the 30s window; no annotationLastSeen annotation.
	report := detectingReport("default", "test-incident", "test-pod", -45*time.Second, lifecycleTestNow)
	pod := readyPod("default", "test-pod")

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseResolved {
		t.Errorf("Phase=%q; want Resolved (window elapsed, pod healthy, no new signal)", got.Status.Phase)
	}
	if got.Status.ResolvedTime == nil {
		t.Error("expected ResolvedTime to be set")
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after resolve; got %v", result.RequeueAfter)
	}
}

// TestReconcile_Detecting_NoEarlyResolveForHealthyPod verifies that a healthy
// pod during the stabilisation window does NOT trigger early resolution. The full
// window must elapse before any decision is made. This prevents OOMKilled/
// CrashLoop pods (which briefly restart as Running+Ready) from being resolved
// within seconds of incident creation.
func TestReconcile_Detecting_NoEarlyResolveForHealthyPod(t *testing.T) {
	// Started 10s ago — still within the 30s window.
	report := detectingReport("default", "test-incident", "test-pod", -10*time.Second, lifecycleTestNow)
	pod := readyPod("default", "test-pod") // pod is Running+Ready

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	// Must requeue — no early resolve regardless of pod health.
	if result.RequeueAfter <= 0 {
		t.Fatalf("expected positive RequeueAfter; got %v", result.RequeueAfter)
	}
	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseDetecting {
		t.Errorf("Phase=%q; want Detecting (window not elapsed, early resolve suppressed)", got.Status.Phase)
	}
}

// TestReconcile_Detecting_ActivatesIfSignalDuringWindow verifies that after the
// stabilisation window, if annotationLastSeen is recent (a new watcher signal
// arrived during the window), the incident is promoted directly to Active without
// checking pod health. This handles OOM/CrashLoop pods that cycle slower than 30s.
func TestReconcile_Detecting_ActivatesIfSignalDuringWindow(t *testing.T) {
	// Started 45s ago — past the 30s window.
	report := detectingReport("default", "test-incident", "test-pod", -45*time.Second, lifecycleTestNow)
	// Signal arrived 10s ago (within the last stabilizationDelay).
	report.Annotations = map[string]string{
		annotationLastSeen: lifecycleTestNow.Add(-10 * time.Second).Format(time.RFC3339),
	}
	pod := readyPod("default", "test-pod") // even healthy pod must not cause early resolve

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseActive {
		t.Errorf("Phase=%q; want Active (signal during window → ongoing failure)", got.Status.Phase)
	}
}

// TestReconcile_Active_RequeuesIfSignalRecent verifies back-off when the last
// watcher signal arrived more recently than healthyResolveWindow.
func TestReconcile_Active_RequeuesIfSignalRecent(t *testing.T) {
	// Last signal 1 minute ago — 4 minutes remain.
	report := activeReport("default", "test-incident", "test-pod", -time.Minute, lifecycleTestNow)
	pod := readyPod("default", "test-pod")

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseActive {
		t.Errorf("Phase=%q; want Active (signal still recent)", got.Status.Phase)
	}
	if result.RequeueAfter <= 0 {
		t.Errorf("expected positive RequeueAfter; got %v", result.RequeueAfter)
	}
	if result.RequeueAfter > healthyResolveWindow {
		t.Errorf("RequeueAfter=%v; should be <= %v", result.RequeueAfter, healthyResolveWindow)
	}
}

// TestReconcile_Active_AutoResolvesAfterWindow verifies auto-resolve when no
// signal has arrived for healthyResolveWindow and the pod is healthy.
func TestReconcile_Active_AutoResolvesAfterWindow(t *testing.T) {
	// Last signal 6 minutes ago — beyond the 5-minute healthy resolve window.
	report := activeReport("default", "test-incident", "test-pod", -6*time.Minute, lifecycleTestNow)
	pod := readyPod("default", "test-pod")

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseResolved {
		t.Errorf("Phase=%q; want Resolved", got.Status.Phase)
	}
	if got.Status.ResolvedTime == nil {
		t.Error("expected ResolvedTime to be set")
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after auto-resolve; got %v", result.RequeueAfter)
	}
	if len(got.Status.Timeline) == 0 {
		t.Error("expected timeline entry for resolve")
	}
}

// TestReconcile_Active_StaysActiveIfPodUnhealthy verifies that the reconciler
// does NOT resolve when the window has passed but the pod is still unhealthy.
func TestReconcile_Active_StaysActiveIfPodUnhealthy(t *testing.T) {
	report := activeReport("default", "test-incident", "test-pod", -6*time.Minute, lifecycleTestNow)
	pod := pendingPod("default", "test-pod") // pod NOT ready

	r := makeReconciler(t, lifecycleTestNow, report, pod)
	result := reconcileNN(t, r, "default", "test-incident")

	got := fetchReport(t, r, "default", "test-incident")
	if got.Status.Phase != phaseActive {
		t.Errorf("Phase=%q; want Active (pod still unhealthy)", got.Status.Phase)
	}
	if result.RequeueAfter != healthyResolveWindow {
		t.Errorf("RequeueAfter=%v; want %v", result.RequeueAfter, healthyResolveWindow)
	}
}

// TestReconcile_Active_NodeFailure_AutoResolvesOnTTL verifies TTL-only resolution
// for NodeFailure incidents (no pod health check, node name stored as "pod" name).
func TestReconcile_Active_NodeFailure_AutoResolvesOnTTL(t *testing.T) {
	start := metav1.NewTime(lifecycleTestNow.Add(-20 * time.Minute))
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-incident",
			Namespace: "default",
			Annotations: map[string]string{
				annotationLastSeen: lifecycleTestNow.Add(-6 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "NodeNotReady",
			Severity:     "P1",
			StartTime:    &start,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "node-1", Namespace: "default"},
			},
		},
	}

	r := makeReconciler(t, lifecycleTestNow, report)
	result := reconcileNN(t, r, "default", "node-incident")

	got := fetchReport(t, r, "default", "node-incident")
	if got.Status.Phase != phaseResolved {
		t.Errorf("Phase=%q; want Resolved (NodeFailure TTL)", got.Status.Phase)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after resolve; got %v", result.RequeueAfter)
	}
}
