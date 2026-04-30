package watcher

import (
	"maps"
	"time"
)

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

	// StatefulSet-sourced signals (detected from apps/v1 StatefulSet objects).
	EventTypeStalledStatefulSet EventType = "StalledStatefulSet"

	// DaemonSet-sourced signals (detected from apps/v1 DaemonSet objects).
	EventTypeStalledDaemonSet EventType = "StalledDaemonSet"

	// Job-sourced signals (detected from batch/v1 Job objects).
	EventTypeJobFailed EventType = "JobFailed"

	// CronJob-sourced signals (detected from batch/v1 CronJob objects).
	EventTypeCronJobFailed EventType = "CronJobFailed"

	// OTel-sourced signals (ingested via internal/otelingest from the cluster-wide
	// OTel Collector DaemonSet). These allow the correlator to reason about
	// user-workload traces and logs alongside Kubernetes control-plane events.
	EventTypeOTelSpanError        EventType = "OTelSpanError"
	EventTypeOTelSpanLatencySpike EventType = "OTelSpanLatencySpike"
	EventTypeOTelLogMatch         EventType = "OTelLogMatch"
	EventTypeOTelSpanEvent        EventType = "OTelSpanEvent"
)

var validEventTypes = map[EventType]struct{}{
	EventTypeCrashLoopBackOff:     {},
	EventTypeOOMKilled:            {},
	EventTypeImagePullBackOff:     {},
	EventTypePodPendingTooLong:    {},
	EventTypeGracePeriodViolation: {},
	EventTypePodHealthy:           {},
	EventTypePodDeleted:           {},
	EventTypeNodeNotReady:         {},
	EventTypePodEvicted:           {},
	EventTypeProbeFailure:         {},
	EventTypeStalledRollout:       {},
	EventTypeNodePressure:         {},
	EventTypeStalledStatefulSet:   {},
	EventTypeStalledDaemonSet:     {},
	EventTypeJobFailed:            {},
	EventTypeCronJobFailed:        {},
	EventTypeOTelSpanError:        {},
	EventTypeOTelSpanLatencySpike: {},
	EventTypeOTelLogMatch:         {},
	EventTypeOTelSpanEvent:        {},
}

// IsKnownEventType reports whether name is one of the signal types emitted by
// the watcher or OTLP ingest pipeline.
func IsKnownEventType(name string) bool {
	_, ok := validEventTypes[EventType(name)]
	return ok
}

// AttributesEvent is an optional interface implemented by events that carry
// key/value attributes (notably OTel span and log signals). The CRD rule engine
// uses this for attribute-level condition matching without needing type-switches.
//
// Events that do not implement this interface are treated as having no attributes,
// preserving backward compatibility with existing K8s-event-only rules.
type AttributesEvent interface {
	Attributes() map[string]string
}

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

// StalledStatefulSetEvent is emitted when an apps/v1 StatefulSet's rolling update
// stalls — detected when UpdateRevision != CurrentRevision and no pods have been
// updated for longer than the scan interval.
type StalledStatefulSetEvent struct {
	BaseEvent
	StatefulSetName string
	Revision        int64
	DesiredReplicas int32
	ReadyReplicas   int32
	UpdatedReplicas int32
	Reason          string
	Message         string
}

func (e StalledStatefulSetEvent) Type() EventType       { return EventTypeStalledStatefulSet }
func (e StalledStatefulSetEvent) OccurredAt() time.Time { return e.At }
func (e StalledStatefulSetEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.StatefulSetName
}

// StalledDaemonSetEvent is emitted when an apps/v1 DaemonSet has fewer ready pods
// than desired for an extended period, indicating a stalled rollout or scheduling failure.
type StalledDaemonSetEvent struct {
	BaseEvent
	DaemonSetName          string
	Revision               int64
	DesiredNumberScheduled int32
	NumberReady            int32
	UpdatedNumberScheduled int32
	Reason                 string
	Message                string
}

func (e StalledDaemonSetEvent) Type() EventType       { return EventTypeStalledDaemonSet }
func (e StalledDaemonSetEvent) OccurredAt() time.Time { return e.At }
func (e StalledDaemonSetEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.DaemonSetName
}

// JobFailedEvent is emitted when a batch/v1 Job reaches a Failed condition,
// typically due to BackoffLimitExceeded or DeadlineExceeded.
type JobFailedEvent struct {
	BaseEvent
	JobName string
	Reason  string
	Message string
}

