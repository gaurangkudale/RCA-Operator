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

const (
	defaultCrashLoopRestartThreshold int32 = 3
	defaultPendingTimeout                  = 5 * time.Minute
	defaultPendingScanInterval             = 30 * time.Second
	defaultReadyStabilityWindow            = 60 * time.Second
)

// PodWatcherConfig controls pod failure detection thresholds.
type PodWatcherConfig struct {
	AgentName                 string
	CrashLoopRestartThreshold int32
	PendingTimeout            time.Duration
	PendingScanInterval       time.Duration
	ReadyStabilityWindow      time.Duration
	WatchNamespaces           []string
}

// PodWatcher monitors pods and emits typed watch events for correlator processing.
type PodWatcher struct {
	cache   ctrlcache.Cache
	emitter EventEmitter
	log     logr.Logger
	config  PodWatcherConfig
	clock   func() time.Time

	mu             sync.Mutex
	pendingAlerted map[types.UID]bool
	graceAlerted   map[types.UID]bool
	namespaceSet   map[string]struct{}
	readySince     map[types.UID]time.Time
	healthyAlerted map[types.UID]bool
}

// NewPodWatcher creates a pod watcher backed by controller-runtime informers.
func NewPodWatcher(cache ctrlcache.Cache, emitter EventEmitter, logger logr.Logger, cfg PodWatcherConfig) *PodWatcher {
	if cfg.CrashLoopRestartThreshold <= 0 {
		cfg.CrashLoopRestartThreshold = defaultCrashLoopRestartThreshold
	}
	if cfg.PendingTimeout <= 0 {
		cfg.PendingTimeout = defaultPendingTimeout
	}
	if cfg.PendingScanInterval <= 0 {
		cfg.PendingScanInterval = defaultPendingScanInterval
	}
	if cfg.ReadyStabilityWindow <= 0 {
		cfg.ReadyStabilityWindow = defaultReadyStabilityWindow
	}

	return &PodWatcher{
		cache:          cache,
		emitter:        emitter,
		log:            logger.WithName("pod-watcher"),
		config:         cfg,
		clock:          time.Now,
		pendingAlerted: make(map[types.UID]bool),
		graceAlerted:   make(map[types.UID]bool),
		namespaceSet:   toNamespaceSet(cfg.WatchNamespaces),
		readySince:     make(map[types.UID]time.Time),
		healthyAlerted: make(map[types.UID]bool),
	}
}

// Start registers informer handlers and launches periodic pending-pod scans.
func (w *PodWatcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &corev1.Pod{})
	if err != nil {
		return fmt.Errorf("failed to get pod informer: %w", err)
	}

	_, err = informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			pod, ok := toPod(obj)
			if !ok {
				return
			}
			w.onPodAdd(pod)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldPod, oldOK := toPod(oldObj)
			newPod, newOK := toPod(newObj)
			if !oldOK || !newOK {
				return
			}
			w.onPodUpdate(oldPod, newPod)
		},
		DeleteFunc: func(obj any) {
			pod, ok := toPod(obj)
			if !ok {
				return
			}
			w.onPodDelete(pod)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add pod informer handler: %w", err)
	}

	// Bootstrap once after cache sync so existing failing pods are reported
	// immediately when the watcher starts (no need to wait for the next update).
	go func() {
		if !w.cache.WaitForCacheSync(ctx) {
			w.log.Info("Pod watcher bootstrap scan skipped because cache did not sync")
			return
		}
		w.scanCurrentFailureSignals(ctx)
	}()

	go wait.UntilWithContext(ctx, w.scanPendingPods, w.config.PendingScanInterval)
	go wait.UntilWithContext(ctx, w.scanReadyPods, w.config.PendingScanInterval)
	go wait.UntilWithContext(ctx, w.scanGracePeriodViolations, w.config.PendingScanInterval)
	w.log.Info("Started pod watcher",
		"crashLoopThreshold", w.config.CrashLoopRestartThreshold,
		"pendingTimeout", w.config.PendingTimeout.String(),
		"pendingScanInterval", w.config.PendingScanInterval.String(),
		"readyStabilityWindow", w.config.ReadyStabilityWindow.String(),
	)

	return nil
}

func (w *PodWatcher) onPodAdd(pod *corev1.Pod) {
	if !w.shouldWatchNamespace(pod.Namespace) {
		return
	}
	w.detectPodHealthy(nil, pod)
	w.detectImagePullBackOff(nil, pod)
	w.detectCrashLoop(nil, pod)
	w.detectOOMKilled(nil, pod)
	w.detectContainerExitCode(nil, pod)
	w.detectGracePeriodViolation(pod)
	w.updatePendingState(pod)
}

