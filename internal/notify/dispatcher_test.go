package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// ── Pure helper functions ──────────────────────────────────────────────────────

func TestShouldPage_NilConfig_ReturnsFalse(t *testing.T) {
	if shouldPage(nil, "P1") {
		t.Error("shouldPage(nil, P1) should return false")
	}
}

func TestShouldPage_DefaultThreshold_P2(t *testing.T) {
	cfg := &rcav1alpha1.PagerDutyConfig{} // no Severity set → defaults to P2
	if !shouldPage(cfg, "P1") {
		t.Error("P1 >= P2 threshold should page")
	}
	if !shouldPage(cfg, "P2") {
		t.Error("P2 >= P2 threshold should page")
	}
	if shouldPage(cfg, "P3") {
		t.Error("P3 < P2 threshold should not page")
	}
	if shouldPage(cfg, "P4") {
		t.Error("P4 < P2 threshold should not page")
	}
}

func TestShouldPage_ExplicitP1Threshold(t *testing.T) {
	cfg := &rcav1alpha1.PagerDutyConfig{Severity: "P1"}
	if !shouldPage(cfg, "P1") {
		t.Error("P1 >= P1 threshold should page")
	}
	if shouldPage(cfg, "P2") {
		t.Error("P2 < P1 threshold should not page")
	}
}

func TestPagerDutyAction_Resolve(t *testing.T) {
	if got := pagerDutyAction("resolve"); got != "resolve" {
		t.Errorf("got %q, want resolve", got)
	}
}

func TestPagerDutyAction_Trigger(t *testing.T) {
	if got := pagerDutyAction("trigger"); got != "trigger" {
		t.Errorf("got %q, want trigger", got)
	}
	if got := pagerDutyAction("detect"); got != "trigger" {
		t.Errorf("non-resolve action should return trigger, got %q", got)
	}
}

func TestPagerDutySeverity_Mapping(t *testing.T) {
	tests := []struct{ in, want string }{
		{"P1", "critical"},
		{"P2", "error"},
		{"P3", "warning"},
		{"P4", "info"},
		{"", "info"},
	}
	for _, tt := range tests {
		got := pagerDutySeverity(tt.in)
		if got != tt.want {
			t.Errorf("pagerDutySeverity(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNotificationSummary_FromStatusSummary(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	report.Status.Summary = "pod crash detected"
	if got := notificationSummary(report); got != "pod crash detected" {
		t.Errorf("expected summary from Status.Summary, got %q", got)
	}
}

// func TestNotificationSummary_FromTimeline(t *testing.T) {
// 	report := &rcav1alpha1.IncidentReport{}
// 	report.Status.Timeline = []rcav1alpha1.TimelineEntry{
// 		{Event: "first event"},
// 		{Event: "latest event"},
// 	}
// 	if got := notificationSummary(report); got != "latest event" {
// 		t.Errorf("expected last timeline entry, got %q", got)
// 	}
// }

func TestNotificationSummary_Fallback(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	report.Spec.IncidentType = "CrashLoopBackOff"
	report.Namespace = "prod"
	got := notificationSummary(report)
	if !strings.Contains(got, "CrashLoopBackOff") {
		t.Errorf("fallback summary should mention incident type, got %q", got)
	}
}

func TestSlackMessage_Triggered_P1_WithMention(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	report.Spec.IncidentType = "NodeFailure"
	report.Spec.AgentRef = "rca-agent"
	report.Namespace = "prod"
	report.Status.Severity = "P1"
	report.Status.Summary = "node not ready"

	cfg := &rcav1alpha1.SlackConfig{MentionOnP1: "@oncall"}
	msg := slackMessage(report, cfg, "trigger")

	if !strings.Contains(msg, "@oncall") {
		t.Error("P1 trigger should mention oncall")
	}
	if !strings.Contains(msg, "TRIGGERED") {
		t.Error("trigger action should show TRIGGERED state")
	}
}

func TestSlackMessage_Resolved_NoMention(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	report.Spec.IncidentType = "NodeFailure"
	report.Spec.AgentRef = "rca-agent"
	report.Status.Severity = "P1"
	report.Status.Summary = "node recovered"

	cfg := &rcav1alpha1.SlackConfig{MentionOnP1: "@oncall"}
	msg := slackMessage(report, cfg, "resolve")

	if strings.Contains(msg, "@oncall") {
		t.Error("resolve action should not mention oncall")
	}
	if !strings.Contains(msg, "RESOLVED") {
		t.Error("resolve action should show RESOLVED state")
	}
}

func TestSlackMessage_Triggered_P3_NoMention(t *testing.T) {
	report := &rcav1alpha1.IncidentReport{}
	report.Status.Severity = "P3"
	cfg := &rcav1alpha1.SlackConfig{MentionOnP1: "@oncall"}
	msg := slackMessage(report, cfg, "trigger")
	if strings.Contains(msg, "@oncall") {
		t.Error("P3 trigger should not mention oncall")
	}
}

// ── HTTP dispatch ─────────────────────────────────────────────────────────────

func TestPostJSON_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type: application/json")
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte("text")) {
			t.Error("expected body to contain 'text'")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	d := NewDispatcher(nil, logr.Discard())
	payload, _ := json.Marshal(map[string]string{"text": "hello"})
	if err := d.postJSON(context.Background(), ts.URL, payload, "TestService"); err != nil {
		t.Errorf("postJSON should not fail on 200: %v", err)
	}
}

func TestPostJSON_NonSuccessStatus_ReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer ts.Close()

	d := NewDispatcher(nil, logr.Discard())
	payload, _ := json.Marshal(map[string]string{"x": "y"})
	err := d.postJSON(context.Background(), ts.URL, payload, "TestService")
	if err == nil {
		t.Error("expected error on 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestPostJSON_InvalidURL_ReturnsError(t *testing.T) {
	d := NewDispatcher(nil, logr.Discard())
	err := d.postJSON(context.Background(), "http://127.0.0.1:0/", []byte("{}"), "TestService")
	if err == nil {
		t.Error("expected error for unreachable URL")
	}
}

// ── NotifyIncident nil guards ──────────────────────────────────────────────────

func TestNotifyIncident_NilDispatcher_ReturnsNil(t *testing.T) {
	var d *Dispatcher
	if err := d.NotifyIncident(context.Background(), &rcav1alpha1.IncidentReport{}, "trigger"); err != nil {
		t.Errorf("nil dispatcher should return nil, got %v", err)
	}
}

func TestNotifyIncident_NilReport_ReturnsNil(t *testing.T) {
	d := NewDispatcher(nil, logr.Discard())
	if err := d.NotifyIncident(context.Background(), nil, "trigger"); err != nil {
		t.Errorf("nil report should return nil, got %v", err)
	}
}
