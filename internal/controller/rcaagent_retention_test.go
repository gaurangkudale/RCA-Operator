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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

func TestShouldPruneIncidentReport(t *testing.T) {
	now := time.Date(2026, 3, 11, 19, 0, 0, 0, time.UTC)

	resolvedOld := &rcav1alpha1.IncidentReport{Status: rcav1alpha1.IncidentReportStatus{Phase: "Resolved", ResolvedTime: ptrTime(metav1.NewTime(now.Add(-2 * time.Hour)))}}
	if !shouldPruneIncidentReport(resolvedOld, now, time.Hour) {
		t.Fatal("expected old resolved incident to be pruned")
	}

	resolvedRecent := &rcav1alpha1.IncidentReport{Status: rcav1alpha1.IncidentReportStatus{Phase: "Resolved", ResolvedTime: ptrTime(metav1.NewTime(now.Add(-30 * time.Minute)))}}
	if shouldPruneIncidentReport(resolvedRecent, now, time.Hour) {
		t.Fatal("expected recent resolved incident to be kept")
	}

	active := &rcav1alpha1.IncidentReport{Status: rcav1alpha1.IncidentReportStatus{Phase: "Active", ResolvedTime: ptrTime(metav1.NewTime(now.Add(-24 * time.Hour)))}}
	if shouldPruneIncidentReport(active, now, time.Hour) {
		t.Fatal("expected active incident to be kept")
	}

	// Empty-phase (zombie) incidents older than retention should be pruned.
	zombieOld := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
		Status:     rcav1alpha1.IncidentReportStatus{Phase: ""},
	}
	if !shouldPruneIncidentReport(zombieOld, now, time.Hour) {
		t.Fatal("expected old zombie incident to be pruned")
	}

	// Empty-phase incidents created recently should be kept.
	zombieRecent := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute))},
		Status:     rcav1alpha1.IncidentReportStatus{Phase: ""},
	}
	if shouldPruneIncidentReport(zombieRecent, now, time.Hour) {
		t.Fatal("expected recent zombie incident to be kept")
	}
}

func TestCleanupResolvedIncidents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 11, 19, 30, 0, 0, time.UTC)
	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec: rcav1alpha1.RCAAgentSpec{
			WatchNamespaces:   []string{"development"},
			IncidentRetention: "1h",
		},
	}

	oldResolved := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "old-resolved", Namespace: "development"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Resolved",
			ResolvedTime: ptrTime(metav1.NewTime(now.Add(-2 * time.Hour))),
		},
	}
	recentResolved := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "recent-resolved", Namespace: "development"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Resolved",
			ResolvedTime: ptrTime(metav1.NewTime(now.Add(-15 * time.Minute))),
		},
	}
	active := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "development"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase: "Active",
		},
	}
	otherAgent := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "other-agent", Namespace: "development"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "agent-b"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Resolved",
			ResolvedTime: ptrTime(metav1.NewTime(now.Add(-4 * time.Hour))),
		},
	}
	// zombie: empty phase, belongs to agent-a, older than retention window.
	zombie := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "zombie",
			Namespace:         "development",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels:            map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec:   rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{Phase: ""},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, oldResolved, recentResolved, active, otherAgent, zombie).
		Build()

	reconciler := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := reconciler.cleanupResolvedIncidents(context.Background(), agent); err != nil {
		t.Fatalf("cleanupResolvedIncidents returned error: %v", err)
	}

	notFound := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "old-resolved", Namespace: "development"}, notFound); err == nil {
		t.Fatal("expected expired resolved incident to be deleted")
	}

	stillThere := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "recent-resolved", Namespace: "development"}, stillThere); err != nil {
		t.Fatalf("expected recent resolved incident to remain: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "active", Namespace: "development"}, stillThere); err != nil {
		t.Fatalf("expected active incident to remain: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "other-agent", Namespace: "development"}, stillThere); err != nil {
		t.Fatalf("expected other-agent incident to remain: %v", err)
	}

	// zombie should be pruned — it's old and has no phase.
	if err := c.Get(context.Background(), types.NamespacedName{Name: "zombie", Namespace: "development"}, notFound); err == nil {
		t.Fatal("expected zombie (empty-phase) incident to be deleted")
	}
}

func ptrTime(in metav1.Time) *metav1.Time {
	return &in
}

// ── ResourceSaturation TTL auto-resolve tests ─────────────────────────────────

func TestResolveStaleThrottlingIncidents_ResolvesExpiredIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	// Last-seen is 15 minutes ago — older than the 10-minute TTL.
	lastSeen := now.Add(-15 * time.Minute)

	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"development"}},
	}
	staleIncident := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resourcesaturation-cpu-pod-abc",
			Namespace: "development",
			Annotations: map[string]string{
				annotationLastSeen: lastSeen.UTC().Format(time.RFC3339),
			},
			Labels: map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "ResourceSaturation",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, staleIncident).
		Build()

	reconciler := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := reconciler.resolveStaleThrottlingIncidents(context.Background(), agent); err != nil {
		t.Fatalf("resolveStaleThrottlingIncidents: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: staleIncident.Name, Namespace: "development"}, updated); err != nil {
		t.Fatalf("failed to fetch updated incident: %v", err)
	}
	if updated.Status.Phase != "Resolved" {
		t.Errorf("phase: got %q, want Resolved", updated.Status.Phase)
	}
	if updated.Status.ResolvedTime == nil {
		t.Error("expected ResolvedTime to be set")
	}
	if len(updated.Status.Timeline) == 0 {
		t.Error("expected timeline entry for auto-resolve")
	}
}

func TestResolveStaleThrottlingIncidents_KeepsRecentIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	// Last-seen is only 3 minutes ago — within the 10-minute TTL.
	lastSeen := now.Add(-3 * time.Minute)

	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"development"}},
	}
	recentIncident := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resourcesaturation-cpu-pod-xyz",
			Namespace: "development",
			Annotations: map[string]string{
				annotationLastSeen: lastSeen.UTC().Format(time.RFC3339),
			},
			Labels: map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "ResourceSaturation",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, recentIncident).
		Build()

	reconciler := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := reconciler.resolveStaleThrottlingIncidents(context.Background(), agent); err != nil {
		t.Fatalf("resolveStaleThrottlingIncidents: %v", err)
	}

	kept := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: recentIncident.Name, Namespace: "development"}, kept); err != nil {
		t.Fatalf("failed to fetch incident: %v", err)
	}
	if kept.Status.Phase != "Active" {
		t.Errorf("phase: got %q, want Active (should not have been resolved)", kept.Status.Phase)
	}
}

func TestResolveStaleThrottlingIncidents_IgnoresNonResourceSaturation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-20 * time.Minute)

	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"development"}},
	}
	// CrashLoop incident — should NOT be affected by the throttling TTL resolver.
	crashLoop := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crashloop-my-pod-abc",
			Namespace: "development",
			Annotations: map[string]string{
				annotationLastSeen: lastSeen.UTC().Format(time.RFC3339),
			},
			Labels: map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "CrashLoop",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, crashLoop).
		Build()

	reconciler := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := reconciler.resolveStaleThrottlingIncidents(context.Background(), agent); err != nil {
		t.Fatalf("resolveStaleThrottlingIncidents: %v", err)
	}

	kept := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: crashLoop.Name, Namespace: "development"}, kept); err != nil {
		t.Fatalf("failed to fetch incident: %v", err)
	}
	if kept.Status.Phase != "Active" {
		t.Errorf("non-ResourceSaturation incident should remain Active, got %q", kept.Status.Phase)
	}
}
