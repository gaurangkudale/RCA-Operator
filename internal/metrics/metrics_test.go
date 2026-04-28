package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func resetAll() {
	SignalsReceived.Reset()
	SignalsDeduplicated.Reset()
	IncidentsDetecting.Reset()
	IncidentsActivated.Reset()
	IncidentsResolved.Reset()
	ActiveIncidents.Reset()
	IncidentTransitionSeconds.Reset()
}

func TestRecordSignalReceived_BumpsCounterAndCoercesEmpty(t *testing.T) {
	resetAll()

	RecordSignalReceived("CrashLoopBackOff")
	RecordSignalReceived("CrashLoopBackOff")
	RecordSignalReceived("")

	if got := testutil.ToFloat64(SignalsReceived.WithLabelValues("CrashLoopBackOff")); got != 2 {
		t.Fatalf("want 2 CrashLoopBackOff signals, got %v", got)
	}
	if got := testutil.ToFloat64(SignalsReceived.WithLabelValues("unknown")); got != 1 {
		t.Fatalf("empty event_type should be coerced to 'unknown', got %v", got)
	}
}

func TestRecordSignalDeduplicated_BumpsCounter(t *testing.T) {
	resetAll()
	RecordSignalDeduplicated("OOMKilled")
	if got := testutil.ToFloat64(SignalsDeduplicated.WithLabelValues("OOMKilled")); got != 1 {
		t.Fatalf("want 1, got %v", got)
	}
}

func TestRecordIncidentDetecting_BumpsCounterAndGauge(t *testing.T) {
	resetAll()
	RecordIncidentDetecting("CrashLoopBackOff", "P2")
	RecordIncidentDetecting("CrashLoopBackOff", "P2")
	RecordIncidentDetecting("OOMKilled", "P2")

	if got := testutil.ToFloat64(IncidentsDetecting.WithLabelValues("CrashLoopBackOff", "P2")); got != 2 {
		t.Fatalf("detecting counter: want 2, got %v", got)
	}
	if got := testutil.ToFloat64(ActiveIncidents.WithLabelValues("CrashLoopBackOff", "P2")); got != 2 {
		t.Fatalf("active gauge after 2 detects: want 2, got %v", got)
	}
	if got := testutil.ToFloat64(ActiveIncidents.WithLabelValues("OOMKilled", "P2")); got != 1 {
		t.Fatalf("active gauge for second type: want 1, got %v", got)
	}
}

func TestRecordIncidentActivated_BumpsCounterAndObservesHistogram(t *testing.T) {
	resetAll()
	RecordIncidentActivated("CrashLoopBackOff", "P2", 45)
	RecordIncidentActivated("CrashLoopBackOff", "P2", 0) // 0 → no histogram observation

	if got := testutil.ToFloat64(IncidentsActivated.WithLabelValues("CrashLoopBackOff", "P2")); got != 2 {
		t.Fatalf("activated counter: want 2, got %v", got)
	}

	dump := histogramText(t)
	if !strings.Contains(dump, `from_phase="detecting",to_phase="active"`) {
		t.Fatalf("expected detecting→active label set in histogram, got:\n%s", dump)
	}
	// Only the 45-second observation should land in the >=30,<60 bucket; the
	// 0-second call must be skipped.
	if !strings.Contains(dump, `le="60"} 1`) {
		t.Fatalf("expected exactly one observation in le=60 bucket; histogram:\n%s", dump)
	}
}

func TestRecordIncidentResolved_DecrementsActiveGauge(t *testing.T) {
	resetAll()
	RecordIncidentDetecting("ImagePullBackOff", "P3")
	RecordIncidentDetecting("ImagePullBackOff", "P3")
	RecordIncidentResolved("ImagePullBackOff", "P3", "Active", 120)

	if got := testutil.ToFloat64(IncidentsResolved.WithLabelValues("ImagePullBackOff", "P3")); got != 1 {
		t.Fatalf("resolved counter: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(ActiveIncidents.WithLabelValues("ImagePullBackOff", "P3")); got != 1 {
		t.Fatalf("active gauge: want 1 after 2 detects + 1 resolve, got %v", got)
	}
	dump := histogramText(t)
	if !strings.Contains(dump, `from_phase="active",to_phase="resolved"`) {
		t.Fatalf("expected active→resolved series; got:\n%s", dump)
	}
}

func TestRecordIncidentResolved_FromDetecting(t *testing.T) {
	resetAll()
	RecordIncidentDetecting("OOMKilled", "P2")
	RecordIncidentResolved("OOMKilled", "P2", "Detecting", 30)

	dump := histogramText(t)
	if !strings.Contains(dump, `from_phase="detecting",to_phase="resolved"`) {
		t.Fatalf("expected detecting→resolved series; got:\n%s", dump)
	}
}

func TestRecordIncidentResolved_NormalizesUnknownFromPhase(t *testing.T) {
	resetAll()
	RecordIncidentDetecting("X", "P4")
	RecordIncidentResolved("X", "P4", "garbage", 5)
	dump := histogramText(t)
	if !strings.Contains(dump, `from_phase="unknown"`) {
		t.Fatalf("expected unknown from_phase normalization; got:\n%s", dump)
	}
}

func TestSetActiveIncidents_AbsoluteValue(t *testing.T) {
	resetAll()
	SetActiveIncidents("StalledRollout", "P2", 7)
	if got := testutil.ToFloat64(ActiveIncidents.WithLabelValues("StalledRollout", "P2")); got != 7 {
		t.Fatalf("want gauge=7, got %v", got)
	}
	SetActiveIncidents("StalledRollout", "P2", 3)
	if got := testutil.ToFloat64(ActiveIncidents.WithLabelValues("StalledRollout", "P2")); got != 3 {
		t.Fatalf("expected absolute reset to 3, got %v", got)
	}
}

func TestNormalizers_CoerceEmptyToUnknown(t *testing.T) {
	resetAll()
	RecordIncidentDetecting("", "")
	if got := testutil.ToFloat64(IncidentsDetecting.WithLabelValues("unknown", "unknown")); got != 1 {
		t.Fatalf("empty labels should be coerced to 'unknown'; got %v", got)
	}
}

func TestNormPhase_ClassifiesKnownAndUnknown(t *testing.T) {
	cases := map[string]string{
		"Detecting": "detecting",
		"detecting": "detecting",
		"Active":    "active",
		"active":    "active",
		"Resolved":  "resolved",
		"resolved":  "resolved",
		"":          "unknown",
		"weird":     "unknown",
	}
	for in, want := range cases {
		if got := normPhase(in); got != want {
			t.Errorf("normPhase(%q) = %q, want %q", in, got, want)
		}
	}
}

// histogramText serializes the transition histogram in Prometheus text
// exposition format so per-label assertions can be done with simple string
// matches instead of pulling in a dto-aware helper.
func histogramText(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	if err := testutil.CollectAndCompare(IncidentTransitionSeconds, strings.NewReader("")); err != nil {
		// CollectAndCompare returns a wrapped error containing the actual
		// metric output; that's exactly what we want to inspect.
		sb.WriteString(err.Error())
	}
	return sb.String()
}
