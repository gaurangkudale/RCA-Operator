package watcher

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

const (
	// defaultEventDedupWindow is the minimum interval between two emissions of the
	// same event key. Kubernetes increments Event.Count every few seconds for
	// repeated events; without this guard the correlator would receive hundreds of
	// identical signals per minute.
	defaultEventDedupWindow = 2 * time.Minute

	// defaultEventDedupSweepInterval controls how often stale dedup-map entries are
	// removed so the map does not grow unboundedly over the operator's lifetime.
	defaultEventDedupSweepInterval = 10 * time.Minute

	// bootstrapLookback is the age window used on startup to replay recent events
	// so that a watcher restart does not miss signals that arrived just before.
	bootstrapLookback = 5 * time.Minute

	// defaultThrottleThreshold is the number of CPUThrottlingHigh K8s Events that
	// must be observed for the same pod/container before a CPUThrottlingEvent is
	// sent to the correlator. This prevents transient one-off throttle spikes from
	// raising ResourceSaturation incidents.
	defaultThrottleThreshold = 3

	// defaultThrottleWindow is the inactivity duration after which the per-key
	// throttle hit counter resets. If no CPUThrottlingHigh event arrives within
	// this window the counter starts fresh and the threshold must be reached again.
	defaultThrottleWindow = 5 * time.Minute

	// k8s Event reason constants that event_watcher acts on.
	reasonOOMKilling           = "OOMKilling"
	reasonEvicted              = "Evicted"
	reasonUnhealthy            = "Unhealthy"
	reasonNodeNotReady         = "NodeNotReady"
	reasonNodeConditionChanged = "NodeConditionChanged"
	// reasonCPUThrottlingHigh is emitted by the kubelet when a container is
	// throttled by the CPU CFS scheduler beyond a configurable threshold.
	// The InvolvedObject is the Pod; FieldPath identifies the container.
	reasonCPUThrottlingHigh = "CPUThrottlingHigh"

	// involvedObjectKindPod and involvedObjectKindNode are the InvolvedObject.Kind
	// values used to route K8s Event records to the correct handler.
	involvedObjectKindPod  = "Pod"
	involvedObjectKindNode = "Node"
)

// EventWatcherConfig controls the behaviour of the Kubernetes Event stream watcher.
type EventWatcherConfig struct {
	// AgentName is stamped on every emitted event for correlator routing.
	AgentName string

	// WatchNamespaces restricts observation to these namespaces.
	// An empty slice means watch all namespaces.
	WatchNamespaces []string

	// DedupWindow is how long the same (namespace/objectUID/reason) key is
	// suppressed after the first emit. Defaults to defaultEventDedupWindow.
	DedupWindow time.Duration

	// DedupSweepInterval controls how often the dedup map is compacted.
	// Defaults to defaultEventDedupSweepInterval.
	DedupSweepInterval time.Duration

	// ThrottleThreshold is the number of CPUThrottlingHigh K8s Events that must
	// be observed for the same pod/container before a CPUThrottlingEvent is emitted.
	// Defaults to defaultThrottleThreshold (3).
	ThrottleThreshold int

	// ThrottleWindow is the inactivity period after which the throttle hit counter
	// for a pod/container resets to zero. Defaults to defaultThrottleWindow (5 min).
	ThrottleWindow time.Duration
}

// EventWatcher watches the core/v1 Event stream and emits typed CorrelatorEvents
// for OOM kills, pod evictions, probe failures, and node NotReady transitions.
//
// It is intentionally read-only: it never writes to the Kubernetes API.
type EventWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  EventWatcherConfig
	clock   func() time.Time

	mu              sync.Mutex
	dedupSeen       map[string]time.Time // key: namespace/objectUID/reason → lastEmittedAt
	namespaceSet    map[string]struct{}
	throttleHits    map[string]int       // key: dedup key → number of CPUThrottlingHigh hits seen
	throttleLastHit map[string]time.Time // key: dedup key → time of most recent hit
}

