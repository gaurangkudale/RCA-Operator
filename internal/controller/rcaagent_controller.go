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
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/collectors"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/retention"
)

const rcaAgentFinalizer = "rca.rca-operator.tech/finalizer"

const (
	incidentAgentLabelKey  = "rca.rca-operator.tech/agent"
	retentionRequeuePeriod = time.Minute
	phaseDetecting         = "Detecting"
	phaseActive            = "Active"
	phaseResolved          = "Resolved"

	// annotationLastSeen mirrors the key written by consumer.go so the controller
	// can read the timestamp during retention and lifecycle cleanup paths.
	annotationLastSeen = "rca.rca-operator.tech/last-seen"
)

// Condition type constants — used in status.conditions
const (
	ConditionTypeAvailable   = "Available"
	ConditionTypeDegraded    = "Degraded"
	ConditionTypeProgressing = "Progressing"
)

type RCAAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Cache                   ctrlcache.Cache
	SignalEmitter           collectors.SignalEmitter
	ManagerContext          context.Context
	newPodCollector         func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.PodCollectorConfig) podCollector
	newEventCollector       func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.EventCollectorConfig) eventCollector
	newWorkloadCollector    func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.WorkloadCollectorConfig) workloadCollector
	newNodeCollector        func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.NodeCollectorConfig) nodeCollector
	newStatefulSetCollector func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.StatefulSetCollectorConfig) statefulSetCollector
	newDaemonSetCollector   func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.DaemonSetCollectorConfig) daemonSetCollector
	newJobCollector         func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.JobCollectorConfig) jobCollector
	newCronJobCollector     func(ctrlcache.Cache, collectors.SignalEmitter, logr.Logger, collectors.CronJobCollectorConfig) cronJobCollector
	collectorRegistry       map[types.NamespacedName]collectorEntry
	collectorRegistryM      sync.Mutex
	nowFn                   func() time.Time
}

type podCollector interface {
	Start(ctx context.Context) error
}

type eventCollector interface {
	Start(ctx context.Context) error
}

type workloadCollector interface {
	Start(ctx context.Context) error
}

type nodeCollector interface {
	Start(ctx context.Context) error
}

type statefulSetCollector interface {
	Start(ctx context.Context) error
}

type daemonSetCollector interface {
	Start(ctx context.Context) error
}

type jobCollector interface {
	Start(ctx context.Context) error
}

type cronJobCollector interface {
	Start(ctx context.Context) error
}

type collectorEntry struct {
	cancel          context.CancelFunc
	watchNamespaces []string
}

// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcaagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcaagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rca.rca-operator.tech,resources=rcaagents/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="apps",resources=statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups="apps",resources=daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="batch",resources=cronjobs,verbs=get;list;watch

<<<<<<< HEAD
// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the RCAAgent object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
=======
>>>>>>> tmp-original-07-04-26-02-30
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
			r.stopCollectors(req.NamespacedName)

			// Stop all collector pipelines before removing the finalizer.

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
	// Validate that notification secrets actually exist.
	if err := r.validateReferencedSecrets(ctx, agent); err != nil {
		log.Error(err, "Secret validation failed")

		msg := err.Error()

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

	if err := r.ensureCollectorsRunning(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.resolveOrphanedIncidents(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.cleanupResolvedIncidents(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}

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
	return ctrl.Result{RequeueAfter: retentionRequeuePeriod}, nil
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

// validateReferencedSecrets checks that notification secrets referenced by the
// RCAAgent exist in the same namespace as the RCAAgent.
func (r *RCAAgentReconciler) validateReferencedSecrets(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	for _, ref := range referencedSecretRefs(agent) {
		secret := &corev1.Secret{}
		key := types.NamespacedName{
			Name:      ref.name,
			Namespace: agent.Namespace,
		}
		if err := r.Get(ctx, key, secret); err != nil {
			return fmt.Errorf("%s secret %q not found in namespace %q: %w", ref.usage, ref.name, agent.Namespace, err)
		}
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

	// Snapshot the just-fetched object BEFORE mutation — this is the patch base.
	// Using the original `agent` (stale resourceVersion) as the base would cause
	// "object has been modified" conflicts when setCondition is called more than
	// once in a single reconcile loop.
	base := current.DeepCopy()

	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: current.Generation,
	})

	if err := r.Status().Patch(ctx, current, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("failed to patch status condition %q: %w", conditionType, err)
	}

	// Propagate the updated resourceVersion back to the caller so the next
	// setCondition call in this reconcile loop starts from the latest version.
	*agent = *current
	return nil
}

// findRCAAgentsForSecret maps a Secret event to the RCAAgents that reference it,
// so deleting or updating a notification Secret immediately triggers reconciliation.
func (r *RCAAgentReconciler) findRCAAgentsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	agentList := &rcav1alpha1.RCAAgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, "Failed to list RCAAgents while mapping Secret event")
		return nil
	}

	var requests []reconcile.Request
	for _, agent := range agentList.Items {
		if referencesSecret(&agent, obj.GetName()) {
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
	r.initCollectorRegistry()
	if r.ManagerContext == nil {
		r.ManagerContext = context.Background()
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rcav1alpha1.RCAAgent{}).
		// Watch notification Secrets so the agent is re-validated immediately when
		// Slack or PagerDuty credentials change.
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findRCAAgentsForSecret),
		).
		Named("rcaagent").
		Complete(r)
}

