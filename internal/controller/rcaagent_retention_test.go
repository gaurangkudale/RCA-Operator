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

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "development"}}, oldResolved, recentResolved, active, otherAgent).
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
}

func ptrTime(in metav1.Time) *metav1.Time {
	return &in
}
