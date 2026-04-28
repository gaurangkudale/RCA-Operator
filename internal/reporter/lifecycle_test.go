package reporter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
)

// fakeReporter wires a fake controller-runtime client and a deterministic
// clock. Tests advance the clock manually for cooldown / reopen-window cases.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newReporterFixture(t *testing.T, objs ...runtime.Object) (*Reporter, *testClock, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{})
	for _, o := range objs {
		cb = cb.WithRuntimeObjects(o)
	}
	c := cb.Build()
	clk := &testClock{now: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)}
	r := NewReporter(c, logr.Discard())
	r.Now = clk.Now
	return r, clk, c
}

// crashLoopInput builds a minimal pod-scoped Input for tests. ns is a
// parameter for clarity even though every current caller passes "prod" — keep
// the signature flexible for future callers.
//
//nolint:unparam // ns is fixed today but the helper is meant to take any namespace
func crashLoopInput(ns, pod, agentRef string, observedAt time.Time) incident.Input {
	return incident.Input{
		Namespace:    ns,
		AgentRef:     agentRef,
		IncidentType: "CrashLoopBackOff",
		Severity:     "P3",
		Reason:       "BackOff",
		Message:      "container crashlooping",
		ObservedAt:   observedAt,
		Scope: rcav1alpha1.IncidentScope{
			Level:     incident.ScopeLevelPod,
			Namespace: ns,
			ResourceRef: &rcav1alpha1.IncidentObjectRef{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       "Pod",
				Namespace:  ns,
				Name:       pod,
			},
		},
		AffectedResources: []rcav1alpha1.AffectedResource{
			{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Pod", Namespace: ns, Name: pod},
		},
	}
}

func listIncidents(t *testing.T, c client.Client) []rcav1alpha1.IncidentReport {
	t.Helper()
	var l rcav1alpha1.IncidentReportList
	if err := c.List(context.Background(), &l); err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	return l.Items
}

// --- EnsureSignal: create path -------------------------------------------------

func TestEnsureSignal_CreatesIncidentInDetectingPhase(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())

	if err := r.EnsureSignal(context.Background(), in); err != nil {
		t.Fatalf("EnsureSignal: %v", err)
	}
	items := listIncidents(t, c)
	if len(items) != 1 {
		t.Fatalf("want 1 IncidentReport, got %d", len(items))
	}
	got := items[0]
	if got.Status.Phase != PhaseDetecting {
		t.Errorf("phase = %q, want %q", got.Status.Phase, PhaseDetecting)
	}
	if got.Status.SignalCount != 1 {
		t.Errorf("signalCount = %d, want 1", got.Status.SignalCount)
	}
	if got.Status.IncidentType != "CrashLoopBackOff" {
		t.Errorf("incidentType = %q", got.Status.IncidentType)
	}
	if got.Status.Severity != "P3" {
		t.Errorf("severity = %q", got.Status.Severity)
	}
	if got.Status.FirstObservedAt == nil || got.Status.LastObservedAt == nil {
		t.Errorf("expected FirstObservedAt and LastObservedAt non-nil")
	}
	if !strings.Contains(got.Name, "crashloopbackoff") {
		t.Errorf("generated name should contain incident type, got %q", got.Name)
	}
	// Labels
	if got.Labels[LabelSeverity] != "P3" {
		t.Errorf("LabelSeverity = %q", got.Labels[LabelSeverity])
	}
	if got.Labels[LabelIncidentType] != "CrashLoopBackOff" {
		t.Errorf("LabelIncidentType = %q", got.Labels[LabelIncidentType])
	}
	if got.Labels[LabelPodName] != "api-1" {
		t.Errorf("LabelPodName = %q", got.Labels[LabelPodName])
	}
	// Annotations
	if got.Annotations[AnnotationSignalSeen] != "1" {
		t.Errorf("AnnotationSignalSeen = %q", got.Annotations[AnnotationSignalSeen])
	}
	if got.Annotations[AnnotationLastSeen] == "" {
		t.Errorf("AnnotationLastSeen should be set")
	}
}

// --- EnsureSignal: dedup into existing active incident -----------------------