func (e JobFailedEvent) Type() EventType       { return EventTypeJobFailed }
func (e JobFailedEvent) OccurredAt() time.Time { return e.At }
func (e JobFailedEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.JobName
}

// CronJobFailedEvent is emitted when a batch/v1 CronJob's most recent child Job
// has failed, indicating a broken scheduled task.
type CronJobFailedEvent struct {
	BaseEvent
	CronJobName string
	LastJobName string
	Reason      string
	Message     string
}

func (e CronJobFailedEvent) Type() EventType       { return EventTypeCronJobFailed }
func (e CronJobFailedEvent) OccurredAt() time.Time { return e.At }
func (e CronJobFailedEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.Namespace + ":" + e.CronJobName
}

// OTelSpanErrorEvent is emitted when the operator ingests a span whose status
// is ERROR (or that carries a 5xx HTTP/gRPC status attribute). The span may
// originate from any auto-instrumented workload in the cluster.
//
// BaseEvent fields are populated from k8s.* resource attributes applied by the
// OTel Collector's k8sattributes processor: Namespace = k8s.namespace.name,
// PodName = k8s.pod.name, NodeName = k8s.node.name.
type OTelSpanErrorEvent struct {
	BaseEvent
	TraceID       string
	SpanID        string
	ParentSpanID  string
	ServiceName   string
	SpanName      string
	SpanKind      string // CLIENT | SERVER | INTERNAL | PRODUCER | CONSUMER
	StatusCode    string // STATUS_CODE_ERROR | STATUS_CODE_OK | STATUS_CODE_UNSET
	StatusMessage string
	DurationNanos int64
	StartTime     time.Time
	EndTime       time.Time
	Attrs         map[string]string
	ResourceAttrs map[string]string
}

func (e OTelSpanErrorEvent) Type() EventType       { return EventTypeOTelSpanError }
func (e OTelSpanErrorEvent) OccurredAt() time.Time { return e.At }
func (e OTelSpanErrorEvent) DedupKey() string {
	// Span-level dedup: a given SpanID within a TraceID is unique.
	return string(e.Type()) + ":" + e.TraceID + ":" + e.SpanID
}
func (e OTelSpanErrorEvent) Attributes() map[string]string {
	m := mergedAttrs(e.Attrs, e.ResourceAttrs, e)
	promoteTraceIDs(m, e.TraceID, e.SpanID, e.ParentSpanID)
	return m
}

// OTelSpanLatencySpikeEvent is emitted when a span's duration exceeds the
// configured latency threshold (see Helm otelIngest.filters.traces.latencyP99Ms).
type OTelSpanLatencySpikeEvent struct {
	BaseEvent
	TraceID       string
	SpanID        string
	ParentSpanID  string
	ServiceName   string
	SpanName      string
	DurationNanos int64
	ThresholdNs   int64
	StartTime     time.Time
	EndTime       time.Time
	Attrs         map[string]string
	ResourceAttrs map[string]string
}

func (e OTelSpanLatencySpikeEvent) Type() EventType       { return EventTypeOTelSpanLatencySpike }
func (e OTelSpanLatencySpikeEvent) OccurredAt() time.Time { return e.At }
func (e OTelSpanLatencySpikeEvent) DedupKey() string {
	return string(e.Type()) + ":" + e.TraceID + ":" + e.SpanID
}
func (e OTelSpanLatencySpikeEvent) Attributes() map[string]string {
	m := mergedAttrs(e.Attrs, e.ResourceAttrs, baseAttrHolder{ServiceName: e.ServiceName, SpanName: e.SpanName})
	promoteTraceIDs(m, e.TraceID, e.SpanID, e.ParentSpanID)
	return m
}

// OTelLogMatchEvent is emitted when the operator ingests a log record whose
// severity is WARN or above (configurable via otelIngest.filters.logs.minSeverity).
//
// BodyHash is a hash of the normalized log body used for dedup within the
// sliding buffer (identical log lines from the same service collapse to one entry).
type OTelLogMatchEvent struct {
	BaseEvent
	TraceID       string
	SpanID        string
	ServiceName   string
	Severity      string // OTel severity text: TRACE | DEBUG | INFO | WARN | ERROR | FATAL
	SeverityNum   int32  // OTel severity number (1-24)
	Body          string // Redacted body text (PII already stripped at ingest time).
	BodyHash      string // Hash of the redacted body; used for dedup.
	Attrs         map[string]string
	ResourceAttrs map[string]string
}

