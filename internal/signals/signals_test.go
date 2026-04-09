package signals

import (
	"testing"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// ── Deduplicator ─────────────────────────────────────────────────────────────

func TestDeduplicator_NewKey_NotDuplicate(t *testing.T) {
	d := NewDeduplicator(1 * time.Minute)
	if d.IsDuplicate("key-a") {
		t.Error("first occurrence should not be a duplicate")
	}
}

func TestDeduplicator_SameKeyWithinWindow_IsDuplicate(t *testing.T) {
	d := NewDeduplicator(1 * time.Minute)
	d.IsDuplicate("key-b") // record first occurrence
	if !d.IsDuplicate("key-b") {
		t.Error("second occurrence within window should be a duplicate")
	}
}

func TestDeduplicator_SameKeyAfterWindow_NotDuplicate(t *testing.T) {
	now := time.Now()
	d := NewDeduplicator(1 * time.Minute)
	// prime with a fake clock set to 2 minutes ago
	d.nowFn = func() time.Time { return now.Add(-2 * time.Minute) }
	d.IsDuplicate("key-c")
	// advance clock past TTL
	d.nowFn = func() time.Time { return now }
	if d.IsDuplicate("key-c") {
		t.Error("occurrence after TTL should not be a duplicate")
	}
}

func TestDeduplicator_DifferentKeys_IndependentWindows(t *testing.T) {
	d := NewDeduplicator(1 * time.Minute)
	d.IsDuplicate("k1")
	d.IsDuplicate("k2")
	if !d.IsDuplicate("k1") {
		t.Error("k1 should still be a duplicate")
	}
	if !d.IsDuplicate("k2") {
		t.Error("k2 should still be a duplicate")
	}
}

func TestDeduplicator_Purge_RemovesExpiredKeys(t *testing.T) {
	now := time.Now()
	d := NewDeduplicator(30 * time.Second)
	d.nowFn = func() time.Time { return now.Add(-1 * time.Minute) } // 1m ago
	d.IsDuplicate("old-key")

	// Advance clock: purge fires on next IsDuplicate call
	d.nowFn = func() time.Time { return now }
	if d.IsDuplicate("old-key") {
		t.Error("old-key should have been purged and not be a duplicate")
	}
}

// ── DefaultMappings ───────────────────────────────────────────────────────────

func TestDefaultMappings_ContainsExpectedTypes(t *testing.T) {
	mappings := DefaultMappings()
	if len(mappings) == 0 {
		t.Fatal("DefaultMappings should not be empty")
	}

	required := []string{
		"CrashLoopBackOff", "OOMKilled", "ImagePullBackOff",
		"NodeNotReady", "PodEvicted", "StalledRollout",
		"JobFailed", "CronJobFailed",
	}
	byType := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		byType[m.EventType] = true
	}
	for _, et := range required {
		if !byType[et] {
			t.Errorf("DefaultMappings missing event type %q", et)
		}
	}
}

func TestDefaultMappings_SeveritiesValid(t *testing.T) {
	valid := map[string]bool{"P1": true, "P2": true, "P3": true, "P4": true}
	for _, m := range DefaultMappings() {
		if !valid[m.Severity] {
			t.Errorf("mapping %q has invalid severity %q", m.EventType, m.Severity)
		}
	}
}

// ── Normalizer ────────────────────────────────────────────────────────────────

func makeCrashEvent() watcher.CrashLoopBackOffEvent {
	return watcher.CrashLoopBackOffEvent{
		BaseEvent: watcher.BaseEvent{
			PodName:   "myapp-xyz",
			Namespace: "default",
			AgentName: "rca-agent",
		},
		RestartCount: 5,
		Threshold:    3,
	}
}

func TestNormalizer_Normalize_KnownEvent_ReturnsSignal(t *testing.T) {
	n := NewNormalizer(nil)
	sig, ok := n.Normalize(makeCrashEvent())
	if !ok {
		t.Fatal("expected ok=true for CrashLoopBackOff event")
	}
	if sig.IncidentType != "CrashLoopBackOff" {
		t.Errorf("expected incident type CrashLoopBackOff, got %q", sig.IncidentType)
	}
	if sig.Severity != "P3" {
		t.Errorf("expected severity P3, got %q", sig.Severity)
	}
	if sig.Namespace != "default" {
		t.Errorf("expected namespace default, got %q", sig.Namespace)
	}
}

func TestNormalizer_Normalize_PodHealthy_ReturnsFalse(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{PodName: "p", Namespace: "ns"},
	}
	_, ok := n.Normalize(ev)
	if ok {
		t.Error("PodHealthy should not be normalized into an incident signal")
	}
}

func TestNormalizer_Normalize_PodDeleted_ReturnsFalse(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.PodDeletedEvent{
		BaseEvent: watcher.BaseEvent{PodName: "p", Namespace: "ns"},
	}
	_, ok := n.Normalize(ev)
	if ok {
		t.Error("PodDeleted should not be normalized into an incident signal")
	}
}

