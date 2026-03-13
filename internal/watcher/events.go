package watcher

import "time"

// EventType identifies the concrete watcher signal type sent to the correlator.
type EventType string

const (
	EventTypeCrashLoopBackOff     EventType = "CrashLoopBackOff"
	EventTypeOOMKilled            EventType = "OOMKilled"
	EventTypeImagePullBackOff     EventType = "ImagePullBackOff"
	EventTypePodPendingTooLong    EventType = "PodPendingTooLong"
	EventTypeGracePeriodViolation EventType = "GracePeriodViolation"
	EventTypePodHealthy           EventType = "PodHealthy"
	EventTypePodDeleted           EventType = "PodDeleted"

	// Event-stream-sourced signals (detected from core/v1 Event objects).
	EventTypeNodeNotReady EventType = "NodeNotReady"
	EventTypePodEvicted   EventType = "PodEvicted"
	EventTypeProbeFailure EventType = "ProbeFailure"

	// Deployment-sourced signals (detected from apps/v1 Deployment objects).
	EventTypeStalledRollout EventType = "StalledRollout"

	// Node-condition-sourced signals (detected from corev1.Node objects by node_watcher.go).
	// NodeNotReady is also captured via event_watcher.go; both paths feed the correlator
	// and the dedup key (namespace+nodeName) prevents duplicate incidents.
	EventTypeNodePressure EventType = "NodePressure"

	// CPU-throttling signal emitted when the kubelet fires a CPUThrottlingHigh
	// warning on a container. Sourced from the core/v1 Event stream.
	EventTypeCPUThrottling EventType = "CPUThrottling"
)

// CorrelatorEvent is the shared typed event interface consumed by the correlator.
type CorrelatorEvent interface {
	Type() EventType
	OccurredAt() time.Time
	DedupKey() string
}

// BaseEvent carries fields common to all watcher-originated signals.
type BaseEvent struct {
	At        time.Time
	AgentName string
	Namespace string
	PodName   string
	PodUID    string
	NodeName  string
}

// CrashLoopBackOffEvent is emitted when a pod container repeatedly restarts in CrashLoopBackOff.
// It may include the last exit code and classification to provide diagnostic context.
type CrashLoopBackOffEvent struct {
	BaseEvent
	ContainerName string
	RestartCount  int32
	Threshold     int32
	// Exit code info (optional) — captured from last container termination
	LastExitCode        int32  // 0 if not available
	ExitCodeCategory    string // e.g., "PermissionDenied", or empty if not available
	ExitCodeDescription string // human-readable description, or empty if not available
}

func (e CrashLoopBackOffEvent) Type() EventType       { return EventTypeCrashLoopBackOff }
func (e CrashLoopBackOffEvent) OccurredAt() time.Time { return e.At }
func (e CrashLoopBackOffEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.ContainerName
}

// OOMKilledEvent is emitted when a container terminates with OOMKilled semantics.
type OOMKilledEvent struct {
	BaseEvent
	ContainerName string
	ExitCode      int32
	Reason        string
}

func (e OOMKilledEvent) Type() EventType       { return EventTypeOOMKilled }
func (e OOMKilledEvent) OccurredAt() time.Time { return e.At }
func (e OOMKilledEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.ContainerName + ":" + e.PodUID
}

// ImagePullBackOffEvent is emitted when image pull for a container fails.
type ImagePullBackOffEvent struct {
	BaseEvent
	ContainerName string
	Reason        string
	Message       string
}

func (e ImagePullBackOffEvent) Type() EventType       { return EventTypeImagePullBackOff }
func (e ImagePullBackOffEvent) OccurredAt() time.Time { return e.At }
func (e ImagePullBackOffEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.ContainerName
}

// PodPendingTooLongEvent is emitted when a pod remains Pending beyond configured timeout.
type PodPendingTooLongEvent struct {
	BaseEvent
	PendingFor time.Duration
	Timeout    time.Duration
}

func (e PodPendingTooLongEvent) Type() EventType       { return EventTypePodPendingTooLong }
func (e PodPendingTooLongEvent) OccurredAt() time.Time { return e.At }
func (e PodPendingTooLongEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.PodUID
}

// GracePeriodViolationEvent is emitted when a deleting pod exceeds termination grace period
// while at least one container is still running.
type GracePeriodViolationEvent struct {
	BaseEvent
	GracePeriodSeconds int64
	OverdueFor         time.Duration
}

func (e GracePeriodViolationEvent) Type() EventType       { return EventTypeGracePeriodViolation }
func (e GracePeriodViolationEvent) OccurredAt() time.Time { return e.At }
func (e GracePeriodViolationEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.PodUID
}

// PodHealthyEvent is emitted when a pod transitions to Running and Ready.
type PodHealthyEvent struct {
	BaseEvent
}

func (e PodHealthyEvent) Type() EventType       { return EventTypePodHealthy }
func (e PodHealthyEvent) OccurredAt() time.Time { return e.At }
func (e PodHealthyEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName
}

// PodDeletedEvent is emitted when a watched pod is removed from the cluster.
// It triggers immediate resolution of any Active incidents referencing the pod.
type PodDeletedEvent struct {
	BaseEvent
}

