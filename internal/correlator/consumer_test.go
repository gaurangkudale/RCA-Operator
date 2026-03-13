package correlator

import (
	"context"
	"strings"
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

const testPhaseResolved = "Resolved"

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

	if updated.Status.Phase != testPhaseResolved {
		t.Fatalf("expected incident phase %s, got %q", testPhaseResolved, updated.Status.Phase)
	}
	if updated.Status.ResolvedTime == nil {
		t.Fatal("expected resolved time to be set")
	}
	if len(updated.Status.Timeline) == 0 {
		t.Fatal("expected timeline to include resolve entry")
	}
}

func TestMapEventForCrashLoopAndGracePeriodViolation(t *testing.T) {
	namespace, pod, agent, incidentType, severity, summary := mapEvent(watcher.CrashLoopBackOffEvent{
		BaseEvent:           watcher.BaseEvent{Namespace: "development", PodName: "svc", AgentName: "agent-a"},
		RestartCount:        4,
		Threshold:           3,
		LastExitCode:        126,
		ExitCodeCategory:    "PermissionDenied",
		ExitCodeDescription: "Command invoked cannot execute",
	})
	if namespace != "development" || pod != "svc" || agent != "agent-a" {
		t.Fatalf("unexpected mapping for crash-loop event: namespace=%s pod=%s agent=%s", namespace, pod, agent)
	}
	if incidentType != "CrashLoop" || severity != "P3" {
		t.Fatalf("unexpected incident mapping for crash-loop event: type=%s severity=%s", incidentType, severity)
	}
	if summary == "" || !strings.Contains(summary, "exitCode=126") || !strings.Contains(summary, "category=PermissionDenied") {
		t.Fatalf("expected crash-loop summary to include exit-code context, got %q", summary)
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

// ── StalledRollout ────────────────────────────────────────────────────────────

func TestMapEventForStalledRollout(t *testing.T) {
	ev := watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{Namespace: "development", AgentName: "agent-a"},
		DeploymentName:  "payment-service",
		Reason:          "ProgressDeadlineExceeded",
		DesiredReplicas: 3,
		ReadyReplicas:   0,
		Message:         "Deployment does not have minimum availability",
	}

	namespace, pod, agent, incidentType, severity, summary := mapEvent(ev)

	if namespace != "development" {
		t.Errorf("namespace: got %q, want %q", namespace, "development")
	}
	if pod != "payment-service" {
		t.Errorf("pod/resource: got %q, want %q", pod, "payment-service")
	}
	if agent != "agent-a" {
		t.Errorf("agent: got %q, want %q", agent, "agent-a")
	}
	if incidentType != "BadDeploy" {
		t.Errorf("incidentType: got %q, want %q", incidentType, "BadDeploy")
	}
	if severity != "P2" {
		t.Errorf("severity: got %q, want %q", severity, "P2")
	}
	for _, want := range []string{"ProgressDeadlineExceeded", "desiredReplicas=3", "readyReplicas=0"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q: got %q", want, summary)
		}
	}
}

func TestHandleEventCreatesStalledRolloutIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	consumer := NewConsumer(client, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	err := consumer.handleEvent(context.Background(), watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{At: now, AgentName: "agent-a", Namespace: "development"},
		DeploymentName:  "payment-service",
		Reason:          "ProgressDeadlineExceeded",
		DesiredReplicas: 3,
		ReadyReplicas:   0,
		Message:         "Deployment does not have minimum availability",
	})
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := client.List(context.Background(), list); err != nil {
		t.Fatalf("failed to list IncidentReports: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport, got %d", len(list.Items))
	}
	report := list.Items[0]
	if report.Status.IncidentType != "BadDeploy" {
		t.Errorf("incidentType: got %q, want BadDeploy", report.Status.IncidentType)
	}
	if report.Status.Severity != "P2" {
		t.Errorf("severity: got %q, want P2", report.Status.Severity)
	}
	if report.Status.Phase != phaseActive {
		t.Errorf("phase: got %q, want Active", report.Status.Phase)
	}
	if len(report.Status.AffectedResources) == 0 || report.Status.AffectedResources[0].Name != "payment-service" {
		t.Errorf("AffectedResources: expected payment-service, got %+v", report.Status.AffectedResources)
	}
	if !strings.HasPrefix(report.Name, "baddeploy-payment-service-") {
		t.Errorf("generated name prefix: got %q", report.Name)
	}
}