// NewEventWatcher creates an EventWatcher backed by a controller-runtime cache.
// Config fields are defaulted when zero.
func NewEventWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg EventWatcherConfig) *EventWatcher {
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = defaultEventDedupWindow
	}
	if cfg.DedupSweepInterval <= 0 {
		cfg.DedupSweepInterval = defaultEventDedupSweepInterval
	}
	if cfg.ThrottleThreshold <= 0 {
		cfg.ThrottleThreshold = defaultThrottleThreshold
	}
	if cfg.ThrottleWindow <= 0 {
		cfg.ThrottleWindow = defaultThrottleWindow
	}

	return &EventWatcher{
		cache:           cache,
		emitter:         emitter,
		log:             logger.WithName("event-watcher"),
		config:          cfg,
		clock:           time.Now,
		dedupSeen:       make(map[string]time.Time),
		namespaceSet:    toNamespaceSet(cfg.WatchNamespaces),
		throttleHits:    make(map[string]int),
		throttleLastHit: make(map[string]time.Time),
	}
}

// Start registers informer handlers, runs a bootstrap replay scan, and launches
// the periodic dedup-map sweep. It is non-blocking; goroutines are bounded by ctx.
func (w *EventWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &corev1.Event{})
	if err != nil {
		return fmt.Errorf("failed to get event informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			ev, ok := toK8sEvent(obj)
			if !ok {
				return
			}
			w.onEventAdd(ev)
		},
		UpdateFunc: func(_, newObj any) {
			ev, ok := toK8sEvent(newObj)
			if !ok {
				return
			}
			w.onEventUpdate(ev)
		},
		// DeleteFunc is intentionally omitted: deleting a K8s Event record is not
		// a signal that warrants incident creation or resolution.
	})
	if err != nil {
		return fmt.Errorf("failed to add event informer handler: %w", err)
	}

	// Bootstrap replay: walk events that arrived while the operator was down so
	// no signal is missed across a restart.
	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("Event watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.bootstrapScan(ctx)
	}()

	go wait.UntilWithContext(ctx, w.sweepDedupMap, w.config.DedupSweepInterval)

	w.log.Info("Started event watcher",
		"dedupWindow", w.config.DedupWindow.String(),
		"dedupSweepInterval", w.config.DedupSweepInterval.String(),
	)
	return nil
}

// onEventAdd handles newly observed K8s Event objects.
func (w *EventWatcher) onEventAdd(ev *corev1.Event) {
	if !w.shouldWatchNamespace(ev.Namespace) {
		return
	}
	w.route(ev)
}

// onEventUpdate handles K8s Event objects that were updated (count incremented).
func (w *EventWatcher) onEventUpdate(ev *corev1.Event) {
	if !w.shouldWatchNamespace(ev.Namespace) {
		return
	}
	w.route(ev)
}

// route dispatches a K8s Event to the appropriate handler based on its Reason.
func (w *EventWatcher) route(ev *corev1.Event) {
	switch ev.Reason {
	case reasonOOMKilling:
		w.handleOOMKilling(ev)
	case reasonEvicted:
		w.handleEviction(ev)
	case reasonUnhealthy:
		w.handleProbeFailure(ev)
	case reasonNodeNotReady, reasonNodeConditionChanged:
		w.handleNodeNotReady(ev)
	case reasonCPUThrottlingHigh:
		w.handleCPUThrottling(ev)
	}
	// All other reasons are intentionally ignored in Phase 1.
}

// handleOOMKilling emits an OOMKilledEvent when the kubelet reports it is about
// to kill a container for exceeding memory limits. This fires before the container
// termination state is written to Pod.Status, giving the correlator an earlier signal.
func (w *EventWatcher) handleOOMKilling(ev *corev1.Event) {
	if ev.InvolvedObject.Kind != involvedObjectKindPod {
		return
	}

	key := dedupKey(ev.Namespace, string(ev.InvolvedObject.UID), ev.Reason)
	if !w.shouldEmit(key) {
		return
	}

	at := eventTimestamp(ev, w.clock())
	w.emitter.Emit(OOMKilledEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ev.Namespace,
			PodName:   ev.InvolvedObject.Name,
			PodUID:    string(ev.InvolvedObject.UID),
			NodeName:  ev.Source.Host,
		},
		// ContainerName is not reliably present in the Event message field;
		// pod_watcher provides it when the termination state is written.
		ContainerName: "",
		ExitCode:      0,
		Reason:        ev.Reason,
	})
}

