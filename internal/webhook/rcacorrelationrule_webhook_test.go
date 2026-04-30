package webhook

import (
	"context"
	"strings"
	"testing"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// validRule builds a minimally-valid RCACorrelationRule that callers can
// mutate per case.
func validRule() *rcav1alpha1.RCACorrelationRule {
	return &rcav1alpha1.RCACorrelationRule{
		Spec: rcav1alpha1.RCACorrelationRuleSpec{
			Priority: 100,
			Trigger:  rcav1alpha1.RuleTrigger{EventType: "CrashLoopBackOff"},
			Conditions: []rcav1alpha1.RuleCondition{
				{EventType: "OOMKilled", Scope: "samePod"},
			},
			Fires: rcav1alpha1.RuleFires{
				IncidentType: "CrashLoopWithOOM",
				Severity:     "P2",
			},
		},
	}
}

func TestRCACorrelationRuleWebhook_ValidateCreate(t *testing.T) {
	w := &RCACorrelationRuleWebhook{}
	ctx := context.Background()

	cases := []struct {
		name    string
		mutate  func(*rcav1alpha1.RCACorrelationRule)
		wantErr string
	}{
		{
			name:   "minimal valid rule",
			mutate: func(*rcav1alpha1.RCACorrelationRule) {},
		},
		{
			name:    "priority below 1 is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Priority = 0 },
			wantErr: "spec.priority must be >= 1",
		},
		{
			name:    "priority negative is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Priority = -5 },
			wantErr: "spec.priority must be >= 1",
		},
		{
			name:    "trigger event type unknown is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Trigger.EventType = "MadeUp" },
			wantErr: "spec.trigger.eventType",
		},
		{
			name:    "fires severity unknown is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Fires.Severity = "P9" },
			wantErr: "must be one of P1, P2, P3, P4",
		},
		{
			name:    "condition event type unknown is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Conditions[0].EventType = "Bogus" },
			wantErr: "spec.conditions[0].eventType",
		},
		{
			name:    "condition scope unknown is rejected",
			mutate:  func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Conditions[0].Scope = "global" },
			wantErr: "spec.conditions[0].scope",
		},
		{
			// Regression: sameTrace is allowed by the CRD enum but was
			// previously rejected by the webhook.
			name:   "sameTrace scope is allowed",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) { r.Spec.Conditions[0].Scope = "sameTrace" },
		},
		{
			name: "OTelSpanError trigger + OTelLogMatch condition is allowed",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Trigger.EventType = "OTelSpanError"
				r.Spec.Conditions[0].EventType = "OTelLogMatch"
				r.Spec.Conditions[0].Scope = "sameTrace"
			},
		},
		{
			name: "newer workload event types are allowed",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Trigger.EventType = "JobFailed"
				r.Spec.Conditions[0].EventType = "CronJobFailed"
				r.Spec.Conditions[0].Scope = "sameNamespace"
			},
		},
		{
			name: "rule with no conditions still validates trigger and fires",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions = nil
			},
		},
		{
			name: "second condition with bad scope is rejected with index",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions = append(r.Spec.Conditions, rcav1alpha1.RuleCondition{EventType: "ProbeFailure", Scope: "WRONG"})
			},
			wantErr: "spec.conditions[1].scope",
		},
		{
			name: "attribute empty key is rejected",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions[0].Attributes = []rcav1alpha1.AttributeMatch{{Op: "Equals", Value: "500"}}
			},
			wantErr: "spec.conditions[0].attributes[0].key",
		},
		{
			name: "attribute unknown op is rejected",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions[0].Attributes = []rcav1alpha1.AttributeMatch{{Key: "http.status_code", Op: "Between", Value: "500"}}
			},
			wantErr: "spec.conditions[0].attributes[0].op",
		},
		{
			name: "attribute invalid regex is rejected",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions[0].Attributes = []rcav1alpha1.AttributeMatch{{Key: "span.name", Op: "Regex", Value: "[invalid("}}
			},
			wantErr: "must be a valid RE2 regex",
		},
		{
			name: "attribute numeric op rejects non-numeric value",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions[0].Attributes = []rcav1alpha1.AttributeMatch{{Key: "http.status_code", Op: "Gte", Value: "five-hundred"}}
			},
			wantErr: "must be numeric",
		},
		{
			name: "valid attributes are accepted",
			mutate: func(r *rcav1alpha1.RCACorrelationRule) {
				r.Spec.Conditions[0].Attributes = []rcav1alpha1.AttributeMatch{
					{Key: "span.name", Op: "Regex", Value: `^GET /api/.*$`},
					{Key: "http.status_code", Op: "Gte", Value: "500"},
					{Key: "trace.id", Op: "Exists"},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRule()
			tc.mutate(r)
			_, err := w.ValidateCreate(ctx, r)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestRCACorrelationRuleWebhook_ValidateUpdate_ReusesValidator(t *testing.T) {
	w := &RCACorrelationRuleWebhook{}
	old := validRule()
	bad := validRule()
	bad.Spec.Priority = 0
	if _, err := w.ValidateUpdate(context.Background(), old, bad); err == nil {
		t.Fatalf("update with priority=0 should fail")
	}
}

func TestRCACorrelationRuleWebhook_ValidateDelete_AlwaysAllows(t *testing.T) {
	w := &RCACorrelationRuleWebhook{}
	if _, err := w.ValidateDelete(context.Background(), validRule()); err != nil {
		t.Fatalf("ValidateDelete should never error, got %v", err)
	}
}

// TestValidEventTypes_AllowsExpectedSet is a guard rail: if the watcher event
// types in internal/watcher/events.go change, this list must be updated. The
// webhook must always be a superset of what watchers can emit.
func TestValidEventTypes_AllowsExpectedSet(t *testing.T) {
	must := []string{
		"CrashLoopBackOff", "OOMKilled", "ImagePullBackOff", "PodPendingTooLong",
		"GracePeriodViolation", "PodHealthy", "PodDeleted", "ProbeFailure",
		"NodeNotReady", "PodEvicted", "NodePressure",
		"StalledRollout", "StalledStatefulSet", "StalledDaemonSet", "JobFailed", "CronJobFailed",
		"OTelSpanError", "OTelSpanLatencySpike", "OTelLogMatch", "OTelSpanEvent",
	}
	for _, et := range must {
		if !watcher.IsKnownEventType(et) {
			t.Errorf("watcher known event types missing %q — webhook will reject otherwise-valid CRs", et)
		}
	}
}

func TestValidScopes_AllowsExpectedSet(t *testing.T) {
	must := []string{"samePod", "sameNode", "sameNamespace", "sameTrace", "any"}
	for _, s := range must {
		if !validScopes[s] {
			t.Errorf("validScopes missing %q", s)
		}
	}
}