func (w *PodWatcher) onPodUpdate(oldPod, newPod *corev1.Pod) {
	if !w.shouldWatchNamespace(newPod.Namespace) {
		return
	}
	w.detectPodHealthy(oldPod, newPod)
	w.detectImagePullBackOff(oldPod, newPod)
	w.detectCrashLoop(oldPod, newPod)
	w.detectOOMKilled(oldPod, newPod)
	w.detectContainerExitCode(oldPod, newPod)
	w.detectGracePeriodViolation(newPod)
	w.updatePendingState(newPod)
}

func (w *PodWatcher) detectPodHealthy(oldPod, newPod *corev1.Pod) {
	w.trackReadyState(oldPod, newPod)
}

func (w *PodWatcher) onPodDelete(pod *corev1.Pod) {
	if w.shouldWatchNamespace(pod.Namespace) {
		w.emitter.Emit(PodDeletedEvent{
			BaseEvent: baseEventFromPod(pod, w.config.AgentName, w.clock()),
		})
	}
	w.mu.Lock()
	delete(w.pendingAlerted, pod.UID)
	delete(w.graceAlerted, pod.UID)
	delete(w.readySince, pod.UID)
	delete(w.healthyAlerted, pod.UID)
	w.mu.Unlock()
}

func (w *PodWatcher) detectCrashLoop(oldPod, newPod *corev1.Pod) {
	oldStatuses := statusByContainer(oldPod)
	for _, status := range newPod.Status.ContainerStatuses {
		if status.State.Waiting == nil || status.State.Waiting.Reason != "CrashLoopBackOff" {
			continue
		}
		if status.RestartCount < w.config.CrashLoopRestartThreshold {
			continue
		}

		oldStatus, hasOld := oldStatuses[status.Name]
		if hasOld {
			// Emit when entering CrashLoopBackOff at threshold even if restart count
			// did not change between the previous and current pod updates.
			if oldStatus.State.Waiting != nil && oldStatus.State.Waiting.Reason == "CrashLoopBackOff" && status.RestartCount <= oldStatus.RestartCount {
				continue
			}
		}

		w.emitter.Emit(CrashLoopBackOffEvent{
			BaseEvent:     baseEventFromPod(newPod, w.config.AgentName, w.clock()),
			ContainerName: status.Name,
			RestartCount:  status.RestartCount,
			Threshold:     w.config.CrashLoopRestartThreshold,
		})
	}
}

func (w *PodWatcher) detectOOMKilled(oldPod, newPod *corev1.Pod) {
	oldStatuses := statusByContainer(oldPod)
	for _, status := range newPod.Status.ContainerStatuses {
		terminated := status.LastTerminationState.Terminated
		if terminated == nil {
			terminated = status.State.Terminated
		}
		if terminated == nil {
			continue
		}

		// Only treat as OOM when kubelet explicitly marks reason="OOMKilled".
		// Exit code 137 (SIGKILL) alone is insufficient — liveness probe kills,
		// manual pod deletions, and other SIGKILL scenarios all produce exit code 137
		// with reason="Error".  Those signals are handled by event_watcher (ProbeFailure)
		// or detectContainerExitCode, not here.
		if terminated.Reason != "OOMKilled" {
			continue
		}

		oldStatus, hasOld := oldStatuses[status.Name]
		if hasOld && status.RestartCount <= oldStatus.RestartCount {
			continue
		}

		w.emitter.Emit(OOMKilledEvent{
			BaseEvent:     baseEventFromPod(newPod, w.config.AgentName, w.clock()),
			ContainerName: status.Name,
			ExitCode:      terminated.ExitCode,
			Reason:        terminated.Reason,
		})
	}
}

func (w *PodWatcher) detectContainerExitCode(oldPod, newPod *corev1.Pod) {
	oldStatuses := statusByContainer(oldPod)
	for _, status := range newPod.Status.ContainerStatuses {
		terminated := status.LastTerminationState.Terminated
		if terminated == nil {
			terminated = status.State.Terminated
		}
		if terminated == nil {
			continue
		}
		if terminated.ExitCode == 0 {
			continue
		}
		// OOM has dedicated handling and incident type.
		if terminated.ExitCode == 137 || terminated.Reason == "OOMKilled" {
			continue
		}

		oldStatus, hasOld := oldStatuses[status.Name]
		if hasOld && status.RestartCount <= oldStatus.RestartCount {
			continue
		}

		category, description := classifyExitCode(terminated.ExitCode)
		w.emitter.Emit(ContainerExitCodeEvent{
			BaseEvent:     baseEventFromPod(newPod, w.config.AgentName, w.clock()),
			ContainerName: status.Name,
			ExitCode:      terminated.ExitCode,
			Reason:        terminated.Reason,
			Category:      category,
			Description:   description,
		})
	}
}