// handleEviction emits a PodEvictedEvent when a pod is forcibly removed from a node
// due to resource pressure (DiskPressure, MemoryPressure, PIDPressure, etc.).
func (w *EventWatcher) handleEviction(ev *corev1.Event) {
	if ev.InvolvedObject.Kind != involvedObjectKindPod {
		return
	}

	key := dedupKey(ev.Namespace, string(ev.InvolvedObject.UID), ev.Reason)
	if !w.shouldEmit(key) {
		return
	}

	at := eventTimestamp(ev, w.clock())
	w.emitter.Emit(PodEvictedEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ev.Namespace,
			PodName:   ev.InvolvedObject.Name,
			PodUID:    string(ev.InvolvedObject.UID),
			NodeName:  ev.Source.Host,
		},
		Reason:  ev.Reason,
		Message: ev.Message,
	})
}

// handleProbeFailure emits a ProbeFailureEvent when a container probe fails.
// The Kubernetes kubelet fires an Unhealthy event with the probe type embedded
// in the message (e.g. "Liveness probe failed: ...").
func (w *EventWatcher) handleProbeFailure(ev *corev1.Event) {
	if ev.InvolvedObject.Kind != involvedObjectKindPod {
		return
	}

	probeType := parseProbeType(ev.Message)
	key := dedupKey(ev.Namespace, string(ev.InvolvedObject.UID), ev.Reason+"/"+probeType)
	if !w.shouldEmit(key) {
		return
	}

	at := eventTimestamp(ev, w.clock())
	w.emitter.Emit(ProbeFailureEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ev.Namespace,
			PodName:   ev.InvolvedObject.Name,
			PodUID:    string(ev.InvolvedObject.UID),
			NodeName:  ev.Source.Host,
		},
		ProbeType: probeType,
		Message:   ev.Message,
	})
}

// handleNodeNotReady emits a NodeNotReadyEvent when the cluster control-plane
// reports that a node has left the Ready condition.
func (w *EventWatcher) handleNodeNotReady(ev *corev1.Event) {
	if ev.InvolvedObject.Kind != involvedObjectKindNode {
		return
	}

	key := dedupKey(ev.Namespace, string(ev.InvolvedObject.UID), ev.Reason)
	if !w.shouldEmit(key) {
		return
	}

	at := eventTimestamp(ev, w.clock())
	w.emitter.Emit(NodeNotReadyEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ev.Namespace,
			NodeName:  ev.InvolvedObject.Name,
		},
		Reason:  ev.Reason,
		Message: ev.Message,
	})
}

// handleCPUThrottling emits a CPUThrottlingEvent when the kubelet reports that a
// container is being throttled by the CPU CFS scheduler.  The container name is
// extracted from InvolvedObject.FieldPath (format: "spec.containers{name}").
//
// A threshold gate suppresses emission until ThrottleThreshold events have been
// observed for the same pod/container. This prevents transient one-off spikes
// from raising ResourceSaturation incidents. The counter resets to zero after a
// successful emit so the next burst must accumulate again.
func (w *EventWatcher) handleCPUThrottling(ev *corev1.Event) {
	if ev.InvolvedObject.Kind != involvedObjectKindPod {
		return
	}

	containerName := parseContainerFromFieldPath(ev.InvolvedObject.FieldPath)
	key := dedupKey(ev.Namespace, string(ev.InvolvedObject.UID), ev.Reason+"/"+containerName)

	// Accumulate hit count; record time of last hit for the sweep.
	w.mu.Lock()
	w.throttleHits[key]++
	w.throttleLastHit[key] = w.clock()
	hitCount := w.throttleHits[key]
	w.mu.Unlock()

	// Threshold not yet reached — suppress and log at debug level.
	if hitCount < w.config.ThrottleThreshold {
		w.log.V(1).Info("CPUThrottling hit below threshold — suppressing",
			"key", key,
			"hits", hitCount,
			"threshold", w.config.ThrottleThreshold,
		)
		return
	}

	// Threshold reached — apply the standard dedup guard so we don't re-emit
	// within DedupWindow even while the counter keeps ticking.
	if !w.shouldEmit(key) {
		return
	}

	// Reset counter after a successful emit so the next burst re-accumulates.
	w.mu.Lock()
	w.throttleHits[key] = 0
	w.mu.Unlock()

	at := eventTimestamp(ev, w.clock())
	w.emitter.Emit(CPUThrottlingEvent{
		BaseEvent: BaseEvent{
			At:        at,
			AgentName: w.config.AgentName,
			Namespace: ev.Namespace,
			PodName:   ev.InvolvedObject.Name,
			PodUID:    string(ev.InvolvedObject.UID),
			NodeName:  ev.Source.Host,
		},
		ContainerName: containerName,
		Message:       ev.Message,
	})
}

