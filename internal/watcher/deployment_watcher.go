package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// defaultDeploymentScanInterval is how often the periodic fallback scanner runs.
	// It catches stalls that arrive when no informer update is delivered (e.g. on
	// quiet clusters where the API server coalesces updates).
	defaultDeploymentScanInterval = 30 * time.Second

	// reasonProgressDeadlineExceeded is the Kubernetes-defined reason string set
	// on the Progressing condition when a rollout misses its progress deadline.
	reasonProgressDeadlineExceeded = "ProgressDeadlineExceeded"
)

// DeploymentWatcherConfig controls the behaviour of the deployment rollout watcher.
type DeploymentWatcherConfig struct {
	// AgentName is stamped on every emitted event for correlator routing.
	AgentName string

	// WatchNamespaces restricts observation to these namespaces.
	// An empty slice means watch all namespaces.
	WatchNamespaces []string

	// ScanInterval controls how often the periodic fallback scan runs.
	// Defaults to defaultDeploymentScanInterval.
	ScanInterval time.Duration
}

// DeploymentWatcher monitors apps/v1 Deployments and emits a StalledRolloutEvent
// when a rollout fails to make forward progress within its configured
// progressDeadlineSeconds window (the kubelet sets Progressing=False with
// Reason=ProgressDeadlineExceeded on the Deployment's status conditions).
//
// At most one StalledRolloutEvent is emitted per (deployment, observedGeneration)
// pair. When the deployment either completes its rollout or starts a new one,
// the in-memory gate is cleared so a subsequent stall can fire again.
//
// It is intentionally read-only: it never writes to the Kubernetes API.
type DeploymentWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  DeploymentWatcherConfig
	clock   func() time.Time

	mu sync.Mutex
	// stalledAlerted maps deployment UID → the observedGeneration for which a
	// StalledRolloutEvent was already fired.  A new rollout (higher generation)
	// resets this gate and allows a fresh event if that rollout also stalls.
	stalledAlerted map[types.UID]int64
	namespaceSet   map[string]struct{}
}

// NewDeploymentWatcher creates a DeploymentWatcher backed by a controller-runtime cache.
// Config fields are defaulted when zero.
func NewDeploymentWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg DeploymentWatcherConfig) *DeploymentWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultDeploymentScanInterval
	}

	return &DeploymentWatcher{
		cache:          cache,
		emitter:        emitter,
		log:            logger.WithName("deployment-watcher"),
		config:         cfg,
		clock:          time.Now,
		stalledAlerted: make(map[types.UID]int64),
		namespaceSet:   toNamespaceSet(cfg.WatchNamespaces),
	}
}

// Start registers informer handlers, runs a bootstrap scan for pre-existing stalled
// rollouts, and launches the periodic fallback scanner. It is non-blocking;
// all goroutines are bounded by ctx.
func (w *DeploymentWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &appsv1.Deployment{})
	if err != nil {
		return fmt.Errorf("failed to get deployment informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			dep, ok := toDeployment(obj)
			if !ok {
				return
			}
			w.onDeploymentAdd(dep)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldDep, oldOK := toDeployment(oldObj)
			newDep, newOK := toDeployment(newObj)
			if !oldOK || !newOK {
				return
			}
			w.onDeploymentUpdate(oldDep, newDep)
		},
		// DeleteFunc is intentionally omitted: deleting a Deployment is not a
		// stall signal.  In-memory state is bounded and will be swept on the next
		// scan when the entry is no longer referenced.
	})
	if err != nil {
		return fmt.Errorf("failed to add deployment informer handler: %w", err)
	}

	// Bootstrap scan: walk all cached deployments once after cache sync so that
	// stalls that existed before the operator started are not missed across a
	// restart.
	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("Deployment watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanDeployments, w.config.ScanInterval)

	w.log.Info("Started deployment watcher",
		"scanInterval", w.config.ScanInterval.String(),
	)
	return nil
}

// onDeploymentAdd handles a newly observed Deployment (informer Add event).
func (w *DeploymentWatcher) onDeploymentAdd(dep *appsv1.Deployment) {
	if !w.shouldWatchNamespace(dep.Namespace) {
		return
	}
	w.detectStalledRollout(nil, dep)
}

// onDeploymentUpdate handles an updated Deployment (informer Update event).
func (w *DeploymentWatcher) onDeploymentUpdate(oldDep, newDep *appsv1.Deployment) {
	if !w.shouldWatchNamespace(newDep.Namespace) {
		return
	}
	w.detectStalledRollout(oldDep, newDep)
}

