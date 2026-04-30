package webhook

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const (
	attributeOpEquals      = "Equals"
	attributeOpNotEquals   = "NotEquals"
	attributeOpContains    = "Contains"
	attributeOpNotContains = "NotContains"
	attributeOpRegex       = "Regex"
	attributeOpExists      = "Exists"
	attributeOpNotExists   = "NotExists"
	attributeOpGte         = "Gte"
	attributeOpLte         = "Lte"
	attributeOpGt          = "Gt"
	attributeOpLt          = "Lt"
)

var validSeverities = map[string]bool{
	"P1": true, "P2": true, "P3": true, "P4": true,
}

var validAttributeOps = map[string]bool{
	"":                     true,
	attributeOpEquals:      true,
	attributeOpNotEquals:   true,
	attributeOpContains:    true,
	attributeOpNotContains: true,
	attributeOpRegex:       true,
	attributeOpExists:      true,
	attributeOpNotExists:   true,
	attributeOpGte:         true,
	attributeOpLte:         true,
	attributeOpGt:          true,
	attributeOpLt:          true,
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
	if !watcher.IsKnownEventType(spec.Trigger.EventType) {
		return nil, fmt.Errorf("spec.trigger.eventType %q is not a known event type", spec.Trigger.EventType)
	}
	if !validSeverities[spec.Fires.Severity] {
		return nil, fmt.Errorf("spec.fires.severity %q must be one of P1, P2, P3, P4", spec.Fires.Severity)
	}
	for i, cond := range spec.Conditions {
		if !watcher.IsKnownEventType(cond.EventType) {
			return nil, fmt.Errorf("spec.conditions[%d].eventType %q is not a known event type", i, cond.EventType)
		}
		if !validScopes[cond.Scope] {
			return nil, fmt.Errorf("spec.conditions[%d].scope %q must be one of samePod, sameNode, sameNamespace, sameTrace, any", i, cond.Scope)
		}
		if err := validateAttributeMatches(i, cond.Attributes); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func validateAttributeMatches(conditionIndex int, matches []rcav1alpha1.AttributeMatch) error {
	for i, match := range matches {
		field := fmt.Sprintf("spec.conditions[%d].attributes[%d]", conditionIndex, i)
		if match.Key == "" {
			return fmt.Errorf("%s.key must not be empty", field)
		}
		if !validAttributeOps[match.Op] {
			return fmt.Errorf("%s.op %q must be one of Equals, NotEquals, Contains, NotContains, Regex, Exists, NotExists, Gte, Lte, Gt, Lt", field, match.Op)
		}
		switch match.Op {
		case attributeOpRegex:
			if match.Value == "" {
				return fmt.Errorf("%s.value must not be empty for Regex", field)
			}
			if _, err := regexp.Compile(match.Value); err != nil {
				return fmt.Errorf("%s.value must be a valid RE2 regex: %w", field, err)
			}
		case attributeOpGte, attributeOpLte, attributeOpGt, attributeOpLt:
			if match.Value == "" {
				return fmt.Errorf("%s.value must not be empty for %s", field, match.Op)
			}
			if _, err := strconv.ParseFloat(match.Value, 64); err != nil {
				return fmt.Errorf("%s.value %q must be numeric for %s", field, match.Value, match.Op)
			}
		}
	}
	return nil
}