func (e PodDeletedEvent) Type() EventType       { return EventTypePodDeleted }
func (e PodDeletedEvent) OccurredAt() time.Time { return e.At }
func (e PodDeletedEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName
}

// NodeNotReadyEvent is emitted when a Kubernetes Node transitions to NotReady.
// Sourced from the core/v1 Event stream (reason: NodeNotReady or NodeConditionChanged).
type NodeNotReadyEvent struct {
	BaseEvent
	// NodeName is the name of the node that went NotReady.
	// Overrides BaseEvent.NodeName for clarity; PodName is empty for node-level events.
	Reason  string
	Message string
}

func (e NodeNotReadyEvent) Type() EventType       { return EventTypeNodeNotReady }
func (e NodeNotReadyEvent) OccurredAt() time.Time { return e.At }
func (e NodeNotReadyEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.NodeName
}

// PodEvictedEvent is emitted when a pod is evicted from a node due to resource pressure.
// Sourced from the core/v1 Event stream (reason: Evicted).
type PodEvictedEvent struct {
	BaseEvent
	Reason  string
	Message string
}

func (e PodEvictedEvent) Type() EventType       { return EventTypePodEvicted }
func (e PodEvictedEvent) OccurredAt() time.Time { return e.At }
func (e PodEvictedEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.PodUID
}

// ProbeFailureEvent is emitted when a container's liveness, readiness, or startup probe fails.
// Sourced from the core/v1 Event stream (reason: Unhealthy).
type ProbeFailureEvent struct {
	BaseEvent
	// ProbeType is one of "Liveness", "Readiness", or "Startup".
	ProbeType string
	Message   string
}

func (e ProbeFailureEvent) Type() EventType       { return EventTypeProbeFailure }
func (e ProbeFailureEvent) OccurredAt() time.Time { return e.At }
func (e ProbeFailureEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.ProbeType
}

// StalledRolloutEvent is emitted when an apps/v1 Deployment rollout fails to make
// forward progress within its configured progressDeadlineSeconds window.
//
// The kubelet marks this state by setting a Progressing condition with
// Status=False and Reason=ProgressDeadlineExceeded on the Deployment.
//
// DeploymentName is also stored in BaseEvent.PodName so the correlator can use
// the same resource-key routing as other event types without a separate code path.
type StalledRolloutEvent struct {
	BaseEvent
	// DeploymentName is the name of the stalled Deployment.
	DeploymentName string
	// Revision is the status.observedGeneration at the time of detection.
	// It is included in the dedup key so that a new rollout attempt that
	// also stalls produces a fresh event.
	Revision int64
	// DesiredReplicas is the replica count requested in spec.replicas (default 1).
	DesiredReplicas int32
	// ReadyReplicas is the number of replicas that are currently Ready.
	ReadyReplicas int32
	// Reason is always "ProgressDeadlineExceeded" for Phase-1 detection.
	Reason string
	// Message is the human-readable detail from the Progressing condition.
	Message string
}

func (e StalledRolloutEvent) Type() EventType       { return EventTypeStalledRollout }
func (e StalledRolloutEvent) OccurredAt() time.Time { return e.At }
func (e StalledRolloutEvent) DedupKey() string {
	// Include DeploymentName and Revision so a re-deployed (new generation)
	// that stalls again emits a separate, distinct event.
	return string(e.Type()) + ":" + e.Namespace + ":" + e.DeploymentName
}

// NodePressureEvent is emitted when a Node enters a resource-pressure condition:
// DiskPressure, MemoryPressure, or PIDPressure.
//
// Sourced from corev1.Node status conditions by node_watcher.go (which watches the
// Node object directly, so it fires even when the kubelet does not produce a K8s Event).
//
// PressureType contains the human-readable condition name — "DiskPressure",
// "MemoryPressure", or "PIDPressure" — matching corev1.NodeConditionType.
type NodePressureEvent struct {
	BaseEvent
	// PressureType is one of: "DiskPressure", "MemoryPressure", "PIDPressure".
	PressureType string
	// Message is the kubelet-provided detail from the condition (may be empty).
	Message string
}

func (e NodePressureEvent) Type() EventType       { return EventTypeNodePressure }
func (e NodePressureEvent) OccurredAt() time.Time { return e.At }
func (e NodePressureEvent) DedupKey() string {
	// Keyed on NodeName + PressureType so each distinct pressure type on the
	// same node can fire independently.
	return string(e.Type()) + ":" + e.NodeName + ":" + e.PressureType
}

// CPUThrottlingEvent is emitted when the kubelet fires a CPUThrottlingHigh warning
// for a container, indicating that the container is being throttled by the CPU CFS
// scheduler due to a cpu-limit being set in the pod spec.
//
// Sourced from the core/v1 Event stream (reason: CPUThrottlingHigh) by event_watcher.go.
type CPUThrottlingEvent struct {
	BaseEvent
	// ContainerName is parsed from InvolvedObject.FieldPath ("spec.containers{name}").
	ContainerName string
	// Message is the full kubelet event message, e.g. "25% throttling of CPU in namespace ...".
	Message string
}

func (e CPUThrottlingEvent) Type() EventType       { return EventTypeCPUThrottling }
func (e CPUThrottlingEvent) OccurredAt() time.Time { return e.At }
func (e CPUThrottlingEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.PodName + ":" + e.ContainerName
}
