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
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const testPhaseResolved = "Resolved"

const (
	testAgentA       = "agent-a"
	testNamespaceDev = "development"
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
			Namespace: testNamespaceDev,
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
			Namespace: testNamespaceDev,
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: testAgentA},
		Status: rcav1alpha1.IncidentReportStatus{
			Severity:     "P3",
			Phase:        "Active",
			IncidentType: "CrashLoopBackOff",
			AffectedResources: []rcav1alpha1.AffectedResource{{
				Kind:      "Pod",
				Name:      "flaky-app-demo",
				Namespace: testNamespaceDev,
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
		AgentName: testAgentA,
		Namespace: testNamespaceDev,
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
		BaseEvent:           watcher.BaseEvent{Namespace: testNamespaceDev, PodName: "svc", AgentName: testAgentA},
		RestartCount:        4,
		Threshold:           3,
		LastExitCode:        126,
		ExitCodeCategory:    "PermissionDenied",
		ExitCodeDescription: "Command invoked cannot execute",
	})
	if namespace != testNamespaceDev || pod != "svc" || agent != testAgentA {
		t.Fatalf("unexpected mapping for crash-loop event: namespace=%s pod=%s agent=%s", namespace, pod, agent)
	}
	if incidentType != "CrashLoopBackOff" || severity != "P3" {
		t.Fatalf("unexpected incident mapping for crash-loop event: type=%s severity=%s", incidentType, severity)
	}
	if summary == "" || !strings.Contains(summary, "exitCode=126") || !strings.Contains(summary, "category=PermissionDenied") {
		t.Fatalf("expected crash-loop summary to include exit-code context, got %q", summary)
	}

	namespace, pod, agent, incidentType, severity, summary = mapEvent(watcher.GracePeriodViolationEvent{
		BaseEvent:          watcher.BaseEvent{Namespace: testNamespaceDev, PodName: "svc", AgentName: testAgentA},
		GracePeriodSeconds: 30,
		OverdueFor:         15 * time.Second,
	})
	if namespace != testNamespaceDev || pod != "svc" || agent != testAgentA {
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
		BaseEvent:       watcher.BaseEvent{Namespace: testNamespaceDev, AgentName: testAgentA},
		DeploymentName:  "payment-service",
		Reason:          "ProgressDeadlineExceeded",
		DesiredReplicas: 3,
		ReadyReplicas:   0,
		Message:         "Deployment does not have minimum availability",
	}

	namespace, pod, agent, incidentType, severity, summary := mapEvent(ev)

	if namespace != testNamespaceDev {
		t.Errorf("namespace: got %q, want %q", namespace, testNamespaceDev)
	}
	if pod != "payment-service" {
		t.Errorf("pod/resource: got %q, want %q", pod, "payment-service")
	}
	if agent != testAgentA {
		t.Errorf("agent: got %q, want %q", agent, testAgentA)
	}
	if incidentType != "StalledRollout" {
		t.Errorf("incidentType: got %q, want %q", incidentType, "StalledRollout")
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
		BaseEvent:       watcher.BaseEvent{At: now, AgentName: testAgentA, Namespace: testNamespaceDev},
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
	if report.Status.IncidentType != "StalledRollout" {
		t.Errorf("incidentType: got %q, want BadDeploy", report.Status.IncidentType)
	}
	if report.Status.Severity != "P2" {
		t.Errorf("severity: got %q, want P2", report.Status.Severity)
	}
	if report.Status.Phase != phaseDetecting {
		t.Errorf("phase: got %q, want Detecting", report.Status.Phase)
	}
	if len(report.Status.AffectedResources) == 0 || report.Status.AffectedResources[0].Name != "payment-service" {
		t.Errorf("AffectedResources: expected payment-service, got %+v", report.Status.AffectedResources)
	}
	if !strings.HasPrefix(report.Name, "stalledrollout-payment-service-") {
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
		BaseEvent:       watcher.BaseEvent{At: now, AgentName: testAgentA, Namespace: testNamespaceDev},
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
				BaseEvent:    watcher.BaseEvent{Namespace: "default", AgentName: testAgentA, NodeName: "worker-1"},
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
			if agent != testAgentA {
				t.Errorf("agent: got %q", agent)
			}
			if incidentType != "NodePressure" {
				t.Errorf("incidentType: got %q, want NodePressure", incidentType)
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

// ── resolveIncidentsForDeletedPod ─────────────────────────────────────────────

func TestHandleEventResolvesActivePodIncidentOnPodDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 11, 0, 0, 0, time.UTC)
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "crashloop-my-pod-abc", Namespace: testNamespaceDev},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: testAgentA},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "CrashLoopBackOff",
			AffectedResources: []rcav1alpha1.AffectedResource{{
				Kind: "Pod", Name: "my-pod", Namespace: testNamespaceDev,
			}},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(report).
		Build()

	consumer := NewConsumer(cl, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	err := consumer.handleEvent(context.Background(), watcher.PodDeletedEvent{
		BaseEvent: watcher.BaseEvent{
			At:        now,
			AgentName: testAgentA,
			Namespace: testNamespaceDev,
			PodName:   "my-pod",
		},
	})
	if err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: report.Name, Namespace: report.Namespace}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != testPhaseResolved {
		t.Errorf("phase: got %q, want Resolved", updated.Status.Phase)
	}
	if updated.Status.ResolvedTime == nil {
		t.Error("expected ResolvedTime to be set")
	}
}

