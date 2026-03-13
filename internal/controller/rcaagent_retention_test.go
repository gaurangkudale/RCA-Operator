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

	active := &rcav1alpha1.IncidentReport{Status: rcav1alpha1.IncidentReportStatus{Phase: phaseActive, ResolvedTime: ptrTime(metav1.NewTime(now.Add(-24 * time.Hour)))}}
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
			Phase: phaseActive,
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
			Phase:        phaseActive,
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
			Phase:        phaseActive,
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
	if kept.Status.Phase != phaseActive {
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
			Phase:        phaseActive,
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
	if kept.Status.Phase != phaseActive {
		t.Errorf("non-ResourceSaturation incident should remain Active, got %q", kept.Status.Phase)
	}
}

// ── normalizeNamespaces ───────────────────────────────────────────────────────

func TestNormalizeNamespaces(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		if normalizeNamespaces(nil) != nil {
			t.Error("expected nil for nil input")
		}
	})
	t.Run("blank strings are dropped", func(t *testing.T) {
		got := normalizeNamespaces([]string{"", "dev", ""})
		if len(got) != 1 || got[0] != "dev" {
			t.Errorf("got %v, want [dev]", got)
		}
	})
	t.Run("duplicates are removed and result is sorted", func(t *testing.T) {
		got := normalizeNamespaces([]string{"staging", "development", "staging"})
		if len(got) != 2 {
			t.Fatalf("expected 2 unique entries, got %d: %v", len(got), got)
		}
		if got[0] != "development" || got[1] != "staging" {
			t.Errorf("expected sorted [development staging], got %v", got)
		}
	})
	t.Run("all-blank input returns nil", func(t *testing.T) {
		if normalizeNamespaces([]string{"", "  "}) != nil {
			t.Error("expected nil when all entries are whitespace")
		}
	})
}

// ── belongsToAgent ────────────────────────────────────────────────────────────

func TestBelongsToAgent(t *testing.T) {
	t.Run("matches via Spec.AgentRef", func(t *testing.T) {
		r := &rcav1alpha1.IncidentReport{Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"}}
		if !belongsToAgent(r, "agent-a") {
			t.Error("expected true when Spec.AgentRef matches")
		}
	})
	t.Run("matches via label", func(t *testing.T) {
		r := &rcav1alpha1.IncidentReport{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{incidentAgentLabelKey: "agent-b"}},
		}
		if !belongsToAgent(r, "agent-b") {
			t.Error("expected true when label matches")
		}
	})
	t.Run("no match", func(t *testing.T) {
		r := &rcav1alpha1.IncidentReport{Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-x"}}
		if belongsToAgent(r, "agent-y") {
			t.Error("expected false when agent names differ")
		}
	})
	t.Run("nil labels return false when Spec.AgentRef also differs", func(t *testing.T) {
		r := &rcav1alpha1.IncidentReport{}
		if belongsToAgent(r, "agent-a") {
			t.Error("expected false for empty report")
		}
	})
}

// ── validateSecret ────────────────────────────────────────────────────────────

func TestValidateSecret_FoundAndMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "development"},
	}
	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec: rcav1alpha1.RCAAgentSpec{
			AIProviderConfig: &rcav1alpha1.AIProviderConfig{SecretRef: "my-secret"},
		},
	}

	t.Run("secret exists — no error", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
		r := &RCAAgentReconciler{Client: c}
		if err := r.validateSecret(context.Background(), agent); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("secret missing — returns error", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &RCAAgentReconciler{Client: c}
		if err := r.validateSecret(context.Background(), agent); err == nil {
			t.Error("expected error when secret is missing")
		}
	})
}

// ── resolveOrphanedIncidents ──────────────────────────────────────────────────

func TestResolveOrphanedIncidents_ResolvesWhenPodGone(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 13, 0, 0, 0, time.UTC)
	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"development"}},
	}
	// Active incident referencing a pod that no longer exists in the cluster.
	orphan := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "crashloop-gone-pod-abc", Namespace: "development",
			Labels: map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "CrashLoop",
			AffectedResources: []rcav1alpha1.AffectedResource{{
				Kind: "Pod", Name: "gone-pod", Namespace: "development",
			}},
		},
	}

	// No Pod object in the fake store — simulates a deleted pod.
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, orphan).
		Build()

	r := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := r.resolveOrphanedIncidents(context.Background(), agent); err != nil {
		t.Fatalf("resolveOrphanedIncidents: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: orphan.Name, Namespace: "development"}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != "Resolved" {
		t.Errorf("phase: got %q, want Resolved", updated.Status.Phase)
	}
	if updated.Status.ResolvedTime == nil {
		t.Error("expected ResolvedTime to be set")
	}
}