func (w *PodWatcher) detectImagePullBackOff(oldPod, newPod *corev1.Pod) {
	oldStatuses := statusByContainer(oldPod)
	for _, status := range newPod.Status.ContainerStatuses {
		if status.State.Waiting == nil {
			continue
		}

		reason := status.State.Waiting.Reason
		if reason != "ImagePullBackOff" && reason != "ErrImagePull" {
			continue
		}

		oldStatus, hasOld := oldStatuses[status.Name]
		if hasOld && oldStatus.State.Waiting != nil && oldStatus.State.Waiting.Reason == reason {
			continue
		}

		w.emitter.Emit(ImagePullBackOffEvent{
			BaseEvent:     baseEventFromPod(newPod, w.config.AgentName, w.clock()),
			ContainerName: status.Name,
			Reason:        reason,
			Message:       status.State.Waiting.Message,
		})
	}
}

// hasImagePullFailure returns true if any container (including init containers) is waiting
// due to an image-pull error. Used to avoid emitting a redundant BadDeploy incident when a
// Registry incident already captures the same root cause.
func hasImagePullFailure(pod *corev1.Pod) bool {
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, s := range statuses {
			if s.State.Waiting != nil && (s.State.Waiting.Reason == "ImagePullBackOff" || s.State.Waiting.Reason == "ErrImagePull") {
				return true
			}
		}
	}
	return false
}

// scanPendingPods catches pending pods that may not receive frequent updates.
func (w *PodWatcher) scanPendingPods(ctx context.Context) {
	podList := &corev1.PodList{}
	if err := w.cache.List(ctx, podList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list pods for pending timeout scan")
		return
	}

	now := w.clock()
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !w.shouldWatchNamespace(pod.Namespace) {
			continue
		}
		if pod.Status.Phase != corev1.PodPending {
			w.clearPendingAlerted(pod.UID)
			continue
		}

		// Skip pods already diagnosed as an image-pull failure; the Registry incident covers them.
		if hasImagePullFailure(pod) {
			continue
		}

		pendingFor := now.Sub(pod.CreationTimestamp.Time)
		if pendingFor < w.config.PendingTimeout {
			continue
		}

		if !w.markPendingAlerted(pod.UID) {
			continue
		}

		w.emitter.Emit(PodPendingTooLongEvent{
			BaseEvent:  baseEventFromPod(pod, w.config.AgentName, now),
			PendingFor: pendingFor,
			Timeout:    w.config.PendingTimeout,
		})
	}
}

func (w *PodWatcher) scanCurrentFailureSignals(ctx context.Context) {
	podList := &corev1.PodList{}
	if err := w.cache.List(ctx, podList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list pods for startup failure scan")
		return
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if !w.shouldWatchNamespace(pod.Namespace) {
			continue
		}

		// old=nil intentionally treats current state as fresh signal for startup bootstrap.
		w.detectImagePullBackOff(nil, pod)
		w.detectCrashLoop(nil, pod)
		w.detectOOMKilled(nil, pod)
		w.detectContainerExitCode(nil, pod)
		w.detectGracePeriodViolation(pod)
		w.trackReadyState(nil, pod)
	}
}

func (w *PodWatcher) scanGracePeriodViolations(ctx context.Context) {
	podList := &corev1.PodList{}
	if err := w.cache.List(ctx, podList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list pods for grace period scan")
		return
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if !w.shouldWatchNamespace(pod.Namespace) {
			continue
		}
		w.detectGracePeriodViolation(pod)
	}
}

func (w *PodWatcher) detectGracePeriodViolation(pod *corev1.Pod) {
	if pod.DeletionTimestamp == nil {
		w.clearGraceAlerted(pod.UID)
		return
	}

	graceSeconds := gracePeriodSeconds(pod)
	now := w.clock()
	deadline := pod.DeletionTimestamp.Add(time.Duration(graceSeconds) * time.Second)
	if !now.After(deadline) || !hasRunningContainers(pod) {
		return
	}

	if !w.markGraceAlerted(pod.UID) {
		return
	}

	w.emitter.Emit(GracePeriodViolationEvent{
		BaseEvent:          baseEventFromPod(pod, w.config.AgentName, now),
		GracePeriodSeconds: graceSeconds,
		OverdueFor:         now.Sub(deadline),
	})
}

func (w *PodWatcher) scanReadyPods(ctx context.Context) {
	podList := &corev1.PodList{}
	if err := w.cache.List(ctx, podList, &client.ListOptions{}); err != nil {
		w.log.Error(err, "Failed to list pods for ready stability scan")
		return
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if !w.shouldWatchNamespace(pod.Namespace) {
			continue
		}
		w.trackReadyState(nil, pod)
	}
}

