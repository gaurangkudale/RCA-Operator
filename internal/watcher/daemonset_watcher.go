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
	defaultDaemonSetScanInterval = 30 * time.Second
)

// DaemonSetWatcherConfig controls the behaviour of the DaemonSet watcher.
type DaemonSetWatcherConfig struct {
	AgentName       string
	WatchNamespaces []string
	ScanInterval    time.Duration
}

// DaemonSetWatcher monitors apps/v1 DaemonSets and emits a StalledDaemonSetEvent
// when the number of ready pods is fewer than desired, indicating a stalled rollout
// or scheduling failure.
//
// At most one event is emitted per (DaemonSet UID, observedGeneration) pair.
type DaemonSetWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  DaemonSetWatcherConfig
	clock   func() time.Time

	mu             sync.Mutex
	stalledAlerted map[types.UID]int64
	namespaceSet   map[string]struct{}
}

// NewDaemonSetWatcher creates a DaemonSetWatcher backed by a controller-runtime cache.
func NewDaemonSetWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg DaemonSetWatcherConfig) *DaemonSetWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultDaemonSetScanInterval
	}

	return &DaemonSetWatcher{
		cache:          cache,
		emitter:        emitter,
		log:            logger.WithName("daemonset-watcher"),
		config:         cfg,
		clock:          time.Now,
		stalledAlerted: make(map[types.UID]int64),
		namespaceSet:   toNamespaceSet(cfg.WatchNamespaces),
	}
}

// Start registers informer handlers, runs a bootstrap scan, and launches the
// periodic fallback scanner. Non-blocking; all goroutines are bounded by ctx.
func (w *DaemonSetWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &appsv1.DaemonSet{})
	if err != nil {
		return fmt.Errorf("failed to get daemonset informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			ds, ok := toDaemonSet(obj)
			if !ok {
				return
			}
			w.onAdd(ds)
		},
		UpdateFunc: func(_, newObj any) {
			ds, ok := toDaemonSet(newObj)
			if !ok {
				return
			}
			w.onUpdate(ds)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add daemonset informer handler: %w", err)
	}

	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("DaemonSet watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanDaemonSets, w.config.ScanInterval)

	w.log.Info("Started daemonset watcher", "scanInterval", w.config.ScanInterval.String())
	return nil
}

func (w *DaemonSetWatcher) onAdd(ds *appsv1.DaemonSet) {
	if !w.shouldWatchNamespace(ds.Namespace) {
		return
	}
	w.detectStalled(ds)
}

func (w *DaemonSetWatcher) onUpdate(ds *appsv1.DaemonSet) {
	if !w.shouldWatchNamespace(ds.Namespace) {
		return
	}
	w.detectStalled(ds)
}

// detectStalled checks whether a DaemonSet rollout is stalled.
// A stall is detected when:
//   - DesiredNumberScheduled > 0
//   - NumberReady < DesiredNumberScheduled (not all pods ready)
//   - UpdatedNumberScheduled < DesiredNumberScheduled (rollout incomplete)
func (w *DaemonSetWatcher) detectStalled(ds *appsv1.DaemonSet) {
	desired := ds.Status.DesiredNumberScheduled
	if desired == 0 {
		return
	}

	// If all pods are ready and updated, rollout is healthy — clear the gate.
	if ds.Status.NumberReady >= desired && ds.Status.UpdatedNumberScheduled >= desired {
		w.clearStalledAlerted(ds.UID)
		return
	}

	// Only fire when the rollout is not making progress: updated < desired.
	if ds.Status.UpdatedNumberScheduled >= desired {
		return
	}

	generation := ds.Status.ObservedGeneration
	if !w.markStalledAlerted(ds.UID, generation) {
		return
	}

	at := w.clock()

	w.emitter.Emit(StalledDaemonSetEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ds.Namespace,
			PodName:   ds.Name,
		},
		DaemonSetName:          ds.Name,
		Revision:               generation,
		DesiredNumberScheduled: desired,
		NumberReady:            ds.Status.NumberReady,
		UpdatedNumberScheduled: ds.Status.UpdatedNumberScheduled,
		Reason:                 "RolloutStalled",
		Message:                fmt.Sprintf("DaemonSet %s/%s rollout stalled: %d/%d updated, %d ready", ds.Namespace, ds.Name, ds.Status.UpdatedNumberScheduled, desired, ds.Status.NumberReady),
	})
}

func (w *DaemonSetWatcher) bootstrapScan(ctx context.Context) {
	list := &appsv1.DaemonSetList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list daemonsets for bootstrap scan")
		return
	}

	scanned := 0
	for i := range list.Items {
		ds := &list.Items[i]
		if !w.shouldWatchNamespace(ds.Namespace) {
			continue
		}
		w.detectStalled(ds)
		scanned++
	}
	w.log.Info("DaemonSet watcher bootstrap scan complete", "scanned", scanned)
}

func (w *DaemonSetWatcher) scanDaemonSets(ctx context.Context) {
	list := &appsv1.DaemonSetList{}
	if err := w.cache.List(ctx, list, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list daemonsets during periodic scan")
		return
	}

	for i := range list.Items {
		ds := &list.Items[i]
		if !w.shouldWatchNamespace(ds.Namespace) {
			continue
		}
		w.detectStalled(ds)
	}
}

func (w *DaemonSetWatcher) markStalledAlerted(uid types.UID, generation int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.stalledAlerted[uid]; ok && existing == generation {
		return false
	}
	w.stalledAlerted[uid] = generation
	return true
}

func (w *DaemonSetWatcher) clearStalledAlerted(uid types.UID) {
	w.mu.Lock()
	delete(w.stalledAlerted, uid)
	w.mu.Unlock()
}

func (w *DaemonSetWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

func toDaemonSet(obj any) (*appsv1.DaemonSet, bool) {
	switch t := obj.(type) {
	case *appsv1.DaemonSet:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		ds, ok := t.Obj.(*appsv1.DaemonSet)
		return ds, ok
	default:
		return nil, false
	}
}