// detectStalledRollout inspects the Deployment's Progressing condition and emits
// a StalledRolloutEvent when the rollout has exceeded its progress deadline.
//
// Emission is gated per (UID, observedGeneration) so that:
//   - Repeated informer updates for the same stall do not spam the channel.
//   - A new rollout attempt (higher generation) that also stalls produces a
//     fresh event.
//
// When the rollout recovers (Progressing → True) the gate is cleared.  The
// oldDep parameter is accepted for future use (e.g. verifying a state
// transition) but is not required; pass nil for bootstrap/scan invocations.
func (w *DeploymentWatcher) detectStalledRollout(_ *appsv1.Deployment, newDep *appsv1.Deployment) {
	cond, ok := deploymentProgressingCondition(newDep)
	if !ok {
		// No Progressing condition yet — rollout has not been observed by the
		// controller manager.  Nothing to evaluate.
		return
	}

	// Recovery: the Progressing condition flipped back to True (rollout
	// completed or made progress).  Clear the gate so the next stall on any
	// future generation can fire a fresh event.
	if cond.Status == corev1.ConditionTrue {
		w.clearStalledAlerted(newDep.UID)
		return
	}

	// A stall is indicated by Status=False AND Reason=ProgressDeadlineExceeded.
	// Other Status=False reasons (e.g. "ReplicaSetUpdated") represent transient
	// states during a rollout and should not trigger an incident.
	if cond.Status != corev1.ConditionFalse || cond.Reason != reasonProgressDeadlineExceeded {
		return
	}

	generation := newDep.Status.ObservedGeneration

	// Gate: emit at most once per (UID, generation).
	if !w.markStalledAlerted(newDep.UID, generation) {
		return
	}

	// Use the condition's LastTransitionTime as the event timestamp so the
	// timeline is anchored to when Kubernetes first flagged the stall, not to
	// when the operator processed the update.
	at := cond.LastTransitionTime.Time
	if at.IsZero() {
		at = w.clock()
	}

	w.emitter.Emit(StalledRolloutEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: newDep.Namespace,
			// PodName carries the deployment name so the correlator can use the
			// standard resource-key routing path without a separate code path.
			PodName: newDep.Name,
		},
		DeploymentName:  newDep.Name,
		Revision:        generation,
		DesiredReplicas: deploymentDesiredReplicas(newDep),
		ReadyReplicas:   newDep.Status.ReadyReplicas,
		Reason:          reasonProgressDeadlineExceeded,
		Message:         cond.Message,
	})
}

// bootstrapScan walks all cached Deployments once after cache sync and calls
// detectStalledRollout so that pre-existing stalls are reported immediately on
// operator startup.
func (w *DeploymentWatcher) bootstrapScan(ctx context.Context) {
	depList := &appsv1.DeploymentList{}
	if err := w.cache.List(ctx, depList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list deployments for bootstrap scan")
		return
	}

	scanned := 0
	for i := range depList.Items {
		dep := &depList.Items[i]
		if !w.shouldWatchNamespace(dep.Namespace) {
			continue
		}
		w.detectStalledRollout(nil, dep)
		scanned++
	}

	w.log.Info("Deployment watcher bootstrap scan complete", "scanned", scanned)
}

// scanDeployments is the periodic fallback scan.  It ensures stalls are
// detected even when no informer update arrives (a scenario that can occur on
// low-traffic clusters where the API server coalesces status updates).
func (w *DeploymentWatcher) scanDeployments(ctx context.Context) {
	depList := &appsv1.DeploymentList{}
	if err := w.cache.List(ctx, depList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list deployments during periodic scan")
		return
	}

	for i := range depList.Items {
		dep := &depList.Items[i]
		if !w.shouldWatchNamespace(dep.Namespace) {
			continue
		}
		w.detectStalledRollout(nil, dep)
	}
}

// markStalledAlerted records a StalledRolloutEvent emission for the given UID
// at the given generation.  Returns true if the record is new (caller should
// emit), false if already recorded for this exact generation.
func (w *DeploymentWatcher) markStalledAlerted(uid types.UID, generation int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.stalledAlerted[uid]; ok && existing == generation {
		return false
	}
	w.stalledAlerted[uid] = generation
	return true
}

// clearStalledAlerted removes the dedup record for the given UID so that the
// next stall (on any future generation) can emit a fresh event.
func (w *DeploymentWatcher) clearStalledAlerted(uid types.UID) {
	w.mu.Lock()
	delete(w.stalledAlerted, uid)
	w.mu.Unlock()
}

func (w *DeploymentWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

// ── helpers ───────────────────────────────────────────────────────────────────

// toDeployment safely casts an informer object to *appsv1.Deployment, handling
// the tombstone wrapper that controller-runtime uses for deleted objects.
func toDeployment(obj any) (*appsv1.Deployment, bool) {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		dep, ok := t.Obj.(*appsv1.Deployment)
		return dep, ok
	default:
		return nil, false
	}
}

// deploymentProgressingCondition returns the Progressing condition from the
// Deployment's status, along with a boolean indicating whether it was found.
func deploymentProgressingCondition(dep *appsv1.Deployment) (appsv1.DeploymentCondition, bool) {
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing {
			return cond, true
		}
	}
	return appsv1.DeploymentCondition{}, false
}

// deploymentDesiredReplicas returns the configured replica count, defaulting to
// 1 when spec.replicas is nil (which is Kubernetes' own default).
func deploymentDesiredReplicas(dep *appsv1.Deployment) int32 {
	if dep.Spec.Replicas != nil {
		return *dep.Spec.Replicas
	}
	return 1
}
