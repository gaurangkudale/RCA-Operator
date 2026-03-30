package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// ── findRCAAgentsForSecret ────────────────────────────────────────────────────

func TestFindRCAAgentsForSecret_ReturnsMatchingAgents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	// Two agents: one references "my-secret", one references something else.
	agentA := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a", Namespace: "dev"},
		Spec: rcav1alpha1.RCAAgentSpec{
			Notifications: &rcav1alpha1.NotificationsConfig{
				Slack: &rcav1alpha1.SlackConfig{WebhookSecretRef: "my-secret", Channel: "#incidents"},
			},
		},
	}
	agentB := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-b", Namespace: "dev"},
		Spec: rcav1alpha1.RCAAgentSpec{
			Notifications: &rcav1alpha1.NotificationsConfig{
				Slack: &rcav1alpha1.SlackConfig{WebhookSecretRef: "other-secret", Channel: "#incidents"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agentA, agentB).Build()
	r := &RCAAgentReconciler{Client: c}

	secretObj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "dev"}}
	requests := r.findRCAAgentsForSecret(context.Background(), secretObj)
	if len(requests) != 1 {
		t.Fatalf("expected 1 reconcile request, got %d", len(requests))
	}
	if requests[0].Name != "agent-a" {
		t.Errorf("expected request for agent-a, got %q", requests[0].Name)
	}
}

func TestFindRCAAgentsForSecret_ReturnsEmptyWhenNoMatch(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &RCAAgentReconciler{Client: c}

	secretObj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated-secret", Namespace: "dev"}}
	requests := r.findRCAAgentsForSecret(context.Background(), secretObj)
	if len(requests) != 0 {
		t.Errorf("expected 0 requests when no agents reference the secret, got %d", len(requests))
	}
}
