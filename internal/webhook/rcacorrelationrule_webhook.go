package webhook

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// validEventTypes mirrors the EventType constants in
// internal/watcher/events.go. Keep in sync — the webhook rejects CRs with
// event types the operator can never emit.
var validEventTypes = map[string]bool{
	// Pod-level
	"CrashLoopBackOff":     true,
	"OOMKilled":            true,
	"ImagePullBackOff":     true,
	"PodPendingTooLong":    true,
	"GracePeriodViolation": true,
	"PodHealthy":           true,
	"PodDeleted":           true,
	"ProbeFailure":         true,
	// Node-level
	"NodeNotReady": true,
	"PodEvicted":   true,
	"NodePressure": true,
	// Workload-level
	"StalledRollout":     true,
	"StalledStatefulSet": true,
	"StalledDaemonSet":   true,
	"JobFailed":          true,
	"CronJobFailed":      true,
	// OTel-derived
	"OTelSpanError":        true,
	"OTelSpanLatencySpike": true,
	"OTelLogMatch":         true,
	"OTelSpanEvent":        true,
}

var validSeverities = map[string]bool{
	"P1": true, "P2": true, "P3": true, "P4": true,
}

// validScopes mirrors api/v1alpha1/rcacorrelationrule_types.go RuleCondition.Scope enum.
var validScopes = map[string]bool{
	"samePod":       true,
	"sameNode":      true,
	"sameNamespace": true,
	"sameTrace":     true,
	"any":           true,
}

// RCACorrelationRuleWebhook implements typed validating webhook for RCACorrelationRule.
type RCACorrelationRuleWebhook struct{}

// SetupRCACorrelationRuleWebhookWithManager registers the webhook.
func SetupRCACorrelationRuleWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &rcav1alpha1.RCACorrelationRule{}).
		WithValidator(&RCACorrelationRuleWebhook{}).
		Complete()
}

// ValidateCreate implements admission.Validator[*rcav1alpha1.RCACorrelationRule].
func (w *RCACorrelationRuleWebhook) ValidateCreate(_ context.Context, rule *rcav1alpha1.RCACorrelationRule) (admission.Warnings, error) {
	return validateRule(rule)
}

// ValidateUpdate implements admission.Validator[*rcav1alpha1.RCACorrelationRule].
func (w *RCACorrelationRuleWebhook) ValidateUpdate(_ context.Context, _, rule *rcav1alpha1.RCACorrelationRule) (admission.Warnings, error) {
	return validateRule(rule)
}

// ValidateDelete implements admission.Validator[*rcav1alpha1.RCACorrelationRule].
func (w *RCACorrelationRuleWebhook) ValidateDelete(_ context.Context, _ *rcav1alpha1.RCACorrelationRule) (admission.Warnings, error) {
	return nil, nil
}

func validateRule(rule *rcav1alpha1.RCACorrelationRule) (admission.Warnings, error) {
	spec := rule.Spec
	if spec.Priority < 1 {
		return nil, fmt.Errorf("spec.priority must be >= 1")
	}
	if !validEventTypes[spec.Trigger.EventType] {
		return nil, fmt.Errorf("spec.trigger.eventType %q is not a known event type", spec.Trigger.EventType)
	}
	if !validSeverities[spec.Fires.Severity] {
		return nil, fmt.Errorf("spec.fires.severity %q must be one of P1, P2, P3, P4", spec.Fires.Severity)
	}
	for i, cond := range spec.Conditions {
		if !validEventTypes[cond.EventType] {
			return nil, fmt.Errorf("spec.conditions[%d].eventType %q is not a known event type", i, cond.EventType)
		}
		if !validScopes[cond.Scope] {
			return nil, fmt.Errorf("spec.conditions[%d].scope %q must be one of samePod, sameNode, sameNamespace, sameTrace, any", i, cond.Scope)
		}
	}

	return nil, nil
}