// ── incrementCounter ──────────────────────────────────────────────────────────

func TestIncrementCounter(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "1"},
		{"0", "1"},
		{"1", "2"},
		{"5", "6"},
		{"99", "100"},
		{"not-a-number", "1"},
		{"-1", "1"},
	}
	for _, tc := range cases {
		got := incrementCounter(tc.input)
		if got != tc.want {
			t.Errorf("incrementCounter(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── higherSeverity ────────────────────────────────────────────────────────────

func TestHigherSeverity(t *testing.T) {
	cases := []struct {
		current, incoming, want string
	}{
		{"P3", "P2", "P2"}, // incoming is higher
		{"P2", "P3", "P2"}, // current is higher
		{"P2", "P2", "P2"}, // equal
		{"P3", "P1", "P1"}, // P1 beats P3
		{"", "P3", "P3"},   // empty current → return incoming
		{"P1", "", "P1"},   // empty incoming → current wins
		{"", "", ""},       // both empty
	}
	for _, tc := range cases {
		got := higherSeverity(tc.current, tc.incoming)
		if got != tc.want {
			t.Errorf("higherSeverity(%q, %q) = %q, want %q", tc.current, tc.incoming, got, tc.want)
		}
	}
}

// ── safeLabelValue ────────────────────────────────────────────────────────────

func TestSafeLabelValue(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"", "unknown"},
		{"my-pod", "my-pod"},
		{"My Pod", "my-pod"}, // space → hyphen, uppercase → lower
		{"---", "unknown"},   // all hyphens stripped → fallback
		{"a/b:c", "a-b-c"},   // special chars → hyphens
		{strings.Repeat("x", 70), strings.Repeat("x", 63)}, // truncated to 63
	}
	for _, tc := range cases {
		got := safeLabelValue(tc.input)
		if got != tc.want {
			t.Errorf("safeLabelValue(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── safeNameToken ─────────────────────────────────────────────────────────────

func TestSafeNameToken(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"", "incident"},
		{"my-pod", "my-pod"},
		{"My Pod", "my-pod"},
		{"---", "incident"},  // all hyphens stripped → fallback
		{"pod.v2", "pod-v2"}, // dot replaced with hyphen
	}
	for _, tc := range cases {
		got := safeNameToken(tc.input)
		if got != tc.want {
			t.Errorf("safeNameToken(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── trimTimeline / trimSignals ────────────────────────────────────────────────

func TestTrimTimeline(t *testing.T) {
	tl := make([]rcav1alpha1.TimelineEvent, 55)
	result := trimTimeline(tl)
	if len(result) != maxTimelineEntries {
		t.Errorf("expected %d entries after trim, got %d", maxTimelineEntries, len(result))
	}
	// Short slice — must not be trimmed.
	short := make([]rcav1alpha1.TimelineEvent, 3)
	if len(trimTimeline(short)) != 3 {
		t.Error("short timeline must not be trimmed")
	}
}

func TestTrimSignals(t *testing.T) {
	signals := make([]string, 25)
	result := trimSignals(signals)
	if len(result) != maxSignalEntries {
		t.Errorf("expected %d signals after trim, got %d", maxSignalEntries, len(result))
	}
	short := make([]string, 5)
	if len(trimSignals(short)) != 5 {
		t.Error("short signals must not be trimmed")
	}
}

// ── incidentAffectsPod ────────────────────────────────────────────────────────

func TestIncidentAffectsPod(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "my-pod", Namespace: "dev"},
				{Kind: "Deployment", Name: "my-deploy", Namespace: "dev"},
			},
		},
	}
	if !incidentAffectsPod(report, "my-pod", "dev") {
		t.Error("expected true for matching pod")
	}
	if incidentAffectsPod(report, "other-pod", "dev") {
		t.Error("expected false for different pod name")
	}
	if incidentAffectsPod(report, "my-pod", "staging") {
		t.Error("expected false for different namespace")
	}
	if incidentAffectsPod(report, "my-deploy", "dev") {
		t.Error("expected false for non-Pod kind")
	}
}

// ── updateActiveIncident ──────────────────────────────────────────────────────

func TestUpdateActiveIncident_UpdatesSeverityAndTimeline(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 11, 30, 0, 0, time.UTC)
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "crashloop-svc-abc", Namespace: testNamespaceDev,
			Labels:      map[string]string{labelSeverity: "P3"},
			Annotations: map[string]string{annotationSignalSeen: "1"},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: testAgentA},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			Severity:     "P3",
			IncidentType: "CrashLoopBackOff",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "svc", Namespace: testNamespaceDev},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(report).
		Build()

	consumer := NewConsumer(cl, nil, logr.Discard())
	consumer.now = func() time.Time { return now }

	// Second CrashLoopBackOff signal — should upgrade severity if higher and append timeline.
	err := consumer.handleEvent(context.Background(), watcher.CrashLoopBackOffEvent{
		BaseEvent:    watcher.BaseEvent{At: now, AgentName: testAgentA, Namespace: testNamespaceDev, PodName: "svc"},
		RestartCount: 5,
		Threshold:    3,
	})
	if err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: report.Name, Namespace: testNamespaceDev}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Annotations[annotationSignalSeen] != "2" {
		t.Errorf("signal-count: got %q, want 2", updated.Annotations[annotationSignalSeen])
	}
	if len(updated.Status.Timeline) == 0 {
		t.Error("expected timeline to be updated")
	}
}

