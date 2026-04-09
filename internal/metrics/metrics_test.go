package metrics

import (
	"testing"
)

// ── safe() ────────────────────────────────────────────────────────────────────

func TestSafe_EmptyString_ReturnsUnknown(t *testing.T) {
	if got := safe(""); got != "unknown" {
		t.Errorf("safe(\"\") = %q, want \"unknown\"", got)
	}
}

func TestSafe_NonEmpty_PassThrough(t *testing.T) {
	cases := []string{"P1", "CrashLoopBackOff", "rca-agent", "prod"}
	for _, v := range cases {
		if got := safe(v); got != v {
			t.Errorf("safe(%q) = %q, want %q", v, got, v)
		}
	}
}

// ── Counter / gauge recording — smoke tests ───────────────────────────────────
// These verify the public API does not panic and the Prometheus descriptors are
// registered (registerOnce fires in init). They do not assert counter values
// because the controller-runtime test registry shares state across tests.

func TestRecordSignalReceived_NoPanic(t *testing.T) {
	RecordSignalReceived("CrashLoopBackOff", "test-agent")
}

func TestRecordSignalDeduplicated_NoPanic(t *testing.T) {
	RecordSignalDeduplicated("OOMKilled")
}

func TestRecordIncidentDetecting_NoPanic(t *testing.T) {
	RecordIncidentDetecting("test-agent", "CrashLoopBackOff", "P3")
}

func TestRecordIncidentActivated_NoPanic(t *testing.T) {
	RecordIncidentActivated("test-agent", "NodeFailure", "P1")
}

func TestRecordIncidentResolved_NoPanic(t *testing.T) {
	RecordIncidentResolved("test-agent", "NodeFailure", "P1")
}

func TestSetActiveIncidents_NoPanic(t *testing.T) {
	SetActiveIncidents("test-agent", "CrashLoopBackOff", "P3", 2)
}

func TestIncDecActiveIncidents_NoPanic(t *testing.T) {
	IncActiveIncidents("test-agent", "CrashLoopBackOff", "P3")
	DecActiveIncidents("test-agent", "CrashLoopBackOff", "P3")
}

func TestObserveIncidentTransition_NoPanic(t *testing.T) {
	ObserveIncidentTransition("Detecting", "Active", 30.5)
	ObserveIncidentTransition("Active", "Resolved", 120.0)
}

func TestRecordSignalProcessed_NoPanic(t *testing.T) {
	RecordSignalProcessed("ImagePullBackOff", "agent-1")
}

func TestObserveSignalDuration_NoPanic(t *testing.T) {
	ObserveSignalDuration("CrashLoopBackOff", 0.05)
}

func TestRecordRuleEvaluation_NoPanic(t *testing.T) {
	RecordRuleEvaluation("rule-crashloop", true)
	RecordRuleEvaluation("rule-imagepull", false)
}

func TestSetCorrelationBufferSize_NoPanic(t *testing.T) {
	SetCorrelationBufferSize("test-agent", 42)
}

func TestRecordNotification_NoPanic(t *testing.T) {
	RecordNotification("slack", "trigger", "success", "P1")
	RecordNotification("pagerduty", "resolve", "error", "P2")
}

func TestObserveNotificationDuration_NoPanic(t *testing.T) {
	ObserveNotificationDuration("slack", 0.12)
}

// ── Backward-compat aliases ───────────────────────────────────────────────────

func TestRecordIncidentDetected_Alias_NoPanic(t *testing.T) {
	RecordIncidentDetected("agent", "OOMKilled", "P2")
}

func TestSetIncidentsActive_Alias_NoPanic(t *testing.T) {
	SetIncidentsActive("agent", "OOMKilled", "P2", 1)
}