func (r *RCAAgentReconciler) initCollectorRegistry() {
	r.collectorRegistryM.Lock()
	defer r.collectorRegistryM.Unlock()
	if r.collectorRegistry == nil {
		r.collectorRegistry = make(map[types.NamespacedName]collectorEntry)
	}
}

func (r *RCAAgentReconciler) ensureCollectorsRunning(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	if r.SignalEmitter == nil {
		return nil
	}

	key := types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}
	desiredNamespaces := normalizeNamespaces(agent.Spec.WatchNamespaces)

	podFactory := r.newPodCollector
	if podFactory == nil {
		if r.Cache == nil {
			return nil
		}
		podFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.PodCollectorConfig) podCollector {
			return collectors.NewPodCollector(cache, emitter, logger, cfg)
		}
	}
	eventFactory := r.newEventCollector
	if eventFactory == nil {
		eventFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.EventCollectorConfig) eventCollector {
			return collectors.NewEventCollector(cache, emitter, logger, cfg)
		}
	}

	r.collectorRegistryM.Lock()
	entry, exists := r.collectorRegistry[key]
	if exists && reflect.DeepEqual(entry.watchNamespaces, desiredNamespaces) {
		r.collectorRegistryM.Unlock()
		return nil
	}
	if exists {
		delete(r.collectorRegistry, key)
	}
	r.collectorRegistryM.Unlock()
	if exists {
		entry.cancel()
	}

	baseCtx := r.ManagerContext
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	collectorCtx, cancel := context.WithCancel(baseCtx)

	log := logf.FromContext(ctx)
	pc := podFactory(r.Cache, r.SignalEmitter, log,
		collectors.PodCollectorConfig{
			AgentName:       agent.Name,
			WatchNamespaces: desiredNamespaces,
		},
	)
	if err := pc.Start(collectorCtx); err != nil {
		cancel()
		return fmt.Errorf("failed to start pod collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
	}

	ec := eventFactory(r.Cache, r.SignalEmitter, log,
		collectors.EventCollectorConfig{
			AgentName:       agent.Name,
			WatchNamespaces: desiredNamespaces,
		},
	)
	if err := ec.Start(collectorCtx); err != nil {
		cancel()
		return fmt.Errorf("failed to start event collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
	}

	// Start optional collectors (workload types + node). Each is silently skipped
	// when neither an injected factory nor a real cache is available (unit-test paths).
	if err := r.startOptionalCollectors(collectorCtx, log, agent, desiredNamespaces); err != nil {
		cancel()
		return err
	}

	r.collectorRegistryM.Lock()
	defer r.collectorRegistryM.Unlock()
	r.collectorRegistry[key] = collectorEntry{cancel: cancel, watchNamespaces: desiredNamespaces}

	log.Info("Started collectors for RCAAgent",
		"name", agent.Name,
		"namespace", agent.Namespace,
		"watchNamespaces", desiredNamespaces,
	)

	return nil
}

// startOptionalCollectors starts workload and node collectors that are only
// available when a real cache is present (or an injected test factory).
// nolint:gocyclo
func (r *RCAAgentReconciler) startOptionalCollectors(
	ctx context.Context,
	log logr.Logger,
	agent *rcav1alpha1.RCAAgent,
	namespaces []string,
) error {
	// Workload collection (Deployment rollout stall detection).
	workloadFactory := r.newWorkloadCollector
	if workloadFactory == nil && r.Cache != nil {
		workloadFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.WorkloadCollectorConfig) workloadCollector {
			return collectors.NewWorkloadCollector(cache, emitter, logger, cfg)
		}
	}
	if workloadFactory != nil {
		wc := workloadFactory(r.Cache, r.SignalEmitter, log,
			collectors.WorkloadCollectorConfig{AgentName: agent.Name, WatchNamespaces: namespaces},
		)
		if err := wc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start workload collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// Node collection (NotReady/Pressure conditions).
	nodeFactory := r.newNodeCollector
	if nodeFactory == nil && r.Cache != nil {
		nodeFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.NodeCollectorConfig) nodeCollector {
			return collectors.NewNodeCollector(cache, emitter, logger, cfg)
		}
	}
	if nodeFactory != nil {
		nc := nodeFactory(r.Cache, r.SignalEmitter, log,
			collectors.NodeCollectorConfig{AgentName: agent.Name, IncidentNamespace: agent.Namespace},
		)
		if err := nc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start node collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// StatefulSet collection (stalled rollouts).
	stsFactory := r.newStatefulSetCollector
	if stsFactory == nil && r.Cache != nil {
		stsFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.StatefulSetCollectorConfig) statefulSetCollector {
			return collectors.NewStatefulSetCollector(cache, emitter, logger, cfg)
		}
	}
	if stsFactory != nil {
		sc := stsFactory(r.Cache, r.SignalEmitter, log,
			collectors.StatefulSetCollectorConfig{AgentName: agent.Name, WatchNamespaces: namespaces},
		)
		if err := sc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start statefulset collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// DaemonSet collection (stalled rollouts).
	dsFactory := r.newDaemonSetCollector
	if dsFactory == nil && r.Cache != nil {
		dsFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.DaemonSetCollectorConfig) daemonSetCollector {
			return collectors.NewDaemonSetCollector(cache, emitter, logger, cfg)
		}
	}
	if dsFactory != nil {
		dc := dsFactory(r.Cache, r.SignalEmitter, log,
			collectors.DaemonSetCollectorConfig{AgentName: agent.Name, WatchNamespaces: namespaces},
		)
		if err := dc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start daemonset collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// Job collection (failed Jobs).
	jobFactory := r.newJobCollector
	if jobFactory == nil && r.Cache != nil {
		jobFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.JobCollectorConfig) jobCollector {
			return collectors.NewJobCollector(cache, emitter, logger, cfg)
		}
	}
	if jobFactory != nil {
		jc := jobFactory(r.Cache, r.SignalEmitter, log,
			collectors.JobCollectorConfig{AgentName: agent.Name, WatchNamespaces: namespaces},
		)
		if err := jc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start job collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// CronJob collection (failed scheduled runs).
	cjFactory := r.newCronJobCollector
	if cjFactory == nil && r.Cache != nil {
		cjFactory = func(cache ctrlcache.Cache, emitter collectors.SignalEmitter, logger logr.Logger, cfg collectors.CronJobCollectorConfig) cronJobCollector {
			return collectors.NewCronJobCollector(cache, emitter, logger, cfg)
		}
	}
	if cjFactory != nil {
		cc := cjFactory(r.Cache, r.SignalEmitter, log,
			collectors.CronJobCollectorConfig{AgentName: agent.Name, WatchNamespaces: namespaces},
		)
		if err := cc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start cronjob collector for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	return nil
}

func (r *RCAAgentReconciler) stopCollectors(key types.NamespacedName) {
	r.collectorRegistryM.Lock()
	entry, ok := r.collectorRegistry[key]
	if ok {
		delete(r.collectorRegistry, key)
	}
	r.collectorRegistryM.Unlock()

	if ok {
		entry.cancel()
	}
}

func normalizeNamespaces(namespaces []string) []string {
	if len(namespaces) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(namespaces))
	out := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

type secretRefUsage struct {
	name  string
	usage string
}

func referencedSecretRefs(agent *rcav1alpha1.RCAAgent) []secretRefUsage {
	if agent == nil || agent.Spec.Notifications == nil {
		return nil
	}

	refs := make([]secretRefUsage, 0, 2)
	if slack := agent.Spec.Notifications.Slack; slack != nil && strings.TrimSpace(slack.WebhookSecretRef) != "" {
		refs = append(refs, secretRefUsage{
			name:  strings.TrimSpace(slack.WebhookSecretRef),
			usage: "Slack webhook",
		})
	}
	if pagerDuty := agent.Spec.Notifications.PagerDuty; pagerDuty != nil && strings.TrimSpace(pagerDuty.SecretRef) != "" {
		refs = append(refs, secretRefUsage{
			name:  strings.TrimSpace(pagerDuty.SecretRef),
			usage: "PagerDuty routing key",
		})
	}
	return refs
}

func referencesSecret(agent *rcav1alpha1.RCAAgent, secretName string) bool {
	for _, ref := range referencedSecretRefs(agent) {
		if ref.name == secretName {
			return true
		}
	}
	return false
}

func (r *RCAAgentReconciler) cleanupResolvedIncidents(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	retentionDuration, err := retention.ParseIncidentRetention(agent.Spec.IncidentRetention, agent.Spec.IncidentRetentionDays)
	if err != nil {
		return fmt.Errorf("invalid incident retention for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
	}

	namespaces, err := r.retentionNamespaces(ctx, agent)
	if err != nil {
		return err
	}

	now := r.now()
	deletedCount := 0
	for _, namespace := range namespaces {
		list := &rcav1alpha1.IncidentReportList{}
		if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("failed to list IncidentReports in namespace %q for retention cleanup: %w", namespace, err)
		}

		for i := range list.Items {
			report := &list.Items[i]
			if !belongsToAgent(report, agent.Name) {
				continue
			}
			if !shouldPruneIncidentReport(report, now, retentionDuration) {
				continue
			}

			if err := r.Delete(ctx, report); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to delete IncidentReport %s/%s during retention cleanup: %w", report.Namespace, report.Name, err)
			}
			deletedCount++
		}
	}

	if deletedCount > 0 {
		logf.FromContext(ctx).Info("Deleted IncidentReports by retention policy",
			"agent", agent.Name,
			"deletedCount", deletedCount,
			"retention", retentionDuration.String(),
		)
	}

	return nil
}

func (r *RCAAgentReconciler) retentionNamespaces(ctx context.Context, agent *rcav1alpha1.RCAAgent) ([]string, error) {
	namespaces := normalizeNamespaces(agent.Spec.WatchNamespaces)
	if len(namespaces) > 0 {
		return namespaces, nil
	}

	list := &corev1.NamespaceList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("failed to list namespaces for incident retention cleanup: %w", err)
	}

	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	sort.Strings(out)
	return out, nil
}

