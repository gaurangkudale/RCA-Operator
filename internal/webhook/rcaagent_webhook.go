// Package webhook provides validating and defaulting admission webhooks
// for RCA Operator CRDs.
package webhook

import (
	"context"
	"fmt"
	"net"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// RCAAgentWebhook implements typed defaulting and validating webhooks for RCAAgent.
type RCAAgentWebhook struct{}

// SetupRCAAgentWebhookWithManager registers the webhook with the controller manager.
func SetupRCAAgentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &rcav1alpha1.RCAAgent{}).
		WithDefaulter(&RCAAgentWebhook{}).
		WithValidator(&RCAAgentWebhook{}).
		Complete()
}

// Default implements admission.Defaulter[*rcav1alpha1.RCAAgent].
func (w *RCAAgentWebhook) Default(_ context.Context, agent *rcav1alpha1.RCAAgent) error {
	if agent.Spec.IncidentRetention == "" {
		agent.Spec.IncidentRetention = "30d"
	}
	if agent.Spec.OTel != nil {
		if agent.Spec.OTel.ServiceName == "" {
			agent.Spec.OTel.ServiceName = "rca-operator"
		}
		if agent.Spec.OTel.SamplingRate == "" {
			agent.Spec.OTel.SamplingRate = "1.0"
		}
	}
	return nil
}

// ValidateCreate implements admission.Validator[*rcav1alpha1.RCAAgent].
func (w *RCAAgentWebhook) ValidateCreate(_ context.Context, agent *rcav1alpha1.RCAAgent) (admission.Warnings, error) {
	return validateAgent(agent)
}

// ValidateUpdate implements admission.Validator[*rcav1alpha1.RCAAgent].
func (w *RCAAgentWebhook) ValidateUpdate(_ context.Context, _, agent *rcav1alpha1.RCAAgent) (admission.Warnings, error) {
	return validateAgent(agent)
}

// ValidateDelete implements admission.Validator[*rcav1alpha1.RCAAgent].
func (w *RCAAgentWebhook) ValidateDelete(_ context.Context, _ *rcav1alpha1.RCAAgent) (admission.Warnings, error) {
	return nil, nil
}

func validateAgent(agent *rcav1alpha1.RCAAgent) (admission.Warnings, error) {
	if len(agent.Spec.WatchNamespaces) == 0 {
		return nil, fmt.Errorf("spec.watchNamespaces must not be empty")
	}

	if agent.Spec.OTel != nil && agent.Spec.OTel.Endpoint != "" {
		_, _, err := net.SplitHostPort(agent.Spec.OTel.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("spec.otel.endpoint must be a valid host:port, got %q: %w", agent.Spec.OTel.Endpoint, err)
		}
	}

	return nil, nil
}
