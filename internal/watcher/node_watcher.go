package watcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultNodeScanInterval = 30 * time.Second

// NodeWatcherConfig controls Node condition monitoring behaviour.
type NodeWatcherConfig struct {
	// AgentName is stamped on every emitted event for correlator routing.
	AgentName string

	// IncidentNamespace is the namespace where node-level IncidentReport CRs are
	// stored.  Since Nodes are cluster-scoped, a target namespace must be provided
	// explicitly; the agent's own namespace is the natural choice.
	// Defaults to "default" when empty.
	IncidentNamespace string

	// ScanInterval controls how often the periodic fallback scan runs.
	// Defaults to defaultNodeScanInterval.
	ScanInterval time.Duration
}

// NodeWatcher monitors corev1.Node objects and emits typed signals for:
//   - NotReady condition         → NodeNotReadyEvent
//   - DiskPressure condition     → NodePressureEvent{PressureType: "DiskPressure"}
//   - MemoryPressure condition   → NodePressureEvent{PressureType: "MemoryPressure"}
//   - PIDPressure condition      → NodePressureEvent{PressureType: "PIDPressure"}
//
// It supplements event_watcher.go, which captures node signals from the K8s Event
// stream.  Watching the Node object directly is more reliable because:
//   - Node conditions are always present on the Node status object.
//   - DiskPressure and MemoryPressure may not produce K8s Events in every cluster.
//   - K8s Events are rate-limited; conditions are authoritative and persistent.
//
// The correlator's dedup key (namespace+nodeName+conditionType) prevents duplicate
// incidents when both event_watcher and node_watcher observe the same condition.
//
// It is intentionally read-only: it never writes to the Kubernetes API.
type NodeWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  NodeWatcherConfig
	clock   func() time.Time

	mu sync.Mutex
	// alerted maps nodeUID:conditionType → true when an event has already been
	// emitted for that condition.  The entry is deleted when the condition clears
	// so the next activation fires a fresh event.
	alerted map[string]bool
}

// NewNodeWatcher creates a NodeWatcher backed by a controller-runtime cache.
// Config fields are defaulted when zero.
func NewNodeWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg NodeWatcherConfig) *NodeWatcher {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultNodeScanInterval
	}
	if cfg.IncidentNamespace == "" {
		cfg.IncidentNamespace = "default"
	}

	return &NodeWatcher{
		cache:   cache,
		emitter: emitter,
		log:     logger.WithName("node-watcher"),
		config:  cfg,
		clock:   time.Now,
		alerted: make(map[string]bool),
	}
}

// Start registers informer handlers, runs a bootstrap scan for pre-existing
// node conditions, and launches the periodic fallback scanner.
// Non-blocking; all goroutines are bounded by ctx.
func (w *NodeWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &corev1.Node{})
	if err != nil {
		return fmt.Errorf("failed to get node informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			node, ok := toNode(obj)
			if !ok {
				return
			}
			w.checkConditions(node)
		},
		UpdateFunc: func(_, newObj any) {
			node, ok := toNode(newObj)
			if !ok {
				return
			}
			w.checkConditions(node)
		},
		// DeleteFunc is intentionally omitted.  Node deletion is an exceptional
		// event; any active incidents will be resolved by the orphan-resolution
		// loop in the agent controller on the next reconcile cycle.
	})
	if err != nil {
		return fmt.Errorf("failed to add node informer handler: %w", err)
	}

	// Bootstrap scan: walk all cached Nodes once after cache sync so that
	// pre-existing failure conditions are reported immediately on operator startup.
	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("Node watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanNodes, w.config.ScanInterval)

	w.log.Info("Started node watcher",
		"incidentNamespace", w.config.IncidentNamespace,
		"scanInterval", w.config.ScanInterval.String(),
	)
	return nil
}

// checkConditions inspects all relevant conditions on a Node and emits events
// as needed.  Recovery (condition returning to normal) clears the dedup record
// so the next activation fires a fresh event.
func (w *NodeWatcher) checkConditions(node *corev1.Node) {
	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeReady:
			w.handleReadyCondition(node, cond)
		case corev1.NodeDiskPressure:
			w.handlePressureCondition(node, cond, "DiskPressure")
		case corev1.NodeMemoryPressure:
			w.handlePressureCondition(node, cond, "MemoryPressure")
		case corev1.NodePIDPressure:
			w.handlePressureCondition(node, cond, "PIDPressure")
		}
	}
}