func (e OTelLogMatchEvent) Type() EventType       { return EventTypeOTelLogMatch }
func (e OTelLogMatchEvent) OccurredAt() time.Time { return e.At }
func (e OTelLogMatchEvent) DedupKey() string {
	// Log dedup: same service + same body hash collapses within the buffer window.
	return string(e.Type()) + ":" + e.ServiceName + ":" + e.BodyHash
}
func (e OTelLogMatchEvent) Attributes() map[string]string {
	m := mergedAttrs(e.Attrs, e.ResourceAttrs, baseAttrHolder{ServiceName: e.ServiceName, Severity: e.Severity, Body: e.Body})
	promoteTraceIDs(m, e.TraceID, e.SpanID, "")
	return m
}

// OTelSpanEventEvent is emitted when a span carries a standalone OTel event
// record (e.g. exception.stacktrace, log.message). These are attached to spans
// but evaluated independently by the correlator because an exception event can
// be the strongest root-cause signal even on an otherwise-OK span.
type OTelSpanEventEvent struct {
	BaseEvent
	TraceID       string
	SpanID        string
	ServiceName   string
	EventName     string // e.g. "exception", "log"
	EventTime     time.Time
	Attrs         map[string]string
	ResourceAttrs map[string]string
}

func (e OTelSpanEventEvent) Type() EventType       { return EventTypeOTelSpanEvent }
func (e OTelSpanEventEvent) OccurredAt() time.Time { return e.At }
func (e OTelSpanEventEvent) DedupKey() string {
	// Include EventName so multiple distinct events on one span do not collapse.
	return string(e.Type()) + ":" + e.TraceID + ":" + e.SpanID + ":" + e.EventName
}
func (e OTelSpanEventEvent) Attributes() map[string]string {
	m := mergedAttrs(e.Attrs, e.ResourceAttrs, baseAttrHolder{ServiceName: e.ServiceName, EventName: e.EventName})
	promoteTraceIDs(m, e.TraceID, e.SpanID, "")
	return m
}

// baseAttrHolder is a tiny struct used only to promote OTel event struct
// fields into the flat Attributes() map for rule-engine matching.
type baseAttrHolder struct {
	ServiceName string
	SpanName    string
	Severity    string
	Body        string
	EventName   string
}

// mergedAttrs flattens span/log attributes, resource attributes, and struct-level
// fields into a single map keyed for CRD rule condition matching. Struct-level
// fields take precedence over attrs/resourceAttrs on key collisions to guarantee
// that well-known keys like "service.name" always reflect the event's promoted field.
func mergedAttrs(attrs, resourceAttrs map[string]string, self any) map[string]string {
	out := make(map[string]string, len(attrs)+len(resourceAttrs)+8)
	maps.Copy(out, resourceAttrs)
	maps.Copy(out, attrs)
	switch s := self.(type) {
	case OTelSpanErrorEvent:
		if s.ServiceName != "" {
			out["service.name"] = s.ServiceName
		}
		if s.SpanName != "" {
			out["span.name"] = s.SpanName
		}
		if s.SpanKind != "" {
			out["span.kind"] = s.SpanKind
		}
		if s.StatusCode != "" {
			out["span.status.code"] = s.StatusCode
		}
		if s.StatusMessage != "" {
			out["span.status.message"] = s.StatusMessage
		}
	case baseAttrHolder:
		if s.ServiceName != "" {
			out["service.name"] = s.ServiceName
		}
		if s.SpanName != "" {
			out["span.name"] = s.SpanName
		}
		if s.Severity != "" {
			out["log.severity"] = s.Severity
		}
		if s.Body != "" {
			out["log.body"] = s.Body
		}
		if s.EventName != "" {
			out["event.name"] = s.EventName
		}
	}
	return out
}

// promoteTraceIDs writes the span's OTel identifiers into the flattened
// attribute map under stable keys ("trace.id", "span.id", "parent.span.id")
// so CRD rules using sameTrace scope or attribute-match predicates can key off
// them without each rule author needing to know which struct field carries
// the identifier. Empty values are not written so rules can use the Exists
// predicate to detect missing IDs.
func promoteTraceIDs(out map[string]string, traceID, spanID, parentSpanID string) {
	if traceID != "" {
		out["trace.id"] = traceID
	}
	if spanID != "" {
		out["span.id"] = spanID
	}
	if parentSpanID != "" {
		out["parent.span.id"] = parentSpanID
	}
}