// ── mapEvent ──────────────────────────────────────────────────────────────────

func TestMapEvent_AllBranches(t *testing.T) {
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		event        watcher.CorrelatorEvent
		wantNS       string
		wantPod      string
		wantType     string
		wantSeverity string
		wantEmpty    bool // true when both namespace and pod must be ""
	}{
		{
			name:         "CrashLoopBackOff basic",
			event:        watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-a", AgentName: "ag"}, RestartCount: 3, Threshold: 3},
			wantNS:       "dev",
			wantPod:      "pod-a",
			wantType:     "CrashLoopBackOff",
			wantSeverity: "P3",
		},
		{
			name:         "CrashLoopBackOff with exitCode",
			event:        watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-a", AgentName: "ag"}, RestartCount: 3, Threshold: 3, LastExitCode: 137, ExitCodeCategory: "OOM", ExitCodeDescription: "container oom"},
			wantNS:       "dev",
			wantPod:      "pod-a",
			wantType:     "CrashLoopBackOff",
			wantSeverity: "P3",
		},
		{
			name:         "OOMKilled",
			event:        watcher.OOMKilledEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-b", AgentName: "ag"}},
			wantNS:       "dev",
			wantPod:      "pod-b",
			wantType:     "OOMKilled",
			wantSeverity: "P2",
		},
		{
			name:         "ImagePullBackOff",
			event:        watcher.ImagePullBackOffEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-c", AgentName: "ag"}, Reason: "ErrImagePull"},
			wantNS:       "dev",
			wantPod:      "pod-c",
			wantType:     "ImagePullBackOff",
			wantSeverity: "P3",
		},
		{
			name:         "PodPendingTooLong",
			event:        watcher.PodPendingTooLongEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-d", AgentName: "ag"}, PendingFor: 5 * time.Minute, Timeout: 3 * time.Minute},
			wantNS:       "dev",
			wantPod:      "pod-d",
			wantType:     "PodPendingTooLong",
			wantSeverity: "P3",
		},
		{
			name:         "GracePeriodViolation",
			event:        watcher.GracePeriodViolationEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-e", AgentName: "ag"}, GracePeriodSeconds: 30, OverdueFor: 5 * time.Second},
			wantNS:       "dev",
			wantPod:      "pod-e",
			wantType:     "GracePeriodViolation",
			wantSeverity: "P2",
		},
		{
			name:         "NodeNotReady",
			event:        watcher.NodeNotReadyEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", NodeName: "node-1", AgentName: "ag"}, Reason: "KubeletNotReady"},
			wantNS:       "dev",
			wantPod:      "node-1",
			wantType:     "NodeNotReady",
			wantSeverity: "P1",
		},
		{
			name:         "PodEvicted",
			event:        watcher.PodEvictedEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-f", AgentName: "ag"}, Reason: "Evicted"},
			wantNS:       "dev",
			wantPod:      "pod-f",
			wantType:     "PodEvicted",
			wantSeverity: "P2",
		},
		{
			name:         "ProbeFailure",
			event:        watcher.ProbeFailureEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-g", AgentName: "ag"}, ProbeType: "Liveness"},
			wantNS:       "dev",
			wantPod:      "pod-g",
			wantType:     "ProbeFailure",
			wantSeverity: "P3",
		},
		{
			name:         "StalledRollout",
			event:        watcher.StalledRolloutEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", AgentName: "ag"}, DeploymentName: "my-deploy", Reason: "ProgressDeadlineExceeded"},
			wantNS:       "dev",
			wantPod:      "my-deploy",
			wantType:     "StalledRollout",
			wantSeverity: "P2",
		},
		{
			name:         "NodePressure DiskPressure → P2",
			event:        watcher.NodePressureEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", NodeName: "node-2", AgentName: "ag"}, PressureType: "DiskPressure"},
			wantNS:       "dev",
			wantPod:      "node-2",
			wantType:     "NodePressure",
			wantSeverity: "P2",
		},
		{
			name:         "NodePressure PIDPressure → P3",
			event:        watcher.NodePressureEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", NodeName: "node-3", AgentName: "ag"}, PressureType: "PIDPressure"},
			wantNS:       "dev",
			wantPod:      "node-3",
			wantType:     "NodePressure",
			wantSeverity: "P3",
		},
		{
			name:      "PodHealthy returns empty",
			event:     watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "pod-i"}},
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, pod, _, incType, sev, _ := mapEvent(tc.event)
			if tc.wantEmpty {
				if ns != "" || pod != "" {
					t.Errorf("expected empty namespace/pod, got ns=%q pod=%q", ns, pod)
				}
				return
			}
			if ns != tc.wantNS {
				t.Errorf("namespace: got %q, want %q", ns, tc.wantNS)
			}
			if pod != tc.wantPod {
				t.Errorf("pod/resource: got %q, want %q", pod, tc.wantPod)
			}
			if incType != tc.wantType {
				t.Errorf("incidentType: got %q, want %q", incType, tc.wantType)
			}
			if sev != tc.wantSeverity {
				t.Errorf("severity: got %q, want %q", sev, tc.wantSeverity)
			}
		})
	}
}