// parseContainerFromFieldPath extracts the container name from a kubelet
// InvolvedObject.FieldPath string of the form "spec.containers{containerName}".
// Returns an empty string when the format is not recognised.
func parseContainerFromFieldPath(fieldPath string) string {
	open := strings.Index(fieldPath, "{")
	if open < 0 {
		return ""
	}
	name := fieldPath[open+1:]
	name = strings.TrimSuffix(name, "}")
	return name
}

// bootstrapScan replays recent K8s Events so that a watcher restart does not
// miss signals that arrived during the downtime window.
func (w *EventWatcher) bootstrapScan(ctx context.Context) {
	eventList := &corev1.EventList{}
	if err := w.cache.List(ctx, eventList); err != nil {
		w.log.Error(err, "Failed to list events for bootstrap scan")
		return
	}

	cutoff := w.clock().Add(-bootstrapLookback)
	replayed := 0
	for i := range eventList.Items {
		ev := &eventList.Items[i]
		if !w.shouldWatchNamespace(ev.Namespace) {
			continue
		}
		ts := eventTimestamp(ev, w.clock())
		if ts.Before(cutoff) {
			continue
		}
		w.route(ev)
		replayed++
	}

	w.log.Info("Event watcher bootstrap scan complete", "replayed", replayed)
}

// sweepDedupMap removes entries that have been idle longer than 2× DedupWindow
// to prevent the map from growing unboundedly over the operator's lifetime.
// It also resets the throttle hit counter for keys idle longer than ThrottleWindow
// so infrequent throttle events don't prevent future threshold triggering.
func (w *EventWatcher) sweepDedupMap(_ context.Context) {
	now := w.clock()
	dedupCutoff := now.Add(-2 * w.config.DedupWindow)
	throttleCutoff := now.Add(-w.config.ThrottleWindow)
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, lastSeen := range w.dedupSeen {
		if lastSeen.Before(dedupCutoff) {
			delete(w.dedupSeen, key)
		}
	}
	for key, lastHit := range w.throttleLastHit {
		if lastHit.Before(throttleCutoff) {
			delete(w.throttleHits, key)
			delete(w.throttleLastHit, key)
		}
	}
}

// shouldEmit returns true and records the current time when the given key has
// not been seen within the configured DedupWindow. Returns false otherwise.
// Thread-safe.
func (w *EventWatcher) shouldEmit(key string) bool {
	now := w.clock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.dedupSeen[key]; ok && now.Sub(last) < w.config.DedupWindow {
		return false
	}
	w.dedupSeen[key] = now
	return true
}

func (w *EventWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

// dedupKey builds a stable map key from the event's identifying dimensions.
func dedupKey(namespace, objectUID, reason string) string {
	return namespace + "/" + objectUID + "/" + reason
}

// eventTimestamp returns the most precise timestamp available on a K8s Event,
// falling back to the provided default when all fields are zero.
func eventTimestamp(ev *corev1.Event, fallback time.Time) time.Time {
	if !ev.EventTime.IsZero() {
		return ev.EventTime.Time
	}
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if !ev.FirstTimestamp.IsZero() {
		return ev.FirstTimestamp.Time
	}
	return fallback
}

// parseProbeType extracts "Liveness", "Readiness", or "Startup" from a kubelet
// Unhealthy event message. Returns "Unknown" when the type cannot be determined.
func parseProbeType(message string) string {
	msg := strings.ToLower(message)
	switch {
	case strings.HasPrefix(msg, "liveness"):
		return "Liveness"
	case strings.HasPrefix(msg, "readiness"):
		return "Readiness"
	case strings.HasPrefix(msg, "startup"):
		return "Startup"
	default:
		return "Unknown"
	}
}

// toK8sEvent safely casts an informer object to *corev1.Event, handling the
// tombstone wrapper that controller-runtime uses for deleted objects.
func toK8sEvent(obj any) (*corev1.Event, bool) {
	switch t := obj.(type) {
	case *corev1.Event:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		ev, ok := t.Obj.(*corev1.Event)
		return ev, ok
	default:
		return nil, false
	}
}