func TestEnsureSignal_DedupsIntoActiveIncident(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())

	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("first EnsureSignal: %v", err)
	}
	clk.Advance(20 * time.Second)
	in.ObservedAt = clk.Now()
	in.Message = "container crashlooping again"
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("second EnsureSignal: %v", err)
	}

	items := listIncidents(t, c)
	if len(items) != 1 {
		t.Fatalf("want exactly 1 incident after dedup, got %d", len(items))
	}
	got := items[0]
	if got.Status.SignalCount != 2 {
		t.Errorf("signalCount = %d, want 2", got.Status.SignalCount)
	}
	if got.Status.LastObservedAt == nil || got.Status.LastObservedAt.Time.Before(got.Status.FirstObservedAt.Time) {
		t.Errorf("LastObservedAt should advance: first=%v last=%v", got.Status.FirstObservedAt, got.Status.LastObservedAt)
	}
	// Still in Detecting phase — Active transition is the controller's job.
	if got.Status.Phase != PhaseDetecting {
		t.Errorf("phase should remain Detecting, got %q", got.Status.Phase)
	}
}

// --- EnsureSignal: reopen a recently-resolved incident ----------------------

func TestEnsureSignal_ReopensRecentlyResolvedIncident(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Manually mark the incident Resolved by patching status — simulates the
	// healthy-pod resolve path having run.
	items := listIncidents(t, c)
	report := items[0].DeepCopy()
	base := report.DeepCopy()
	now := metav1.NewTime(clk.Now())
	report.Status.Phase = PhaseResolved
	report.Status.ResolvedAt = &now
	report.Status.ResolvedTime = &now
	if err := c.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		t.Fatalf("patch resolve: %v", err)
	}

	// Within ReopenWindow: a new signal must reopen the same incident, not
	// create a fresh one.
	clk.Advance(2 * time.Minute)
	in.ObservedAt = clk.Now()
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("reopen EnsureSignal: %v", err)
	}
	items = listIncidents(t, c)
	if len(items) != 1 {
		t.Fatalf("want exactly 1 incident (reopened), got %d", len(items))
	}
	if items[0].Status.Phase != PhaseDetecting {
		t.Errorf("reopened incident should be Detecting, got %q", items[0].Status.Phase)
	}
	if items[0].Status.ResolvedAt != nil {
		t.Errorf("ResolvedAt should be cleared on reopen, got %v", items[0].Status.ResolvedAt)
	}
}

func TestEnsureSignal_DoesNotReopenAfterReopenWindow(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	items := listIncidents(t, c)
	report := items[0].DeepCopy()
	base := report.DeepCopy()
	now := metav1.NewTime(clk.Now())
	report.Status.Phase = PhaseResolved
	report.Status.ResolvedAt = &now
	report.Status.ResolvedTime = &now
	if err := c.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
		t.Fatalf("patch resolve: %v", err)
	}

	// Past ReopenWindow → expect a brand-new incident.
	clk.Advance(ReopenWindow + time.Minute)
	in.ObservedAt = clk.Now()
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("post-window EnsureSignal: %v", err)
	}
	items = listIncidents(t, c)
	if len(items) != 2 {
		t.Fatalf("want 2 incidents (one resolved, one new) after reopen window, got %d", len(items))
	}
}

// --- ResolveForHealthyPod ---------------------------------------------------

func newReadyPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func newPendingPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

func TestResolveForHealthyPod_ResolvesWhenPodReadyAndCooldownElapsed(t *testing.T) {
	pod := newReadyPod("prod", "api-1")
	r, clk, c := newReporterFixture(t, pod)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Cooldown bypass: advance the clock past SignalCooldown.
	clk.Advance(SignalCooldown + time.Minute)

	if err := r.ResolveForHealthyPod(ctx, "prod", "api-1"); err != nil {
		t.Fatalf("ResolveForHealthyPod: %v", err)
	}

	items := listIncidents(t, c)
	if items[0].Status.Phase != PhaseResolved {
		t.Errorf("phase = %q, want Resolved", items[0].Status.Phase)
	}
	if items[0].Status.ResolvedAt == nil {
		t.Errorf("ResolvedAt should be set")
	}
}