// handleReadyCondition emits NodeNotReadyEvent when Ready is False or Unknown.
// When the node recovers (Ready=True) the dedup record is cleared so the next
// failure can fire again.
func (w *NodeWatcher) handleReadyCondition(node *corev1.Node, cond corev1.NodeCondition) {
	key := nodeAlertKey(node.UID, "NotReady")

	if cond.Status == corev1.ConditionTrue {
		// Node recovered — clear the gate so the next outage fires a fresh event.
		w.clearAlerted(key)
		return
	}

	// Status is False or Unknown — node is not ready.
	if !w.markAlerted(key) {
		return // already emitted for this outage
	}

	at := cond.LastTransitionTime.Time
	if at.IsZero() {
		at = w.clock()
	}

	w.emitter.Emit(NodeNotReadyEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: w.config.IncidentNamespace,
			NodeName:  node.Name,
		},
		Reason:  cond.Reason,
		Message: cond.Message,
	})
}

// handlePressureCondition emits NodePressureEvent when the given condition is True.
// When it resolves (False) the dedup record is cleared.
func (w *NodeWatcher) handlePressureCondition(node *corev1.Node, cond corev1.NodeCondition, pressureType string) {
	key := nodeAlertKey(node.UID, pressureType)

	if cond.Status != corev1.ConditionTrue {
		// Pressure cleared — allow the next activation to fire.
		w.clearAlerted(key)
		return
	}

	if !w.markAlerted(key) {
		return // already emitted for this pressure episode
	}

	at := cond.LastTransitionTime.Time
	if at.IsZero() {
		at = w.clock()
	}

	w.emitter.Emit(NodePressureEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: w.config.IncidentNamespace,
			NodeName:  node.Name,
		},
		PressureType: pressureType,
		Message:      cond.Message,
	})
}

// bootstrapScan walks all cached Nodes once after cache sync and calls
// checkConditions so pre-existing failures are reported immediately.
func (w *NodeWatcher) bootstrapScan(ctx context.Context) {
	nodeList := &corev1.NodeList{}
	if err := w.cache.List(ctx, nodeList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list nodes for bootstrap scan")
		return
	}

	for i := range nodeList.Items {
		w.checkConditions(&nodeList.Items[i])
	}

	w.log.Info("Node watcher bootstrap scan complete", "scanned", len(nodeList.Items))
}

// scanNodes is the periodic fallback scan.  It ensures conditions are detected
// even when no informer update arrives (possible on very quiet clusters).
func (w *NodeWatcher) scanNodes(ctx context.Context) {
	nodeList := &corev1.NodeList{}
	if err := w.cache.List(ctx, nodeList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list nodes during periodic scan")
		return
	}

	for i := range nodeList.Items {
		w.checkConditions(&nodeList.Items[i])
	}
}

// markAlerted records that an event was emitted for the given key.
// Returns true if the record is new (caller should emit), false if already set.
func (w *NodeWatcher) markAlerted(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.alerted[key] {
		return false
	}
	w.alerted[key] = true
	return true
}

// clearAlerted removes the dedup record for key so the next condition activation
// can fire a fresh event.
func (w *NodeWatcher) clearAlerted(key string) {
	w.mu.Lock()
	delete(w.alerted, key)
	w.mu.Unlock()
}

// nodeAlertKey builds a stable dedup map key from a node UID and condition type.
func nodeAlertKey(uid types.UID, conditionType string) string {
	return string(uid) + ":" + conditionType
}

// toNode safely casts an informer object to *corev1.Node, handling the tombstone
// wrapper that controller-runtime uses for deleted objects.
func toNode(obj any) (*corev1.Node, bool) {
	switch t := obj.(type) {
	case *corev1.Node:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		node, ok := t.Obj.(*corev1.Node)
		return node, ok
	default:
		return nil, false
	}
}