// ── isPodCurrentlyReady ───────────────────────────────────────────────────────

func TestIsPodCurrentlyReady(t *testing.T) {
	makePod := func(phase corev1.PodPhase, readyStatus corev1.ConditionStatus) *corev1.Pod {
		return &corev1.Pod{
			Status: corev1.PodStatus{
				Phase: phase,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: readyStatus},
				},
			},
		}
	}

	t.Run("nil pod → false", func(t *testing.T) {
		if isPodCurrentlyReady(nil) {
			t.Error("expected false for nil pod")
		}
	})
	t.Run("Running + Ready → true", func(t *testing.T) {
		if !isPodCurrentlyReady(makePod(corev1.PodRunning, corev1.ConditionTrue)) {
			t.Error("expected true for Running+Ready pod")
		}
	})
	t.Run("Running + NotReady → false", func(t *testing.T) {
		if isPodCurrentlyReady(makePod(corev1.PodRunning, corev1.ConditionFalse)) {
			t.Error("expected false for Running+NotReady pod")
		}
	})
	t.Run("Pending + Ready condition → false (wrong phase)", func(t *testing.T) {
		if isPodCurrentlyReady(makePod(corev1.PodPending, corev1.ConditionTrue)) {
			t.Error("expected false for non-Running pod even when Ready condition is True")
		}
	})
	t.Run("Running + no conditions → false", func(t *testing.T) {
		pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if isPodCurrentlyReady(pod) {
			t.Error("expected false when Ready condition is absent")
		}
	})
}

// ── resolveIncidentsForPod ────────────────────────────────────────────────────

func TestResolveIncidentsForPod_ResolvesWhenPodReady(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	now := time.Date(2026, 3, 14, 14, 0, 0, 0, time.UTC)

	// Running+Ready pod.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "dev"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	active := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "crashloop-my-pod-abc", Namespace: "dev"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "ag"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "CrashLoopBackOff",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "my-pod", Namespace: "dev"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(pod, active).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.handleEvent(context.Background(), watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{At: now, Namespace: "dev", PodName: "my-pod"},
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: active.Name, Namespace: "dev"}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != "Resolved" {
		t.Errorf("expected Resolved, got %q", updated.Status.Phase)
	}
}

func TestResolveIncidentsForPod_PodNotFound_ReturnsNil(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	// No pod in the store — simulates deleted pod.
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := NewConsumer(cl, nil, logr.Discard())

	// Should NOT return an error when pod is not found.
	if err := c.handleEvent(context.Background(), watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{Namespace: "dev", PodName: "gone-pod"},
	}); err != nil {
		t.Errorf("expected nil for deleted pod, got: %v", err)
	}
}