func (r *RCAAgentReconciler) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return time.Now()
}

// resolveOrphanedIncidents marks Active IncidentReports as Resolved when their referenced pod
// no longer exists in the cluster. This acts as a safety-net for missed PodDeletedEvents
// (e.g. controller was down when the pod was deleted).
func (r *RCAAgentReconciler) resolveOrphanedIncidents(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	namespaces, err := r.retentionNamespaces(ctx, agent)
	if err != nil {
		return err
	}

	now := metav1.NewTime(r.now())
	resolvedCount := 0
	for _, namespace := range namespaces {
		list := &rcav1alpha1.IncidentReportList{}
		if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("failed to list IncidentReports for orphan check in namespace %q: %w", namespace, err)
		}

		for i := range list.Items {
			report := &list.Items[i]
			if report.Status.Phase == phaseResolved {
				continue
			}
			if !belongsToAgent(report, agent.Name) {
				continue
			}

			// Check whether all referenced pods are gone.
			podGone := false
			for _, res := range report.Status.AffectedResources {
				if res.Kind != resourceKindPod {
					continue
				}
				pod := &corev1.Pod{}
				getErr := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: res.Name}, pod)
				if errors.IsNotFound(getErr) {
					podGone = true
					break
				}
				if getErr != nil {
					logf.FromContext(ctx).Error(getErr, "Could not check pod existence for orphaned incident",
						"incident", report.Name, "pod", res.Name)
				}
			}
			if !podGone {
				continue
			}

			base := report.DeepCopy()
			incidentstatus.MarkResolved(report, now, "Pod no longer exists in cluster; incident auto-resolved")

			if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to resolve orphaned IncidentReport %s/%s: %w", report.Namespace, report.Name, err)
			}
			resolvedCount++
		}
	}

	if resolvedCount > 0 {
		logf.FromContext(ctx).Info("Resolved orphaned IncidentReports for deleted pods",
			"agent", agent.Name,
			"resolvedCount", resolvedCount,
		)
	}

	return nil
}

func belongsToAgent(report *rcav1alpha1.IncidentReport, agentName string) bool {
	if report.Spec.AgentRef == agentName {
		return true
	}
	if report.Labels == nil {
		return false
	}
	return report.Labels[incidentAgentLabelKey] == agentName
}

func shouldPruneIncidentReport(report *rcav1alpha1.IncidentReport, now time.Time, retentionDuration time.Duration) bool {
	// Prune Resolved incidents older than the retention window.
	if report.Status.Phase == phaseResolved {
		resolvedAt := incidentstatus.EffectiveResolvedTime(report.Status)
		if resolvedAt == nil || resolvedAt.IsZero() {
			return false
		}
		return now.Sub(resolvedAt.Time) > retentionDuration
	}

	// Prune uninitialized incidents (status.phase == "") — these are zombie CRs
	// where the Create succeeded but the subsequent Status().Patch failed (e.g.
	// before a CRD enum was updated). Fall back to creationTimestamp age so they
	// are cleaned up within one retention period even though they were never
	// properly initialized.
	if report.Status.Phase == "" {
		return now.Sub(report.CreationTimestamp.Time) > retentionDuration
	}

	return false
}
