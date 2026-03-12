package correlator

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func TestHandleEventResolvesActiveIncidentWhenPodIsHealthy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 11, 18, 30, 0, 0, time.UTC)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "development",
			Name:      "flaky-app-demo",
			UID:       types.UID("pod-1"),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
			}},
		},
	}
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crashloop-flaky-app-demo-z5ffx",
			Namespace: "development",
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "agent-a"},
		Status: rcav1alpha1.IncidentReportStatus{
			Severity:     "P3",
			Phase:        "Active",
			IncidentType: "CrashLoop",
			AffectedResources: []rcav1alpha1.AffectedResource{{
				Kind:      "Pod",
				Name:      "flaky-app-demo",
				Namespace: "development",
			}},
			Timeline: []rcav1alpha1.TimelineEvent{{
				Time:  metav1.NewTime(now.Add(-10 * time.Minute)),
				Event: "CrashLoopBackOff restarts=3 threshold=3",
			}},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(pod, report).
		Build()

	consumer := NewConsumer(client, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	err := consumer.handleEvent(context.Background(), watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{
		At:        now,
		AgentName: "agent-a",
		Namespace: "development",
		PodName:   "flaky-app-demo",
		PodUID:    "pod-1",
	}})
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: report.Name, Namespace: report.Namespace}, updated); err != nil {
		t.Fatalf("failed to fetch updated incident report: %v", err)
	}

	if updated.Status.Phase != "Resolved" {
		t.Fatalf("expected incident phase Resolved, got %q", updated.Status.Phase)
	}
	if updated.Status.ResolvedTime == nil {
		t.Fatal("expected resolved time to be set")
	}
	if len(updated.Status.Timeline) == 0 {
		t.Fatal("expected timeline to include resolve entry")
	}
}

func TestMapEventForExitCodeAndGracePeriodViolation(t *testing.T) {
	namespace, pod, agent, incidentType, severity, summary := mapEvent(watcher.ContainerExitCodeEvent{
		BaseEvent:   watcher.BaseEvent{Namespace: "development", PodName: "svc", AgentName: "agent-a"},
		ExitCode:    127,
		Category:    "CommandNotFound",
		Reason:      "Error",
		Description: "Command not found",
	})
	if namespace != "development" || pod != "svc" || agent != "agent-a" {
		t.Fatalf("unexpected mapping for exit-code event: namespace=%s pod=%s agent=%s", namespace, pod, agent)
	}
	if incidentType != "ExitCode" || severity != "P3" {
		t.Fatalf("unexpected incident mapping for exit-code event: type=%s severity=%s", incidentType, severity)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary for exit-code event")
	}

	namespace, pod, agent, incidentType, severity, summary = mapEvent(watcher.GracePeriodViolationEvent{
		BaseEvent:          watcher.BaseEvent{Namespace: "development", PodName: "svc", AgentName: "agent-a"},
		GracePeriodSeconds: 30,
		OverdueFor:         15 * time.Second,
	})
	if namespace != "development" || pod != "svc" || agent != "agent-a" {
		t.Fatalf("unexpected mapping for grace-period event: namespace=%s pod=%s agent=%s", namespace, pod, agent)
	}
	if incidentType != "GracePeriodViolation" || severity != "P2" {
		t.Fatalf("unexpected incident mapping for grace-period event: type=%s severity=%s", incidentType, severity)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary for grace-period event")
	}
}