func TestNormalizer_Override_ReplacesDefaultSeverity(t *testing.T) {
	override := SignalMapping{
		EventType:    "CrashLoopBackOff",
		IncidentType: "CrashLoopBackOff",
		Severity:     "P1", // escalate from default P3
		ScopeLevel:   "Pod",
	}
	n := NewNormalizer([]SignalMapping{override})
	sig, ok := n.Normalize(makeCrashEvent())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if sig.Severity != "P1" {
		t.Errorf("expected overridden severity P1, got %q", sig.Severity)
	}
}

func TestNormalizer_Normalize_OOMKilled(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.OOMKilledEvent{
		BaseEvent: watcher.BaseEvent{PodName: "oom-pod", Namespace: "prod"},
		ExitCode:  137,
		Reason:    "OOMKilled",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for OOMKilled")
	}
	if sig.Severity != "P2" {
		t.Errorf("OOMKilled expected P2, got %q", sig.Severity)
	}
}

func TestNormalizer_Normalize_NodeNotReady_ClusterScope(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.NodeNotReadyEvent{
		BaseEvent: watcher.BaseEvent{NodeName: "node-1", Namespace: ""},
		Reason:    "KubeletNotReady",
		Message:   "node pressure",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for NodeNotReady")
	}
	if sig.Severity != "P1" {
		t.Errorf("NodeNotReady expected P1, got %q", sig.Severity)
	}
}

func TestNormalizer_Normalize_StalledRollout_WorkloadRef(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.StalledRolloutEvent{
		BaseEvent:       watcher.BaseEvent{Namespace: "staging"},
		DeploymentName:  "my-deploy",
		DesiredReplicas: 3,
		ReadyReplicas:   1,
		Reason:          "ProgressDeadlineExceeded",
		Message:         "stalled",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for StalledRollout")
	}
	if sig.Scope.WorkloadRef == nil {
		t.Error("expected WorkloadRef to be set for StalledRollout")
	} else if sig.Scope.WorkloadRef.Name != "my-deploy" {
		t.Errorf("WorkloadRef.Name = %q, want my-deploy", sig.Scope.WorkloadRef.Name)
	}
}

func TestNormalizer_Normalize_NodePressure_PIDPressure_P3Override(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.NodePressureEvent{
		BaseEvent:    watcher.BaseEvent{NodeName: "node-2"},
		PressureType: "PIDPressure",
		Message:      "too many processes",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for NodePressure(PIDPressure)")
	}
	// PIDPressure overrides the default P2 → P3
	if sig.Severity != "P3" {
		t.Errorf("PIDPressure expected P3, got %q", sig.Severity)
	}
}

func TestNormalizer_Normalize_JobFailed(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.JobFailedEvent{
		BaseEvent: watcher.BaseEvent{Namespace: "batch"},
		JobName:   "etl-job",
		Reason:    "BackoffLimitExceeded",
		Message:   "job failed",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for JobFailed")
	}
	if sig.Scope.WorkloadRef == nil {
		t.Error("expected WorkloadRef for JobFailed")
	}
	if sig.Scope.WorkloadRef.Kind != "Job" {
		t.Errorf("expected Kind Job, got %q", sig.Scope.WorkloadRef.Kind)
	}
}

func TestNormalizer_Normalize_CronJobFailed(t *testing.T) {
	n := NewNormalizer(nil)
	ev := watcher.CronJobFailedEvent{
		BaseEvent:   watcher.BaseEvent{Namespace: "batch"},
		CronJobName: "nightly-backup",
		LastJobName: "nightly-backup-28500",
		Reason:      "BackoffLimitExceeded",
		Message:     "cronjob failed",
	}
	sig, ok := n.Normalize(ev)
	if !ok {
		t.Fatal("expected ok=true for CronJobFailed")
	}
	if sig.Scope.WorkloadRef == nil {
		t.Error("expected WorkloadRef for CronJobFailed")
	}
	if sig.Scope.WorkloadRef.Kind != "CronJob" {
		t.Errorf("expected Kind CronJob, got %q", sig.Scope.WorkloadRef.Kind)
	}
}

// ── GuessDeploymentNameFromPod ────────────────────────────────────────────────

func TestGuessDeploymentNameFromPod(t *testing.T) {
	tests := []struct {
		pod      string
		expected string
	}{
		// Standard 3-part name: <deploy>-<rs-hash>-<pod-hash>
		{"my-app-7d9f5b4d8c-xk2zp", "my-app"},
		// Multi-word deploy name
		{"payment-service-6b8f4c7d9b-abc12", "payment-service"},
		// Only two parts: strip last segment (pod hash)
		{"myapp-xk2zp", "myapp"},
		// Single token — no separator → empty
		{"mypod", ""},
		// Empty string
		{"", ""},
		// Name with exactly 2 parts but pod hash length ≠ 5
		{"myapp-toolong", ""},
	}

	for _, tt := range tests {
		t.Run(tt.pod, func(t *testing.T) {
			got := GuessDeploymentNameFromPod(tt.pod)
			if got != tt.expected {
				t.Errorf("GuessDeploymentNameFromPod(%q) = %q, want %q", tt.pod, got, tt.expected)
			}
		})
	}
}
