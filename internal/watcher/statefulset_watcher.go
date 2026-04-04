package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultStatefulSetScanInterval = 30 * time.Second
)

// StatefulSetWatcherConfig controls the behaviour of the StatefulSet rollout watcher.
type StatefulSetWatcherConfig struct {
	AgentName       string
	WatchNamespaces []string
	ScanInterval    time.Duration
}

// StatefulSetWatcher monitors apps/v1 StatefulSets and emits a StalledStatefulSetEvent
// when a rolling update stalls — detected when UpdateRevision != CurrentRevision and
// UpdatedReplicas < Replicas, indicating the rollout is not making forward progress.
//
// At most one event is emitted per (StatefulSet UID, observedGeneration) pair.
type StatefulSetWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  StatefulSetWatcherConfig
	clock   func() time.Time

	mu             sync.Mutex
	stalledAlerted map[types.UID]int64
	namespaceSet   map[string]struct{}
}

// NewStatefulSetWatcher creates a StatefulSetWatcher backed by a controller-runtime cache.
func NewStatefulSetWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg StatefulSetWatcherConfig) *StatefulSetWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultStatefulSetScanInterval
	}

	return &StatefulSetWatcher{
		cache:          cache,
		emitter:        emitter,
		log:            logger.WithName("statefulset-watcher"),
		config:         cfg,
		clock:          time.Now,
		stalledAlerted: make(map[types.UID]int64),
		namespaceSet:   toNamespaceSet(cfg.WatchNamespaces),
	}
}

// Start registers informer handlers, runs a bootstrap scan, and launches the
// periodic fallback scanner. Non-blocking; all goroutines are bounded by ctx.
func (w *StatefulSetWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &appsv1.StatefulSet{})
	if err != nil {
		return fmt.Errorf("failed to get statefulset informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			sts, ok := toStatefulSet(obj)
			if !ok {
				return
			}
			w.onAdd(sts)
		},
		UpdateFunc: func(_, newObj any) {
			sts, ok := toStatefulSet(newObj)
			if !ok {
				return
			}
			w.onUpdate(sts)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add statefulset informer handler: %w", err)
	}

	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("StatefulSet watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanStatefulSets, w.config.ScanInterval)

	w.log.Info("Started statefulset watcher", "scanInterval", w.config.ScanInterval.String())
	return nil
}

func (w *StatefulSetWatcher) onAdd(sts *appsv1.StatefulSet) {
	if !w.shouldWatchNamespace(sts.Namespace) {
		return
	}
	w.detectStalled(sts)
}

func (w *StatefulSetWatcher) onUpdate(sts *appsv1.StatefulSet) {
	if !w.shouldWatchNamespace(sts.Namespace) {
		return
	}
	w.detectStalled(sts)
}

// detectStalled checks whether a StatefulSet rollout is stalled.
// A stall is detected when:
//   - UpdateRevision != CurrentRevision (rollout in progress)
//   - UpdatedReplicas < desired replicas (not all pods updated)
//   - ObservedGeneration matches the spec generation (controller has processed the change)
func (w *StatefulSetWatcher) detectStalled(sts *appsv1.StatefulSet) {
	// If revisions match, rollout is complete — clear the gate.
	if sts.Status.UpdateRevision == sts.Status.CurrentRevision {
		w.clearStalledAlerted(sts.UID)
		return
	}

	// Rollout in progress: UpdateRevision != CurrentRevision.
	// Check if it's making progress by looking at UpdatedReplicas.
	desired := statefulSetDesiredReplicas(sts)
	if sts.Status.UpdatedReplicas >= desired {
		// All pods are updated, just waiting for them to become ready.
		return
	}

	generation := sts.Status.ObservedGeneration
	if !w.markStalledAlerted(sts.UID, generation) {
		return
	}

	at := w.clock()

	w.emitter.Emit(StalledStatefulSetEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: sts.Namespace,
			PodName:   sts.Name,
		},
		StatefulSetName: sts.Name,
		Revision:        generation,
		DesiredReplicas: desired,
		ReadyReplicas:   sts.Status.ReadyReplicas,
		UpdatedReplicas: sts.Status.UpdatedReplicas,
		Reason:          "RolloutStalled",
		Message:         fmt.Sprintf("StatefulSet %s/%s rollout stalled: %d/%d updated, %d ready", sts.Namespace, sts.Name, sts.Status.UpdatedReplicas, desired, sts.Status.ReadyReplicas),
	})
}

func (w *StatefulSetWatcher) bootstrapScan(ctx context.Context) {
	list := &appsv1.StatefulSetList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list statefulsets for bootstrap scan")
		return
	}

	scanned := 0
	for i := range list.Items {
		sts := &list.Items[i]
		if !w.shouldWatchNamespace(sts.Namespace) {
			continue
		}
		w.detectStalled(sts)
		scanned++
	}
	w.log.Info("StatefulSet watcher bootstrap scan complete", "scanned", scanned)
}

func (w *StatefulSetWatcher) scanStatefulSets(ctx context.Context) {
	list := &appsv1.StatefulSetList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list statefulsets during periodic scan")
		return
	}

	for i := range list.Items {
		sts := &list.Items[i]
		if !w.shouldWatchNamespace(sts.Namespace) {
			continue
		}
		w.detectStalled(sts)
	}
}

func (w *StatefulSetWatcher) markStalledAlerted(uid types.UID, generation int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.stalledAlerted[uid]; ok && existing == generation {
		return false
	}
	w.stalledAlerted[uid] = generation
	return true
}

func (w *StatefulSetWatcher) clearStalledAlerted(uid types.UID) {
	w.mu.Lock()
	delete(w.stalledAlerted, uid)
	w.mu.Unlock()
}

func (w *StatefulSetWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

func toStatefulSet(obj any) (*appsv1.StatefulSet, bool) {
	switch t := obj.(type) {
	case *appsv1.StatefulSet:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		sts, ok := t.Obj.(*appsv1.StatefulSet)
		return sts, ok
	default:
		return nil, false
	}
}

func statefulSetDesiredReplicas(sts *appsv1.StatefulSet) int32 {
	if sts.Spec.Replicas != nil {
		return *sts.Spec.Replicas
	}
	return 1
}