func TestResolveForHealthyPod_NoOpInsideCooldown(t *testing.T) {
	pod := newReadyPod("prod", "api-1")
	r, clk, c := newReporterFixture(t, pod)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Inside cooldown — must NOT resolve.
	clk.Advance(time.Minute)

	if err := r.ResolveForHealthyPod(ctx, "prod", "api-1"); err != nil {
		t.Fatalf("ResolveForHealthyPod: %v", err)
	}
	items := listIncidents(t, c)
	if items[0].Status.Phase == PhaseResolved {
		t.Errorf("incident resolved inside cooldown window")
	}
}

func TestResolveForHealthyPod_NoOpWhenPodNotReady(t *testing.T) {
	pod := newPendingPod("prod", "api-1")
	r, clk, c := newReporterFixture(t, pod)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	clk.Advance(SignalCooldown + time.Minute)

	if err := r.ResolveForHealthyPod(ctx, "prod", "api-1"); err != nil {
		t.Fatalf("ResolveForHealthyPod: %v", err)
	}
	items := listIncidents(t, c)
	if items[0].Status.Phase == PhaseResolved {
		t.Errorf("pod is Pending — incident should not resolve")
	}
}

func TestResolveForHealthyPod_NoErrorWhenPodNotFound(t *testing.T) {
	r, _, _ := newReporterFixture(t)
	if err := r.ResolveForHealthyPod(context.Background(), "prod", "ghost"); err != nil {
		t.Fatalf("missing pod should be silently ignored, got %v", err)
	}
}

// --- ResolveForDeletedPod ---------------------------------------------------

func TestResolveForDeletedPod_ResolvesIgnoringCooldown(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	ctx := context.Background()
	in := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Even inside cooldown, deletion must resolve.
	clk.Advance(30 * time.Second)
	if err := r.ResolveForDeletedPod(ctx, "prod", "api-1"); err != nil {
		t.Fatalf("ResolveForDeletedPod: %v", err)
	}
	items := listIncidents(t, c)
	if items[0].Status.Phase != PhaseResolved {
		t.Errorf("deleted-pod resolve should bypass cooldown; phase = %q", items[0].Status.Phase)
	}
}

func TestResolveForDeletedPod_DoesNotTouchUnrelatedIncidents(t *testing.T) {
	r, clk, c := newReporterFixture(t)
	ctx := context.Background()
	a := crashLoopInput("prod", "api-1", "default-agent", clk.Now())
	b := crashLoopInput("prod", "worker-2", "default-agent", clk.Now())
	if err := r.EnsureSignal(ctx, a); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := r.EnsureSignal(ctx, b); err != nil {
		t.Fatalf("b: %v", err)
	}
	if err := r.ResolveForDeletedPod(ctx, "prod", "api-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	items := listIncidents(t, c)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	for _, it := range items {
		if it.Labels[LabelPodName] == "api-1" && it.Status.Phase != PhaseResolved {
			t.Errorf("api-1 incident should be Resolved")
		}
		if it.Labels[LabelPodName] == "worker-2" && it.Status.Phase == PhaseResolved {
			t.Errorf("worker-2 incident should NOT be Resolved")
		}
	}
}

// --- helpers under test ----------------------------------------------------

func TestIncidentAffectsPod_MatchesByPodKindNamespaceName(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Namespace: "prod", Name: "api-1"},
				{Kind: "Deployment", Namespace: "prod", Name: "api"},
			},
		},
	}
	if !incidentAffectsPod(report, "api-1", "prod") {
		t.Errorf("expected match")
	}
	if incidentAffectsPod(report, "api-1", "stage") {
		t.Errorf("namespace mismatch should not match")
	}
	if incidentAffectsPod(report, "other", "prod") {
		t.Errorf("name mismatch should not match")
	}
}

