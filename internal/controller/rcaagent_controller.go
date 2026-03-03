/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

const rcaAgentFinalizer = "rca.rca-operator.io/finalizer"

// Condition type constants — used in status.conditions
const (
	ConditionTypeAvailable   = "Available"
	ConditionTypeDegraded    = "Degraded"
	ConditionTypeProgressing = "Progressing"
)

type RCAAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

func (r *RCAAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// ── 1. FETCH ──────────────────────────────────────────────────────────────
	// Always re-fetch before doing anything. Never use a cached copy.
	agent := &rcav1alpha1.RCAAgent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if errors.IsNotFound(err) {
			// CR was deleted before we could reconcile — nothing to do
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch RCAAgent: %w", err)
	}

	log.Info("Reconciling RCAAgent",
		"name", agent.Name,
		"namespace", agent.Namespace,
		"status", agent.Status,
		"watchNamespaces", agent.Spec.WatchNamespaces,
	)

	// ── 2. DELETION / FINALIZER ───────────────────────────────────────────────
	// If the CR is being deleted, run cleanup then remove the finalizer.
	if !agent.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(agent, rcaAgentFinalizer) {
			log.Info("Running cleanup for deleted RCAAgent", "name", agent.Name)

			// Phase 1: nothing external to clean up yet.
			// Phase 2+: stop watchers, cancel goroutines, etc.

			controllerutil.RemoveFinalizer(agent, rcaAgentFinalizer)
			if err := r.Update(ctx, agent); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// ── 3. ENSURE FINALIZER ───────────────────────────────────────────────────
	// Add the finalizer on first reconcile so we can do cleanup on delete.
	if !controllerutil.ContainsFinalizer(agent, rcaAgentFinalizer) {
		controllerutil.AddFinalizer(agent, rcaAgentFinalizer)
		if err := r.Update(ctx, agent); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		// Re-queue immediately after the Update so we reconcile the new state
		return ctrl.Result{Requeue: true}, nil
	}

	// ── 4. VALIDATE SPEC ──────────────────────────────────────────────────────
	// Validate that the referenced secret actually exists.
	if err := r.validateSecret(ctx, agent); err != nil {
		log.Error(err, "Secret validation failed", "secretRef", agent.Spec.AIProviderConfig.SecretRef)

		msg := fmt.Sprintf("Secret %q not found in namespace %q", agent.Spec.AIProviderConfig.SecretRef, agent.Namespace)

		// Mark Available=False so the STATUS column reflects the problem
		if statusErr := r.setCondition(ctx, agent, ConditionTypeAvailable, metav1.ConditionFalse,
			"SecretNotFound", msg,
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}

		// Mark Degraded=True with the reason
		if statusErr := r.setCondition(ctx, agent, ConditionTypeDegraded, metav1.ConditionTrue,
			"SecretNotFound", msg,
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}

		// Don't requeue automatically — controller will re-trigger when the Secret is (re)created
		return ctrl.Result{}, nil
	}

	// Validate that watchNamespaces exist (warn only — don't block)
	r.validateNamespaces(ctx, agent)

	// ── 5. UPDATE STATUS — AVAILABLE ─────────────────────────────────────────
	if err := r.setCondition(ctx, agent, ConditionTypeAvailable, metav1.ConditionTrue,
		"AgentReady",
		fmt.Sprintf("RCAAgent is configured and watching %d namespace(s)", len(agent.Spec.WatchNamespaces)),
	); err != nil {
		return ctrl.Result{}, err
	}

	// Clear Degraded if it was previously set
	if err := r.setCondition(ctx, agent, ConditionTypeDegraded, metav1.ConditionFalse,
		"AgentHealthy",
		"All validations passed",
	); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("RCAAgent reconciled successfully", "name", agent.Name)
	return ctrl.Result{}, nil
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

// validateSecret checks that the Secret named in spec.aiProviderConfig.secretRef
// exists in the same namespace as the RCAAgent.
func (r *RCAAgentReconciler) validateSecret(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      agent.Spec.AIProviderConfig.SecretRef,
		Namespace: agent.Namespace,
	}
	if err := r.Get(ctx, key, secret); err != nil {
		return fmt.Errorf("secret %q not found: %w", key.Name, err)
	}
	return nil
}

// validateNamespaces logs a warning for any watchNamespace that doesn't exist.
// In Phase 1 this is a warning only — we don't block reconciliation.
func (r *RCAAgentReconciler) validateNamespaces(ctx context.Context, agent *rcav1alpha1.RCAAgent) {
	log := logf.FromContext(ctx)
	for _, ns := range agent.Spec.WatchNamespaces {
		namespace := &corev1.Namespace{}
		if err := r.Get(ctx, types.NamespacedName{Name: ns}, namespace); err != nil {
			log.Info("Watched namespace does not exist yet (will watch when created)",
				"namespace", ns)
		}
	}
}

// setCondition patches status.conditions on the RCAAgent.
// It uses patch (not update) to avoid conflicts with other reconcilers.
func (r *RCAAgentReconciler) setCondition(
	ctx context.Context,
	agent *rcav1alpha1.RCAAgent,
	conditionType string,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	// Re-fetch to get the latest resourceVersion before patching status
	current := &rcav1alpha1.RCAAgent{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, current); err != nil {
		return fmt.Errorf("failed to re-fetch RCAAgent before status patch: %w", err)
	}

	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: current.Generation,
	})

	if err := r.Status().Patch(ctx, current, client.MergeFrom(agent)); err != nil {
		return fmt.Errorf("failed to patch status condition %q: %w", conditionType, err)
	}
	return nil
}

// findRCAAgentsForSecret maps a Secret event to the RCAAgents that reference it,
// so that deleting/updating a Secret immediately triggers reconciliation.
func (r *RCAAgentReconciler) findRCAAgentsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	agentList := &rcav1alpha1.RCAAgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, "Failed to list RCAAgents while mapping Secret event")
		return nil
	}

	var requests []reconcile.Request
	for _, agent := range agentList.Items {
		if agent.Spec.AIProviderConfig != nil && agent.Spec.AIProviderConfig.SecretRef == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agent.Name,
					Namespace: agent.Namespace,
				},
			})
		}
	}
	return requests
}

func (r *RCAAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.RCAAgent{}).
		// Watch Secrets — when a Secret is created/updated/deleted, reconcile any
		// RCAAgent that references it via spec.aiProviderConfig.secretRef.
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findRCAAgentsForSecret),
		).
		Named("rcaagent").
		Complete(r)
}