func TestHandleEventDedupsStalledRolloutOnRepeat(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	consumer := NewConsumer(cl, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	ev := watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{At: now, AgentName: "agent-a", Namespace: "development"},
		DeploymentName:  "payment-service",
		Reason:          "ProgressDeadlineExceeded",
		DesiredReplicas: 3,
		ReadyReplicas:   0,
	}

	// First event creates the incident.
	if err := consumer.handleEvent(context.Background(), ev); err != nil {
		t.Fatalf("first handleEvent: %v", err)
	}
	// Second event with the same deployment name must update (not create a duplicate).
	if err := consumer.handleEvent(context.Background(), ev); err != nil {
		t.Fatalf("second handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("dedup: expected exactly 1 IncidentReport, got %d", len(list.Items))
	}
	// The repeated signal should increment the signal counter.
	got := list.Items[0].Annotations[annotationSignalSeen]
	if got != "2" {
		t.Errorf("signal-count annotation: got %q, want \"2\"", got)
	}
}

// ── NodePressure ──────────────────────────────────────────────────────────────

func TestMapEventForNodePressure(t *testing.T) {
	cases := []struct {
		pressureType string
		wantSeverity string
	}{
		{"DiskPressure", "P2"},
		{"MemoryPressure", "P2"},
		{"PIDPressure", "P3"},
	}
	for _, tc := range cases {
		t.Run(tc.pressureType, func(t *testing.T) {
			ev := watcher.NodePressureEvent{
				BaseEvent:    watcher.BaseEvent{Namespace: "default", AgentName: "agent-a", NodeName: "worker-1"},
				PressureType: tc.pressureType,
				Message:      "threshold exceeded",
			}
			namespace, node, agent, incidentType, severity, summary := mapEvent(ev)

			if namespace != "default" {
				t.Errorf("namespace: got %q, want default", namespace)
			}
			if node != "worker-1" {
				t.Errorf("resource: got %q, want worker-1", node)
			}
			if agent != "agent-a" {
				t.Errorf("agent: got %q", agent)
			}
			if incidentType != "NodeFailure" {
				t.Errorf("incidentType: got %q, want NodeFailure", incidentType)
			}
			if severity != tc.wantSeverity {
				t.Errorf("severity: got %q, want %q", severity, tc.wantSeverity)
			}
			if !strings.Contains(summary, tc.pressureType) {
				t.Errorf("summary missing pressure type %q: %q", tc.pressureType, summary)
			}
		})
	}
}

// ── CPUThrottling ─────────────────────────────────────────────────────────────

func TestMapEventForCPUThrottling(t *testing.T) {
	ev := watcher.CPUThrottlingEvent{
		BaseEvent:     watcher.BaseEvent{Namespace: "development", PodName: "cpu-throttle-demo", AgentName: "agent-a"},
		ContainerName: "throttle-demo",
		Message:       "45% throttling of CPU",
	}

	namespace, pod, agent, incidentType, severity, summary := mapEvent(ev)

	if namespace != "development" {
		t.Errorf("namespace: got %q, want development", namespace)
	}
	if pod != "cpu-throttle-demo" {
		t.Errorf("pod: got %q, want cpu-throttle-demo", pod)
	}
	if agent != "agent-a" {
		t.Errorf("agent: got %q", agent)
	}
	if incidentType != "ResourceSaturation" {
		t.Errorf("incidentType: got %q, want ResourceSaturation", incidentType)
	}
	if severity != "P3" {
		t.Errorf("severity: got %q, want P3", severity)
	}
	if !strings.Contains(summary, "throttle-demo") {
		t.Errorf("summary missing container name: %q", summary)
	}
	if !strings.Contains(summary, "45% throttling") {
		t.Errorf("summary missing message: %q", summary)
	}
}

func TestHandleEventCreatesCPUThrottlingIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	consumer := NewConsumer(cl, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	err := consumer.handleEvent(context.Background(), watcher.CPUThrottlingEvent{
		BaseEvent:     watcher.BaseEvent{At: now, AgentName: "agent-a", Namespace: "development", PodName: "cpu-throttle-demo"},
		ContainerName: "throttle-demo",
		Message:       "45% throttling of CPU",
	})
	if err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport, got %d", len(list.Items))
	}
	report := list.Items[0]
	if report.Status.IncidentType != "ResourceSaturation" {
		t.Errorf("incidentType: got %q, want ResourceSaturation", report.Status.IncidentType)
	}
	if report.Status.Severity != "P3" {
		t.Errorf("severity: got %q, want P3", report.Status.Severity)
	}
	if report.Status.Phase != phaseActive {
		t.Errorf("phase: got %q, want Active", report.Status.Phase)
	}
	if !strings.HasPrefix(report.Name, "resourcesaturation-cpu-throttle-demo-") {
		t.Errorf("name prefix: got %q", report.Name)
	}
}
