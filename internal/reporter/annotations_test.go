package reporter

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gaurangkudale/rca-operator/internal/incident"
)

func TestBuildInitialAnnotations_WithTraceIDAndFiredRule(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC))
	in := incident.Input{
		Summary:   "summary",
		DedupKey:  "dedup",
		TraceID:   "abc123",
		FiredRule: "RuleX",
	}

	got := buildInitialAnnotations(in, now)

	if got[AnnotationTraceID] != "abc123" {
		t.Errorf("AnnotationTraceID = %q, want %q", got[AnnotationTraceID], "abc123")
	}
	if got[AnnotationFiredRule] != "RuleX" {
		t.Errorf("AnnotationFiredRule = %q, want %q", got[AnnotationFiredRule], "RuleX")
	}
	if got[AnnotationSignal] != "summary" {
		t.Errorf("AnnotationSignal = %q, want %q", got[AnnotationSignal], "summary")
	}
	if got[AnnotationDedupKey] != "dedup" {
		t.Errorf("AnnotationDedupKey = %q, want %q", got[AnnotationDedupKey], "dedup")
	}
	if got[AnnotationSignalSeen] != "1" {
		t.Errorf("AnnotationSignalSeen = %q, want %q", got[AnnotationSignalSeen], "1")
	}
}

func TestBuildInitialAnnotations_WithoutDiagnostics(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC))
	in := incident.Input{Summary: "s", DedupKey: "d"}

	got := buildInitialAnnotations(in, now)

	if _, ok := got[AnnotationTraceID]; ok {
		t.Errorf("AnnotationTraceID should not be present when TraceID is empty")
	}
	if _, ok := got[AnnotationFiredRule]; ok {
		t.Errorf("AnnotationFiredRule should not be present when FiredRule is empty")
	}
}

// TestApplyDiagnosticAnnotations_PreservesExistingOnEmpty verifies the
// preserve-on-empty contract: a follow-up signal without a TraceID must not
// clear the trace-id the first OTel-sourced signal recorded.
func TestApplyDiagnosticAnnotations_PreservesExistingOnEmpty(t *testing.T) {
	annotations := map[string]string{
		AnnotationTraceID:   "preexisting-trace",
		AnnotationFiredRule: "PreexistingRule",
	}
	applyDiagnosticAnnotations(annotations, incident.Input{})

	if annotations[AnnotationTraceID] != "preexisting-trace" {
		t.Errorf("existing trace-id should be preserved, got %q", annotations[AnnotationTraceID])
	}
	if annotations[AnnotationFiredRule] != "PreexistingRule" {
		t.Errorf("existing fired-rule should be preserved, got %q", annotations[AnnotationFiredRule])
	}
}

func TestApplyDiagnosticAnnotations_OverwritesWithNewValues(t *testing.T) {
	annotations := map[string]string{
		AnnotationTraceID:   "old-trace",
		AnnotationFiredRule: "OldRule",
	}
	applyDiagnosticAnnotations(annotations, incident.Input{TraceID: "new-trace", FiredRule: "NewRule"})

	if annotations[AnnotationTraceID] != "new-trace" {
		t.Errorf("trace-id should be updated, got %q", annotations[AnnotationTraceID])
	}
	if annotations[AnnotationFiredRule] != "NewRule" {
		t.Errorf("fired-rule should be updated, got %q", annotations[AnnotationFiredRule])
	}
}
