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

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/rulengine"
)

// RCACorrelationRuleReconciler watches RCACorrelationRule CRDs and reloads the
// CRD rule engine whenever rules are created, updated, or deleted.
type RCACorrelationRuleReconciler struct {
	Client  client.Client
	Factory *rulengine.Factory
	Log     logr.Logger
}

// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcacorrelationrules,verbs=get;list;watch
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcacorrelationrules/status,verbs=get

func (r *RCACorrelationRuleReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithName("rcacorrelationrule-reconciler")

	if r.Factory == nil || r.Factory.Engine == nil {
		log.V(1).Info("CRD rule engine not yet initialized, skipping reload")
		return ctrl.Result{}, nil
	}

	if err := r.Factory.Engine.LoadRules(ctx); err != nil {
		log.Error(err, "Failed to reload correlation rules")
		return ctrl.Result{}, err
	}

	log.Info("Reloaded correlation rules", "count", r.Factory.Engine.RuleCount())
	return ctrl.Result{}, nil
}

// SetupWithManager registers the RCACorrelationRule controller with the manager.
func (r *RCACorrelationRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.RCACorrelationRule{}).
		Named("rcacorrelationrule").
		Complete(r)
}