func TestResolveIncidentsForPod_PodNotReady_SkipsResolve(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add rca: %v", err)
	}

	// Pod in Pending state — not ready.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-pod", Namespace: "dev"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	active := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "crashloop-pending-pod-abc", Namespace: "dev"},
		Spec:       rcav1alpha1.IncidentReportSpec{AgentRef: "ag"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        "Active",
			IncidentType: "CrashLoopBackOff",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "pending-pod", Namespace: "dev"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(pod, active).
		Build()
	c := NewConsumer(cl, nil, logr.Discard())

	if err := c.handleEvent(context.Background(), watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{Namespace: "dev", PodName: "pending-pod"},
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	// Incident should still be Active since pod isn't ready.
	got := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: active.Name, Namespace: "dev"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
}

// ── OOM signal cooldown ───────────────────────────────────────────────────────

// TestResolveIncidentsForPod_SkipsRecentSignalCooldown verifies that an
// incident with a recent annotationLastSeen (within signalCooldown) is NOT
// resolved when the pod becomes briefly healthy. This prevents the
// OOMKilled/CrashLoop create→resolve→create cycle.
func TestResolveIncidentsForPod_SkipsRecentSignalCooldown(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	// Signal just 30 s ago — well inside the 5-minute signalCooldown.
	lastSeen := now.Add(-30 * time.Second)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "oomkill-demo", Namespace: "dev"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "oom-oomkill-demo-abc",
			Namespace: "dev",
			Annotations: map[string]string{
				annotationLastSeen: lastSeen.Format(time.RFC3339),
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "ag"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseDetecting,
			IncidentType: "OOMKilled",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "oomkill-demo", Namespace: "dev"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(pod, report).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// Pod becomes briefly Running+Ready after OOMKill restart.
	if err := c.handleEvent(context.Background(), watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{Namespace: "dev", PodName: "oomkill-demo"},
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	got := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: report.Name, Namespace: "dev"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase == phaseResolved {
		t.Errorf("incident resolved too early — signal cooldown was not respected (phase=%q)", got.Status.Phase)
	}
}

// ── Registry namespace-level dedup ───────────────────────────────────────────

// TestHandleEventRegistryDedupsToOneIncidentPerNamespace verifies that
// multiple pods from the same deployment failing with ImagePullBackOff
// consolidate into a single Registry IncidentReport, with all affected pod
// names tracked in AffectedResources.
func TestHandleEventRegistryDedupsToOneIncidentPerNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// First pod fails to pull.
	if err := c.handleEvent(context.Background(), watcher.ImagePullBackOffEvent{
		BaseEvent:     watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "payment-service-4pkpz"},
		ContainerName: "payment-service",
		Reason:        "ImagePullBackOff",
	}); err != nil {
		t.Fatalf("first handleEvent: %v", err)
	}

	// Second pod from same deployment fails.
	if err := c.handleEvent(context.Background(), watcher.ImagePullBackOffEvent{
		BaseEvent:     watcher.BaseEvent{At: now.Add(5 * time.Second), AgentName: "ag", Namespace: "dev", PodName: "payment-service-6jwsg"},
		ContainerName: "payment-service",
		Reason:        "ImagePullBackOff",
	}); err != nil {
		t.Fatalf("second handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport (namespace-level dedup), got %d", len(list.Items))
	}

	report := list.Items[0]
	if report.Status.IncidentType != "ImagePullBackOff" {
		t.Errorf("incidentType: got %q, want %q", report.Status.IncidentType, "ImagePullBackOff")
	}

	pods := make(map[string]bool)
	for _, res := range report.Status.AffectedResources {
		pods[res.Name] = true
	}
	if !pods["payment-service-4pkpz"] {
		t.Error("expected payment-service-4pkpz in AffectedResources")
	}
}

func TestHandleEvent_StalledRolloutCoalescesIntoExistingRegistryIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	existing := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-payment-service-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "ImagePullBackOff",
				labelSeverity:     "P2",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			Fingerprint:  "Workload|dev|deployment|payment-service",
			IncidentType: "ImagePullBackOff",
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: "dev",
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       "payment-service",
				},
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       "payment-service",
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "ImagePullBackOff",
			Severity:     "P2",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "dev", Name: "payment-service"},
				{Kind: "Pod", Namespace: "dev", Name: "payment-service-69f84d75db-2lb58"},
			},
			Timeline: []rcav1alpha1.TimelineEvent{{
				Time:  metav1.NewTime(now.Add(-2 * time.Minute)),
				Event: "Registry: ImagePullBackOff with no prior success pod=payment-service-69f84d75db-2lb58 container=app reason=ErrImagePull",
			}},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(existing).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.handleEvent(context.Background(), watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev"},
		DeploymentName:  "payment-service",
		DesiredReplicas: 3,
		ReadyReplicas:   0,
		Reason:          "ProgressDeadlineExceeded",
		Message:         "Deployment does not have minimum availability",
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 incident after coalescing, got %d", len(list.Items))
	}
	if list.Items[0].Name != existing.Name {
		t.Fatalf("expected rollout signal to update %q, got %q", existing.Name, list.Items[0].Name)
	}
	// After the StalledRollout signal, the incident type reflects the latest signal.
	if list.Items[0].Status.IncidentType != "StalledRollout" {
		t.Fatalf("incidentType: got %q, want %q", list.Items[0].Status.IncidentType, "StalledRollout")
	}
	if got := len(list.Items[0].Status.Timeline); got < 2 {
		t.Fatalf("expected rollout event appended to timeline, got %d entries", got)
	}
}

// ── Incident reopen ───────────────────────────────────────────────────────────

// TestHandleEvent_ReopensRecentlyResolvedIncident verifies that a new watcher
// signal for a pod whose incident was resolved within reopenWindow reopens the
// same IncidentReport (Detecting phase) instead of creating a duplicate.
func TestHandleEvent_ReopensRecentlyResolvedIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	resolvedAt := metav1.NewTime(now.Add(-10 * time.Minute)) // within 30-min reopenWindow

	existing := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "oom-oomkill-demo-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelSeverity:     "P2",
				labelIncidentType: "OOMKilled",
				labelPodName:      "oomkill-demo",
			},
			Annotations: map[string]string{
				annotationLastSeen:   resolvedAt.Format(time.RFC3339),
				annotationSignalSeen: "3",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "ag"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "OOMKilled",
			Severity:     "P2",
			ResolvedTime: &resolvedAt,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "oomkill-demo", Namespace: "dev"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(existing).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// New OOMKill for the same pod.
	if err := c.handleEvent(context.Background(), watcher.OOMKilledEvent{
		BaseEvent: watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "oomkill-demo"},
		Reason:    "OOMKilled",
		ExitCode:  137,
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport (reopen, not create new), got %d", len(list.Items))
	}

	got := list.Items[0]
	if got.Name != existing.Name {
		t.Errorf("name: got %q, want %q (should reuse existing report)", got.Name, existing.Name)
	}
	if got.Status.Phase != phaseDetecting {
		t.Errorf("Phase=%q; want Detecting (re-opened)", got.Status.Phase)
	}
	if got.Status.ResolvedTime != nil {
		t.Error("ResolvedTime should be nil after reopen")
	}
	// Signal counter should be incremented (carried over from previous cycle).
	if got.Annotations[annotationSignalSeen] != "4" {
		t.Errorf("signal-count: got %q, want 4", got.Annotations[annotationSignalSeen])
	}
}