func TestResolveOrphanedIncidents_KeepsWhenPodExists(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 13, 0, 0, 0, time.UTC)
	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "development"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"development"}},
	}
	livePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "live-pod", Namespace: "development"},
	}
	incident := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "crashloop-live-pod-abc", Namespace: "development",
			Labels: map[string]string{incidentAgentLabelKey: "agent-a"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "CrashLoop",
			AffectedResources: []rcav1alpha1.AffectedResource{{
				Kind: "Pod", Name: "live-pod", Namespace: "development",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, livePod, incident).
		Build()

	r := &RCAAgentReconciler{Client: c, nowFn: func() time.Time { return now }}
	if err := r.resolveOrphanedIncidents(context.Background(), agent); err != nil {
		t.Fatalf("resolveOrphanedIncidents: %v", err)
	}

	kept := &rcav1alpha1.IncidentReport{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: incident.Name, Namespace: "development"}, kept); err != nil {
		t.Fatalf("get: %v", err)
	}
	if kept.Status.Phase != phaseActive {
		t.Errorf("expected Active when pod still exists, got %q", kept.Status.Phase)
	}
}

// ── setCondition ──────────────────────────────────────────────────────────────

func TestSetCondition_SetsConditionOnAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-x", Namespace: "development"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.RCAAgent{}).
		WithObjects(agent).
		Build()

	r := &RCAAgentReconciler{Client: c}
	if err := r.setCondition(context.Background(), agent, "Ready", metav1.ConditionTrue, "AllGood", "everything is fine"); err != nil {
		t.Fatalf("setCondition: %v", err)
	}

	updated := &rcav1alpha1.RCAAgent{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "agent-x", Namespace: "development"}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	found := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Ready" {
			found = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("condition status: got %q, want True", cond.Status)
			}
			if cond.Reason != "AllGood" {
				t.Errorf("condition reason: got %q, want AllGood", cond.Reason)
			}
		}
	}
	if !found {
		t.Error("expected Ready condition to be present after setCondition")
	}
}

func TestSetCondition_ReturnsErrorWhenAgentMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &RCAAgentReconciler{Client: c}
	agent := &rcav1alpha1.RCAAgent{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "dev"}}

	if err := r.setCondition(context.Background(), agent, "Ready", metav1.ConditionTrue, "R", "m"); err == nil {
		t.Error("expected error when agent does not exist in the cluster")
	}
}

// ── validateNamespaces ────────────────────────────────────────────────────────

func TestValidateNamespaces_NoErrorForExistingAndMissingNamespaces(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "existing"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	r := &RCAAgentReconciler{Client: c}

	agent := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "ag", Namespace: "existing"},
		Spec: rcav1alpha1.RCAAgentSpec{
			WatchNamespaces: []string{"existing", "does-not-exist"},
		},
	}
	// validateNamespaces only logs; it must not panic or return an error.
	r.validateNamespaces(context.Background(), agent)
}

// ── retentionNamespaces ───────────────────────────────────────────────────────

func TestRetentionNamespaces_UsesSpecWhenSet(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &RCAAgentReconciler{Client: c}
	agent := &rcav1alpha1.RCAAgent{
		Spec: rcav1alpha1.RCAAgentSpec{
			WatchNamespaces: []string{"dev", "staging"},
		},
	}
	got, err := r.retentionNamespaces(context.Background(), agent)
	if err != nil {
		t.Fatalf("retentionNamespaces: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 namespaces, got %d: %v", len(got), got)
	}
}

func TestRetentionNamespaces_ListsAllWhenSpecEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-b"}},
	).Build()
	r := &RCAAgentReconciler{Client: c}
	agent := &rcav1alpha1.RCAAgent{}
	got, err := r.retentionNamespaces(context.Background(), agent)
	if err != nil {
		t.Fatalf("retentionNamespaces: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 namespaces from cluster list, got %d: %v", len(got), got)
	}
}