func (w *PodWatcher) trackReadyState(oldPod, newPod *corev1.Pod) {
	now := w.clock()
	uid := newPod.UID
	isReady := isPodReady(newPod)

	w.mu.Lock()
	defer w.mu.Unlock()

	if !isReady {
		delete(w.readySince, uid)
		delete(w.healthyAlerted, uid)
		return
	}

	since, ok := w.readySince[uid]
	readyTransition := podReadySince(newPod, now)
	oldReady := isPodReady(oldPod)
	if !ok {
		since = readyTransition
		w.readySince[uid] = since
		delete(w.healthyAlerted, uid)
	} else if oldPod != nil && !oldReady {
		since = readyTransition
		w.readySince[uid] = since
		delete(w.healthyAlerted, uid)
	}

	if w.healthyAlerted[uid] {
		return
	}
	if now.Sub(since) < w.config.ReadyStabilityWindow {
		return
	}

	w.healthyAlerted[uid] = true
	w.emitter.Emit(PodHealthyEvent{BaseEvent: baseEventFromPod(newPod, w.config.AgentName, now)})
}

func (w *PodWatcher) updatePendingState(pod *corev1.Pod) {
	if pod.Status.Phase != corev1.PodPending {
		w.clearPendingAlerted(pod.UID)
	}
}

func (w *PodWatcher) markPendingAlerted(uid types.UID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pendingAlerted[uid] {
		return false
	}
	w.pendingAlerted[uid] = true
	return true
}

func (w *PodWatcher) clearPendingAlerted(uid types.UID) {
	w.mu.Lock()
	delete(w.pendingAlerted, uid)
	w.mu.Unlock()
}

func (w *PodWatcher) markGraceAlerted(uid types.UID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.graceAlerted[uid] {
		return false
	}
	w.graceAlerted[uid] = true
	return true
}

func (w *PodWatcher) clearGraceAlerted(uid types.UID) {
	w.mu.Lock()
	delete(w.graceAlerted, uid)
	w.mu.Unlock()
}

func statusByContainer(pod *corev1.Pod) map[string]corev1.ContainerStatus {
	if pod == nil {
		return nil
	}

	out := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		out[status.Name] = status
	}
	return out
}

func baseEventFromPod(pod *corev1.Pod, agentName string, at time.Time) BaseEvent {
	return BaseEvent{
		At:        at,
		AgentName: agentName,
		Namespace: pod.Namespace,
		PodName:   pod.Name,
		PodUID:    string(pod.UID),
		NodeName:  pod.Spec.NodeName,
	}
}

func toPod(obj any) (*corev1.Pod, bool) {
	switch t := obj.(type) {
	case *corev1.Pod:
		return t, true
	case toolscache.DeletedFinalStateUnknown:
		pod, ok := t.Obj.(*corev1.Pod)
		return pod, ok
	default:
		return nil, false
	}
}

func toNamespaceSet(namespaces []string) map[string]struct{} {
	if len(namespaces) == 0 {
		return nil
	}

	out := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		out[ns] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (w *PodWatcher) shouldWatchNamespace(namespace string) bool {
	if len(w.namespaceSet) == 0 {
		return true
	}
	_, ok := w.namespaceSet[namespace]
	return ok
}

func isPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podReadySince(pod *corev1.Pod, fallback time.Time) time.Time {
	if pod == nil {
		return fallback
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type != corev1.PodReady {
			continue
		}
		if condition.Status != corev1.ConditionTrue {
			break
		}
		if condition.LastTransitionTime.IsZero() {
			break
		}
		return condition.LastTransitionTime.Time
	}
	return fallback
}

func classifyExitCode(exitCode int32) (string, string) {
	switch exitCode {
	case 1:
		return "GeneralError", "General application error"
	case 2:
		return "ShellMisuse", "Misuse of shell builtins"
	case 126:
		return "PermissionDenied", "Command invoked cannot execute"
	case 127:
		return "CommandNotFound", "Command not found"
	case 130:
		return "Interrupted", "Script terminated by Control-C"
	case 134:
		return "Abort", "Process aborted (SIGABRT)"
	case 139:
		return "SegmentationFault", "Segmentation fault (SIGSEGV)"
	case 143:
		return "Terminated", "Terminated by SIGTERM"
	case 255:
		return "OutOfRange", "Exit status out of range"
	default:
		return "NonZeroExit", "Unclassified non-zero exit code"
	}
}

func gracePeriodSeconds(pod *corev1.Pod) int64 {
	if pod != nil && pod.DeletionGracePeriodSeconds != nil && *pod.DeletionGracePeriodSeconds > 0 {
		return *pod.DeletionGracePeriodSeconds
	}
	return 30
}

func hasRunningContainers(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Running != nil {
			return true
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil {
			return true
		}
	}
	return false
}