// TestHandleEvent_CreatesNewIfResolvedTooOld verifies that when a resolved
// incident is older than reopenWindow a new IncidentReport is created instead
// of reopening the stale one.
func TestHandleEvent_CreatesNewIfResolvedTooOld(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	resolvedAt := metav1.NewTime(now.Add(-2 * time.Hour)) // older than 30-min reopenWindow

	old := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "oom-oomkill-demo-stale",
			Namespace: "dev",
			Annotations: map[string]string{
				annotationLastSeen:   resolvedAt.Format(time.RFC3339),
				annotationSignalSeen: "1",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{AgentRef: "ag"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "OOMKilled",
			ResolvedTime: &resolvedAt,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "oomkill-demo", Namespace: "dev"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(old).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.handleEvent(context.Background(), watcher.OOMKilledEvent{
		BaseEvent: watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "oomkill-demo"},
		Reason:    "OOMKilled",
		ExitCode:  137,
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 IncidentReports (old stale + new), got %d", len(list.Items))
	}
}

// newRegistryIncident is a test helper that builds an open ImagePullBackOff IncidentReport
// at workload scope for a shared deployment. All incidents for the same namespace
// share the same fingerprint so they consolidate into one.
//
//nolint:unparam
func newRegistryIncident(name, namespace, podName string, createdAt metav1.Time) *rcav1alpha1.IncidentReport {
	fp := "Workload|" + namespace + "|deployment|my-deploy"
	return &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: createdAt,
			Labels: map[string]string{
				labelIncidentType: "ImagePullBackOff",
				labelSeverity:     "P2",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:    "ag",
			Fingerprint: fp,
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: namespace,
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  namespace,
					Name:       "my-deploy",
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "ImagePullBackOff",
			Severity:     "P2",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: podName, Namespace: namespace},
			},
		},
	}
}

// TestConsolidateRegistryDuplicates_MergesAndResolvesExtras verifies that when
// three open Registry incidents exist in the same namespace, startup
// consolidation keeps the oldest as canonical, merges all pods into it, and
// resolves the two extras.
func TestConsolidateRegistryDuplicates_MergesAndResolvesExtras(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	// Oldest incident — should be kept as canonical.
	r1 := newRegistryIncident("registry-pod-a-aaaa", "dev", "pod-a",
		metav1.NewTime(now.Add(-3*time.Minute)))
	// Two newer duplicates created by the bootstrap-scan race.
	r2 := newRegistryIncident("registry-pod-b-bbbb", "dev", "pod-b",
		metav1.NewTime(now.Add(-2*time.Minute)))
	r3 := newRegistryIncident("registry-pod-c-cccc", "dev", "pod-c",
		metav1.NewTime(now.Add(-1*time.Minute)))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(r1, r2, r3).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.consolidateRegistryDuplicates(context.Background()); err != nil {
		t.Fatalf("consolidateRegistryDuplicates: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}

	var resolved, open []string
	for _, r := range list.Items {
		if r.Status.Phase == phaseResolved {
			resolved = append(resolved, r.Name)
		} else {
			open = append(open, r.Name)
		}
	}

	if len(open) != 1 {
		t.Fatalf("expected 1 open incident, got %d: %v", len(open), open)
	}
	if open[0] != r1.Name {
		t.Errorf("canonical incident should be oldest %q, got %q", r1.Name, open[0])
	}
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved duplicates, got %d: %v", len(resolved), resolved)
	}

	// Canonical should now contain all 3 pods.
	canonical := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "dev", Name: r1.Name}, canonical); err != nil {
		t.Fatalf("get canonical: %v", err)
	}
	if len(canonical.Status.AffectedResources) != 3 {
		t.Errorf("canonical AffectedResources: got %d, want 3", len(canonical.Status.AffectedResources))
	}
}

// TestConsolidateRegistryDuplicates_NoopWhenSingleIncident verifies that
// consolidation is a no-op when only one open Registry incident exists.
func TestConsolidateRegistryDuplicates_NoopWhenSingleIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	r1 := newRegistryIncident("registry-pod-a-aaaa", "dev", "pod-a",
		metav1.NewTime(now.Add(-5*time.Minute)))

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(r1).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.consolidateRegistryDuplicates(context.Background()); err != nil {
		t.Fatalf("consolidateRegistryDuplicates: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Status.Phase != phaseActive {
		t.Errorf("incident should be left unchanged; got %d items, phase=%q", len(list.Items), list.Items[0].Status.Phase)
	}
}

// TestHandleEvent_RegistryCachePreventBootstrapDuplicates simulates the
// bootstrap-scan race: three ImagePullBackOff events arrive in rapid succession.
// Even if the API informer cache hasn't caught up, the in-memory dedup cache
// must route the second and third events to the first-created incident.
func TestHandleEvent_RegistryCachePreventBootstrapDuplicates(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// Pod names follow Kubernetes naming convention so GuessDeploymentNameFromPod
	// can derive the shared deployment name "my-deploy" for all three pods.
	makeEvent := func(pod string) watcher.ImagePullBackOffEvent {
		return watcher.ImagePullBackOffEvent{
			BaseEvent:     watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: pod},
			Reason:        "ImagePullBackOff",
			ContainerName: "app",
		}
	}

	for _, pod := range []string{"my-deploy-abc123def-ab12c", "my-deploy-abc123def-cd34e", "my-deploy-abc123def-ef56g"} {
		if err := c.handleEvent(context.Background(), makeEvent(pod)); err != nil {
			t.Fatalf("handleEvent(%s): %v", pod, err)
		}
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly 1 Registry incident, got %d", len(list.Items))
	}
	// Expect 4 AffectedResources: 1 Deployment (workload scope) + 3 pods.
	if len(list.Items[0].Status.AffectedResources) != 4 {
		t.Errorf("expected 4 AffectedResources (1 deployment + 3 pods), got %d", len(list.Items[0].Status.AffectedResources))
	}
}