func TestIsPodCurrentlyReady(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{name: "nil", pod: nil, want: false},
		{name: "pending", pod: newPendingPod("ns", "p"), want: false},
		{
			name: "running but not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			want: false,
		},
		{name: "running and ready", pod: newReadyPod("ns", "p"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPodCurrentlyReady(tc.pod); got != tc.want {
				t.Errorf("isPodCurrentlyReady = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSafeLabelValue_TruncatesAndSanitizes(t *testing.T) {
	long := strings.Repeat("a", 70)
	got := safeLabelValue(long)
	if len(got) > 63 {
		t.Errorf("label value len = %d, want ≤63", len(got))
	}
	if got := safeLabelValue("Foo/Bar:baz_1"); got == "" {
		t.Errorf("expected non-empty sanitized value")
	}
	// safeLabelValue substitutes a placeholder for empty input so the label
	// always has a parseable value (Kubernetes rejects empty label values).
	if got := safeLabelValue(""); got == "" {
		t.Errorf("empty input should produce a non-empty placeholder, got empty")
	}
}

func TestHigherSeverity_PicksMoreSevere(t *testing.T) {
	cases := []struct {
		current, incoming, want string
	}{
		{"P3", "P1", "P1"},
		{"P1", "P3", "P1"},
		{"", "P2", "P2"},
		{"P2", "", "P2"},
		{"P4", "P3", "P3"},
	}
	for _, tc := range cases {
		if got := higherSeverity(tc.current, tc.incoming); got != tc.want {
			t.Errorf("higherSeverity(%q,%q)=%q want %q", tc.current, tc.incoming, got, tc.want)
		}
	}
}

func TestIncrementCounter_HandlesNonNumericGracefully(t *testing.T) {
	if got := incrementCounter("3"); got != "4" {
		t.Errorf("incrementCounter(\"3\") = %q, want \"4\"", got)
	}
	if got := incrementCounter(""); got != "1" {
		t.Errorf("incrementCounter(\"\") = %q, want \"1\"", got)
	}
	if got := incrementCounter("not-a-number"); got != "1" {
		t.Errorf("incrementCounter(\"not-a-number\") = %q, want \"1\"", got)
	}
}

func TestTrimSignals_CapsAtMaxSignalEntries(t *testing.T) {
	in := make([]string, MaxSignalEntries+5)
	for i := range in {
		in[i] = "s"
	}
	got := trimSignals(in)
	if len(got) != MaxSignalEntries {
		t.Errorf("trimSignals len = %d, want %d", len(got), MaxSignalEntries)
	}
}

func TestTruncateEventMessage_LimitsLength(t *testing.T) {
	long := strings.Repeat("x", 2048)
	got := truncateEventMessage(long)
	if len(got) >= len(long) {
		t.Errorf("truncateEventMessage should shorten input")
	}
}

func TestParseTraceIDCSV_TrimsAndDedupes(t *testing.T) {
	got := parseTraceIDCSV(" abc, def , abc , ghi ")
	want := []string{"abc", "def", "ghi"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestMergeTraceIDList_AppendsSingleIncomingDeduped(t *testing.T) {
	// mergeTraceIDList treats incoming as a single trace ID, not a CSV — it
	// appends iff incoming isn't already in the existing CSV.
	if got := mergeTraceIDList("a,b,c", "d"); got != "a,b,c,d" {
		t.Errorf("append new ID: got %q want a,b,c,d", got)
	}
	if got := mergeTraceIDList("a,b,c", "b"); got != "a,b,c" {
		t.Errorf("dedup existing ID: got %q want a,b,c", got)
	}
	if got := mergeTraceIDList("a,b,c", ""); got != "a,b,c" {
		t.Errorf("empty incoming should keep existing: got %q", got)
	}
}

func TestMergeTraceIDList_RespectsMaxTraceIDEntriesCap(t *testing.T) {
	parts := make([]string, MaxTraceIDEntries)
	for i := range parts {
		parts[i] = "id" + strings.Repeat("x", i+1)
	}
	existing := strings.Join(parts, ",")
	got := mergeTraceIDList(existing, "newId")
	gotParts := strings.Split(got, ",")
	if len(gotParts) != MaxTraceIDEntries {
		t.Errorf("expected merged list capped at %d, got %d", MaxTraceIDEntries, len(gotParts))
	}
	if gotParts[len(gotParts)-1] != "newId" {
		t.Errorf("newest ID should be at the tail, got %q", gotParts[len(gotParts)-1])
	}
}
