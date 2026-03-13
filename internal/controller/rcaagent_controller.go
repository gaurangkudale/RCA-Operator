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
	"github.com/gaurangkudale/rca-operator/internal/retention"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const rcaAgentFinalizer = "rca.rca-operator.io/finalizer"

const (
	incidentAgentLabelKey  = "rca.rca-operator.io/agent"
	retentionRequeuePeriod = time.Minute
	phaseActive            = "Active"
	phaseResolved          = "Resolved"

	// annotationLastSeen mirrors the key written by consumer.go so the controller
	// can read the timestamp and auto-resolve stale ResourceSaturation incidents.
	annotationLastSeen = "rca.rca-operator.io/last-seen"

	// defaultThrottlingTTL is how long a ResourceSaturation incident may remain
	// Active without receiving a new CPUThrottlingHigh signal before the controller
	// automatically marks it Resolved.
	defaultThrottlingTTL = 10 * time.Minute
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

	Cache            ctrlcache.Cache
	WatcherEmitter   watcher.EventEmitter
	ManagerContext   context.Context
	newPodWatcher    func(ctrlcache.Cache, watcher.EventEmitter, logr.Logger, watcher.PodWatcherConfig) podWatcher
	newEventWatcher  func(ctrlcache.Cache, watcher.EventEmitter, logr.Logger, watcher.EventWatcherConfig) eventWatcher
	newDeployWatcher func(ctrlcache.Cache, watcher.EventEmitter, logr.Logger, watcher.DeploymentWatcherConfig) deploymentWatcher
	newNodeWatcher   func(ctrlcache.Cache, watcher.EventEmitter, logr.Logger, watcher.NodeWatcherConfig) nodeWatcher
	watcherRegistry  map[types.NamespacedName]watcherEntry
	watcherRegistryM sync.Mutex
	nowFn            func() time.Time
}

type podWatcher interface {
	Start(ctx context.Context) error
}

type eventWatcher interface {
	Start(ctx context.Context) error
}

type deploymentWatcher interface {
	Start(ctx context.Context) error
}

type nodeWatcher interface {
	Start(ctx context.Context) error
}

type watcherEntry struct {
	cancel          context.CancelFunc
	watchNamespaces []string
}

// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rca.rca-operator.io,resources=rcaagents/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

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
			r.stopWatcher(req.NamespacedName)

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

	if err := r.ensureWatcherRunning(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.resolveOrphanedIncidents(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.resolveStaleThrottlingIncidents(ctx, agent); err != nil {
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
	r.initWatcherRegistry()
	if r.ManagerContext == nil {
		r.ManagerContext = context.Background()
	}

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

func (r *RCAAgentReconciler) initWatcherRegistry() {
	r.watcherRegistryM.Lock()
	defer r.watcherRegistryM.Unlock()
	if r.watcherRegistry == nil {
		r.watcherRegistry = make(map[types.NamespacedName]watcherEntry)
	}
}

func (r *RCAAgentReconciler) ensureWatcherRunning(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	if r.WatcherEmitter == nil {
		return nil
	}

	key := types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}
	desiredNamespaces := normalizeNamespaces(agent.Spec.WatchNamespaces)

	podFactory := r.newPodWatcher
	if podFactory == nil {
		if r.Cache == nil {
			return nil
		}
		podFactory = func(cache ctrlcache.Cache, emitter watcher.EventEmitter, logger logr.Logger, cfg watcher.PodWatcherConfig) podWatcher {
			return watcher.NewPodWatcher(cache, emitter, logger, cfg)
		}
	}
	eventFactory := r.newEventWatcher
	if eventFactory == nil {
		eventFactory = func(cache ctrlcache.Cache, emitter watcher.EventEmitter, logger logr.Logger, cfg watcher.EventWatcherConfig) eventWatcher {
			return watcher.NewEventWatcher(cache, emitter, logger, cfg)
		}
	}

	r.watcherRegistryM.Lock()
	entry, exists := r.watcherRegistry[key]
	if exists && reflect.DeepEqual(entry.watchNamespaces, desiredNamespaces) {
		r.watcherRegistryM.Unlock()
		return nil
	}
	if exists {
		delete(r.watcherRegistry, key)
	}
	r.watcherRegistryM.Unlock()
	if exists {
		entry.cancel()
	}

	baseCtx := r.ManagerContext
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	watcherCtx, cancel := context.WithCancel(baseCtx)

	log := logf.FromContext(ctx)
	pw := podFactory(r.Cache, r.WatcherEmitter, log,
		watcher.PodWatcherConfig{
			AgentName:       agent.Name,
			WatchNamespaces: desiredNamespaces,
		},
	)
	if err := pw.Start(watcherCtx); err != nil {
		cancel()
		return fmt.Errorf("failed to start pod watcher for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
	}

	ew := eventFactory(r.Cache, r.WatcherEmitter, log,
		watcher.EventWatcherConfig{
			AgentName:       agent.Name,
			WatchNamespaces: desiredNamespaces,
		},
	)
	if err := ew.Start(watcherCtx); err != nil {
		cancel()
		return fmt.Errorf("failed to start event watcher for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
	}

	// DeploymentWatcher is started when either an injected factory is provided
	// or a real cache is available.  When neither is true (e.g. unit tests that
	// inject pod/event fakes but have no cache) the watcher is simply skipped so
	// test compatibility is preserved without requiring changes to existing tests.
	deployFactory := r.newDeployWatcher
	if deployFactory == nil && r.Cache != nil {
		deployFactory = func(cache ctrlcache.Cache, emitter watcher.EventEmitter, logger logr.Logger, cfg watcher.DeploymentWatcherConfig) deploymentWatcher {
			return watcher.NewDeploymentWatcher(cache, emitter, logger, cfg)
		}
	}
	if deployFactory != nil {
		dw := deployFactory(r.Cache, r.WatcherEmitter, log,
			watcher.DeploymentWatcherConfig{
				AgentName:       agent.Name,
				WatchNamespaces: desiredNamespaces,
			},
		)
		if err := dw.Start(watcherCtx); err != nil {
			cancel()
			return fmt.Errorf("failed to start deployment watcher for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	// NodeWatcher monitors corev1.Node objects for NotReady/Pressure conditions.
	// Same graceful-skip pattern as DeploymentWatcher: silently skipped when
	// there is neither an injected factory nor a real cache (unit-test paths).
	nodeFactory := r.newNodeWatcher
	if nodeFactory == nil && r.Cache != nil {
		nodeFactory = func(cache ctrlcache.Cache, emitter watcher.EventEmitter, logger logr.Logger, cfg watcher.NodeWatcherConfig) nodeWatcher {
			return watcher.NewNodeWatcher(cache, emitter, logger, cfg)
		}
	}
	if nodeFactory != nil {
		nw := nodeFactory(r.Cache, r.WatcherEmitter, log,
			watcher.NodeWatcherConfig{
				AgentName:         agent.Name,
				IncidentNamespace: agent.Namespace,
			},
		)
		if err := nw.Start(watcherCtx); err != nil {
			cancel()
			return fmt.Errorf("failed to start node watcher for RCAAgent %s/%s: %w", agent.Namespace, agent.Name, err)
		}
	}

	r.watcherRegistryM.Lock()
	defer r.watcherRegistryM.Unlock()
	r.watcherRegistry[key] = watcherEntry{cancel: cancel, watchNamespaces: desiredNamespaces}

	log.Info("Started watchers for RCAAgent",
		"name", agent.Name,
		"namespace", agent.Namespace,
		"watchNamespaces", desiredNamespaces,
	)

	return nil
}

func (r *RCAAgentReconciler) stopWatcher(key types.NamespacedName) {
	r.watcherRegistryM.Lock()
	entry, ok := r.watcherRegistry[key]
	if ok {
		delete(r.watcherRegistry, key)
	}
	r.watcherRegistryM.Unlock()

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
			if report.Status.Phase != phaseActive {
				continue
			}
			if !belongsToAgent(report, agent.Name) {
				continue
			}

			// Check whether all referenced pods are gone.
			podGone := false
			for _, res := range report.Status.AffectedResources {
				if res.Kind != "Pod" {
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
			report.Status.Phase = phaseResolved
			report.Status.ResolvedTime = &now
			report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
				Time:  now,
				Event: "Pod no longer exists in cluster; incident auto-resolved",
			})
			if len(report.Status.Timeline) > 50 {
				report.Status.Timeline = report.Status.Timeline[len(report.Status.Timeline)-50:]
			}

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

// resolveStaleThrottlingIncidents auto-resolves Active ResourceSaturation incidents
// that have not received a new CPUThrottlingHigh signal within defaultThrottlingTTL.
// The last-signal time is read from the rca.rca-operator.io/last-seen annotation,
// which the correlator consumer keeps up-to-date on every signal update.
func (r *RCAAgentReconciler) resolveStaleThrottlingIncidents(ctx context.Context, agent *rcav1alpha1.RCAAgent) error {
	namespaces, err := r.retentionNamespaces(ctx, agent)
	if err != nil {
		return err
	}

	now := r.now()
	resolvedCount := 0
	for _, namespace := range namespaces {
		list := &rcav1alpha1.IncidentReportList{}
		if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("failed to list IncidentReports for throttling TTL check in %q: %w", namespace, err)
		}

		for i := range list.Items {
			report := &list.Items[i]
			if report.Status.Phase != phaseActive {
				continue
			}
			if report.Status.IncidentType != "ResourceSaturation" {
				continue
			}
			if !belongsToAgent(report, agent.Name) {
				continue
			}

			// Read the last-signal timestamp from annotations.
			lastSeenStr, ok := report.Annotations[annotationLastSeen]
			if !ok || lastSeenStr == "" {
				continue
			}
			lastSeen, parseErr := time.Parse(time.RFC3339, lastSeenStr)
			if parseErr != nil {
				continue
			}
			if now.Sub(lastSeen) < defaultThrottlingTTL {
				continue
			}

			// TTL exceeded — auto-resolve.
			nowMeta := metav1.NewTime(now)
			base := report.DeepCopy()
			report.Status.Phase = phaseResolved
			report.Status.ResolvedTime = &nowMeta
			report.Status.Timeline = append(report.Status.Timeline, rcav1alpha1.TimelineEvent{
				Time:  nowMeta,
				Event: fmt.Sprintf("No CPUThrottling signals for %.0f minutes; incident auto-resolved", defaultThrottlingTTL.Minutes()),
			})
			if len(report.Status.Timeline) > 50 {
				report.Status.Timeline = report.Status.Timeline[len(report.Status.Timeline)-50:]
			}

			if err := r.Status().Patch(ctx, report, client.MergeFrom(base)); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to resolve stale ResourceSaturation incident %s/%s: %w", report.Namespace, report.Name, err)
			}
			resolvedCount++
		}
	}

	if resolvedCount > 0 {
		logf.FromContext(ctx).Info("Auto-resolved stale ResourceSaturation incidents (TTL exceeded)",
			"agent", agent.Name,
			"resolvedCount", resolvedCount,
			"ttl", defaultThrottlingTTL.String(),
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
		if report.Status.ResolvedTime == nil || report.Status.ResolvedTime.IsZero() {
			return false
		}
		return now.Sub(report.Status.ResolvedTime.Time) > retentionDuration
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