// TestHandleEvent_Rule2BadDeploy_DedupsWithExistingIncident verifies that when
// Rule 2 fires (CrashLoop + StalledRollout → BadDeploy), it routes the signal
// to the existing BadDeploy incident created by the earlier StalledRollout event
// rather than creating a duplicate. The CorrelationResult.Resource field
// overrides podName to the deployment name, aligning the dedup key.
func TestHandleEvent_Rule2BadDeploy_DedupsWithExistingIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	const deployName = "payment-service"

	// Existing StalledRollout incident created by the earlier StalledRollout signal.
	existingBadDeploy := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stalledrollout-payment-service-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "StalledRollout",
				labelSeverity:     "P2",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			Fingerprint:  "Workload|dev|deployment|payment-service",
			IncidentType: "StalledRollout",
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: "dev",
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       deployName,
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "StalledRollout",
			Severity:     "P2",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "dev", Name: deployName},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(existingBadDeploy).
		Build()

	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return now }
	// Pre-buffer a StalledRollout event so Rule 2 fires on the CrashLoop.
	corr.Add(watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{At: now.Add(-30 * time.Second), AgentName: "ag", Namespace: "dev"},
		DeploymentName:  deployName,
		DesiredReplicas: 3,
		ReadyReplicas:   0,
		Reason:          "ProgressDeadlineExceeded",
	})

	c := NewConsumer(cl, nil, logr.Discard(), WithRuleEngine(corr))
	c.now = func() time.Time { return now }

	// Fire a CrashLoop event; Rule 2 overrides podName → deployment name.
	if err := c.handleEvent(context.Background(), watcher.CrashLoopBackOffEvent{
		BaseEvent:    watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "payment-service-abc123-xyz"},
		RestartCount: 5,
		Threshold:    3,
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 BadDeploy incident (no duplicate), got %d", len(list.Items))
	}
	if list.Items[0].Name != existingBadDeploy.Name {
		t.Errorf("existing incident name should be unchanged; got %q", list.Items[0].Name)
	}
}

// TestHandleEvent_Rule5NodeFailure_DedupsWithNodeNotReadyIncident verifies that
// when Rule 5 fires (PodEvicted + NodeNotReady → NodeFailure P1), it routes the
// signal into the existing NodeFailure incident created by the NodeNotReady event
// rather than creating a second one for the evicted pod's name.
func TestHandleEvent_Rule5NodeFailure_DedupsWithNodeNotReadyIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	const nodeName = "worker-node-1"

	// Existing NodeNotReady incident created by the earlier NodeNotReady signal.
	existingNodeFailure := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nodenotready-worker-node-1-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "NodeNotReady",
				labelSeverity:     "P1",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			Fingerprint:  "Cluster|node|worker-node-1",
			IncidentType: "NodeNotReady",
			Scope: rcav1alpha1.IncidentScope{
				Level: incident.ScopeLevelCluster,
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       nodeName,
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "NodeNotReady",
			Severity:     "P1",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "v1", Kind: "Node", Name: nodeName},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(existingNodeFailure).
		Build()

	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return now }
	// Pre-buffer a NodeNotReady event so Rule 5 fires on the PodEvicted.
	corr.Add(watcher.NodeNotReadyEvent{
		BaseEvent: watcher.BaseEvent{At: now.Add(-20 * time.Second), AgentName: "ag", Namespace: "dev", NodeName: nodeName},
		Reason:    "KubeletNotReady",
		Message:   "runtime network not ready",
	})

	c := NewConsumer(cl, nil, logr.Discard(), WithRuleEngine(corr))
	c.now = func() time.Time { return now }

	// Fire a PodEvicted event; Rule 5 overrides podName → node name.
	if err := c.handleEvent(context.Background(), watcher.PodEvictedEvent{
		BaseEvent: watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "workload-pod-xyz", NodeName: nodeName},
		Reason:    "NodeEviction",
		Message:   "evicted by kubelet",
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

}

// ── Workload-ref fallback deduplication ──────────────────────────────────────

// TestHandleEvent_WorkloadRefFallback_PreventsOpenDuplicate verifies that when
// an open Registry incident exists with a different fingerprint (e.g.,
// ReplicaSet-scoped) but the same WorkloadRef (Deployment), a new
// ImagePullBackOff event is routed to the existing incident via the workload-ref
// fallback rather than creating a second one.
func TestHandleEvent_WorkloadRefFallback_PreventsOpenDuplicate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	// Existing open incident created with a ReplicaSet-scoped fingerprint
	// (what happens when the RS Get succeeds but Deployment lookup fails).
	existing := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-myapp-rs-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "ImagePullBackOff",
				labelSeverity:     "P3",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			Fingerprint:  "Workload|dev|replicaset|myapp-abc12345",
			IncidentType: "ImagePullBackOff",
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: "dev",
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       "myapp",
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "ImagePullBackOff",
			Severity:     "P3",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "dev", Name: "myapp"},
				{Kind: "Pod", Namespace: "dev", Name: "myapp-abc12345-xyz56"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(existing).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// New event for a different pod of the SAME deployment. The enricher will
	// fail (pod not in fake client) and fall back to the heuristic which
	// correctly guesses "myapp" from the pod name, producing fingerprint
	// "Workload|dev|deployment|myapp" — different from the existing incident's
	// "Workload|dev|replicaset|myapp-abc12345".
	if err := c.handleEvent(context.Background(), watcher.ImagePullBackOffEvent{
		BaseEvent:     watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "myapp-abc12345-def78"},
		ContainerName: "app",
		Reason:        "ImagePullBackOff",
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport (workload-ref fallback, no duplicate), got %d", len(list.Items))
	}
	if list.Items[0].Name != existing.Name {
		t.Errorf("existing incident name should be unchanged; got %q", list.Items[0].Name)
	}
}

