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