// TestHandleEvent_WorkloadRefFallback_ReOpensResolvedWithDifferentFingerprint
// verifies that a resolved Registry incident with a different fingerprint is
// reopened (not duplicated) when a new ImagePullBackOff event arrives for the
// same workload within the reopen window. This covers the "one resolved, one
// active" duplicate scenario described in the issue.
func TestHandleEvent_WorkloadRefFallback_ReOpensResolvedWithDifferentFingerprint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	resolvedAt := metav1.NewTime(now.Add(-5 * time.Minute)) // within 30-min reopen window

	// Resolved incident created with a ReplicaSet-scoped fingerprint; the
	// WorkloadRef correctly points to the parent Deployment.
	resolved := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-myapp-rs-abc",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "ImagePullBackOff",
				labelSeverity:     "P3",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			Fingerprint:  "Workload|dev|replicaset|myapp-abc12345",
			IncidentType: "ImagePullBackOff",
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: "dev",
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       "myapp",
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseResolved,
			IncidentType: "ImagePullBackOff",
			Severity:     "P3",
			ResolvedTime: &resolvedAt,
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "dev", Name: "myapp"},
				{Kind: "Pod", Namespace: "dev", Name: "myapp-abc12345-xyz56"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(resolved).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	// New event for the same deployment. The enricher falls back to heuristic
	// and produces fingerprint "Registry|dev|deployment|myapp", which differs
	// from the resolved incident's "Registry|dev|replicaset|myapp-abc12345".
	// The workload-ref fallback must reopen the resolved incident.
	if err := c.handleEvent(context.Background(), watcher.ImagePullBackOffEvent{
		BaseEvent:     watcher.BaseEvent{At: now, AgentName: "ag", Namespace: "dev", PodName: "myapp-abc12345-def78"},
		ContainerName: "app",
		Reason:        "ImagePullBackOff",
	}); err != nil {
		t.Fatalf("handleEvent: %v", err)
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := cl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport (reopen via workload-ref, no duplicate), got %d: resolved=%v active=%v",
			len(list.Items),
			func() []string {
				var names []string
				for _, r := range list.Items {
					if r.Status.Phase == phaseResolved {
						names = append(names, r.Name+"(resolved)")
					}
				}
				return names
			}(),
			func() []string {
				var names []string
				for _, r := range list.Items {
					if r.Status.Phase != phaseResolved {
						names = append(names, r.Name+"("+r.Status.Phase+")")
					}
				}
				return names
			}(),
		)
	}
	got := list.Items[0]
	if got.Name != resolved.Name {
		t.Errorf("expected incident %q to be reopened; got %q", resolved.Name, got.Name)
	}
	if got.Status.Phase == phaseResolved {
		t.Errorf("incident should have been reopened, but phase is still %q", got.Status.Phase)
	}
	if got.Status.ResolvedTime != nil {
		t.Error("ResolvedTime should be nil after reopen")
	}
}

// TestConsolidateBackfillsFingerprint verifies that Consolidate writes
// Spec.Fingerprint on legacy incidents that were created without it.
// After backfill, a new signal can find the incident via the label-based hash
// lookup without needing the workload-ref fallback.
func TestConsolidateBackfillsFingerprint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add RCA scheme: %v", err)
	}

	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

	// Legacy incident without Spec.Fingerprint; WorkloadRef points to a Deployment.
	legacy := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-myapp-legacy",
			Namespace: "dev",
			Labels: map[string]string{
				labelIncidentType: "ImagePullBackOff",
				labelSeverity:     "P3",
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "ag",
			IncidentType: "ImagePullBackOff",
			Scope: rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: "dev",
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  "dev",
					Name:       "myapp",
				},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:        phaseActive,
			IncidentType: "ImagePullBackOff",
			Severity:     "P3",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "dev", Name: "myapp"},
				{Kind: "Pod", Namespace: "dev", Name: "myapp-abc12345-xyz56"},
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		WithObjects(legacy).
		Build()

	c := NewConsumer(cl, nil, logr.Discard())
	c.now = func() time.Time { return now }

	if err := c.consolidateRegistryDuplicates(context.Background()); err != nil {
		t.Fatalf("consolidateRegistryDuplicates: %v", err)
	}

	updated := &rcav1alpha1.IncidentReport{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "dev", Name: legacy.Name}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Spec.Fingerprint == "" {
		t.Error("Spec.Fingerprint should have been backfilled by Consolidate")
	}
}
