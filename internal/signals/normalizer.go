// Package signals implements the explicit signal processing pipeline:
// Normalize → Enrich → Deduplicate.
package signals

import (
	"fmt"
	"regexp"
	"strings"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// SignalMapping defines a configurable event-type → incident-type mapping.
// The default table can be overridden per-agent via RCAAgent.spec.signalMappings.
type SignalMapping struct {
	EventType    string
	IncidentType string
	Severity     string
	ScopeLevel   string // Pod, Workload, Namespace, Cluster
}

// DefaultMappings returns the built-in event→incident type mappings.
// IncidentType mirrors the raw EventType so incident classes remain
// self-describing rather than hardcoded aliases.
func DefaultMappings() []SignalMapping {
	return []SignalMapping{
		{EventType: "CrashLoopBackOff", IncidentType: "CrashLoopBackOff", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "OOMKilled", IncidentType: "OOMKilled", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "ImagePullBackOff", IncidentType: "ImagePullBackOff", Severity: "P3", ScopeLevel: "Workload"},
		{EventType: "PodPendingTooLong", IncidentType: "PodPendingTooLong", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "GracePeriodViolation", IncidentType: "GracePeriodViolation", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "NodeNotReady", IncidentType: "NodeNotReady", Severity: "P1", ScopeLevel: "Cluster"},
		{EventType: "PodEvicted", IncidentType: "PodEvicted", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "ProbeFailure", IncidentType: "ProbeFailure", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "StalledRollout", IncidentType: "StalledRollout", Severity: "P2", ScopeLevel: "Workload"},
		{EventType: "NodePressure", IncidentType: "NodePressure", Severity: "P2", ScopeLevel: "Cluster"},
		{EventType: "StalledStatefulSet", IncidentType: "StalledStatefulSet", Severity: "P2", ScopeLevel: "Workload"},
		{EventType: "StalledDaemonSet", IncidentType: "StalledDaemonSet", Severity: "P2", ScopeLevel: "Workload"},
		{EventType: "JobFailed", IncidentType: "JobFailed", Severity: "P3", ScopeLevel: "Workload"},
		{EventType: "CronJobFailed", IncidentType: "CronJobFailed", Severity: "P3", ScopeLevel: "Workload"},
		// OTel-sourced signals. ScopeLevel defaults to Workload because spans
		// carry service identity; the Enricher promotes pod→deployment scope
		// when k8s.pod.name resource attributes are populated by the
		// collector's k8sattributes processor.
		{EventType: "OTelSpanError", IncidentType: "OTelSpanError", Severity: "P3", ScopeLevel: "Workload"},
		{EventType: "OTelSpanLatencySpike", IncidentType: "OTelSpanLatencySpike", Severity: "P4", ScopeLevel: "Workload"},
		{EventType: "OTelLogMatch", IncidentType: "OTelLogMatch", Severity: "P4", ScopeLevel: "Workload"},
		{EventType: "OTelSpanEvent", IncidentType: "OTelSpanEvent", Severity: "P3", ScopeLevel: "Workload"},
	}
}

// NormalizedSignal is the output of the Normalizer stage.
type NormalizedSignal struct {
	incident.Input
	RawEvent watcher.CorrelatorEvent
}

// Normalizer maps raw watcher events into NormalizedSignal using a configurable mapping table.
type Normalizer struct {
	mappings map[string]SignalMapping
}

// NewNormalizer creates a Normalizer from the given mappings. If empty, defaults are used.
func NewNormalizer(overrides []SignalMapping) *Normalizer {
	n := &Normalizer{mappings: make(map[string]SignalMapping)}
	for _, m := range DefaultMappings() {
		n.mappings[m.EventType] = m
	}
	for _, m := range overrides {
		n.mappings[m.EventType] = m
	}
	return n
}

// Normalize converts a raw watcher event into a NormalizedSignal.
// Returns ok=false for unrecognised events or lifecycle events (PodHealthy, PodDeleted)
// which are handled separately by the consumer.
func (n *Normalizer) Normalize(event watcher.CorrelatorEvent) (NormalizedSignal, bool) {
	eventType := string(event.Type())

	// Lifecycle events are not normalised into incidents — the consumer handles them directly.
	if eventType == string(watcher.EventTypePodHealthy) || eventType == string(watcher.EventTypePodDeleted) {
		return NormalizedSignal{}, false
	}

	mapping, ok := n.mappings[eventType]
	if !ok {
		return NormalizedSignal{}, false
	}

	input := n.buildInput(event, mapping)
	return NormalizedSignal{Input: input, RawEvent: event}, true
}

func (n *Normalizer) buildInput(event watcher.CorrelatorEvent, mapping SignalMapping) incident.Input {
	base := extractBase(event)
	summary := stripEmptyKV(buildSummary(event))

	input := incident.Input{
		Namespace:    base.Namespace,
		AgentRef:     base.AgentName,
		IncidentType: mapping.IncidentType,
		Severity:     mapping.Severity,
		Summary:      summary,
		Reason:       extractReason(event),
		Message:      summary,
		DedupKey:     event.DedupKey(),
		ObservedAt:   event.OccurredAt(),
		TraceID:      extractTraceID(event),
	}

	// Apply special severity override for NodePressure/PIDPressure.
	if np, ok := event.(watcher.NodePressureEvent); ok && np.PressureType == "PIDPressure" {
		input.Severity = "P3"
	}

	// Scope defaults based on mapping.
	// Pod-originated events start at pod scope so the Enricher can resolve owner refs.
	// Workload events without a pod name (StalledRollout) go directly to workload scope.
	switch mapping.ScopeLevel {
	case "Cluster":
		input.Scope = rcav1alpha1.IncidentScope{
			Level: incident.ScopeLevelCluster,
			ResourceRef: &rcav1alpha1.IncidentObjectRef{
				APIVersion: "v1",
				Kind:       "Node",
				Name:       base.NodeName,
			},
		}
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "v1", Kind: "Node", Name: base.NodeName},
		}
	case "Workload":
		if base.PodName != "" {
			// Pod-originated workload event (e.g. ImagePullBackOff) — start at pod scope,
			// the Enricher will resolve owner refs and promote.
			input.Scope = rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelPod,
				Namespace: base.Namespace,
				ResourceRef: &rcav1alpha1.IncidentObjectRef{
					APIVersion: "v1",
					Kind:       "Pod",
					Namespace:  base.Namespace,
					Name:       base.PodName,
				},
			}
		} else {
			// Deployment-originated workload event (e.g. StalledRollout).
			input.Scope = rcav1alpha1.IncidentScope{
				Level:     incident.ScopeLevelWorkload,
				Namespace: base.Namespace,
			}
		}
	default:
		// Pod-scoped events.
		input.Scope = rcav1alpha1.IncidentScope{
			Level:     incident.ScopeLevelPod,
			Namespace: base.Namespace,
			ResourceRef: &rcav1alpha1.IncidentObjectRef{
				APIVersion: "v1",
				Kind:       "Pod",
				Namespace:  base.Namespace,
				Name:       base.PodName,
			},
		}
	}

	// Handle workload-level events: override scope to Workload so the fingerprint
	// uses workload identity instead of pod identity.
	switch ev := event.(type) {
	case watcher.StalledRolloutEvent:
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  ev.Namespace,
			Name:       ev.DeploymentName,
		}
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = ev.Namespace
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "apps/v1", Kind: "Deployment", Namespace: ev.Namespace, Name: ev.DeploymentName},
		}
	case watcher.StalledStatefulSetEvent:
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
			Namespace:  ev.Namespace,
			Name:       ev.StatefulSetName,
		}
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = ev.Namespace
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: ev.Namespace, Name: ev.StatefulSetName},
		}
	case watcher.StalledDaemonSetEvent:
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "apps/v1",
			Kind:       "DaemonSet",
			Namespace:  ev.Namespace,
			Name:       ev.DaemonSetName,
		}
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = ev.Namespace
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: ev.Namespace, Name: ev.DaemonSetName},
		}
	case watcher.JobFailedEvent:
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "batch/v1",
			Kind:       "Job",
			Namespace:  ev.Namespace,
			Name:       ev.JobName,
		}
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = ev.Namespace
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "batch/v1", Kind: "Job", Namespace: ev.Namespace, Name: ev.JobName},
		}
	case watcher.CronJobFailedEvent:
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "batch/v1",
			Kind:       "CronJob",
			Namespace:  ev.Namespace,
			Name:       ev.CronJobName,
		}
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = ev.Namespace
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "batch/v1", Kind: "CronJob", Namespace: ev.Namespace, Name: ev.CronJobName},
		}
	case watcher.OTelSpanErrorEvent, watcher.OTelSpanLatencySpikeEvent, watcher.OTelLogMatchEvent, watcher.OTelSpanEventEvent:
		applyOTelScopeOverrides(&input, event)
	}

	return input
}

// applyOTelScopeOverrides upgrades OTel signals to a stable workload identity
// when pod metadata is unavailable. Priority:
//  1. k8s workload attrs (deployment/statefulset/daemonset/job/cronjob)
//  2. service identity (service.name + namespace)
//
// This ensures trace/log-derived incidents are still created and grouped by
// service/workload instead of being dropped due missing pod scope.
func applyOTelScopeOverrides(input *incident.Input, event watcher.CorrelatorEvent) {
	serviceName, resourceAttrs, ok := extractOTelResourceIdentity(event)
	if !ok {
		return
	}

	namespace := firstNonEmpty(
		input.Namespace,
		resourceAttrs["k8s.namespace.name"],
		resourceAttrs["service.namespace"],
	)
	if namespace != "" {
		input.Namespace = namespace
		input.Scope.Namespace = namespace
	}

	if ref, affected, hasWorkload := workloadFromOTelResourceAttrs(namespace, resourceAttrs); hasWorkload {
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{affected}
		return
	}

	serviceName = firstNonEmpty(serviceName, resourceAttrs["service.name"])
	if serviceName == "" || namespace == "" {
		return
	}

	ref := &rcav1alpha1.IncidentObjectRef{
		APIVersion: "v1",
		Kind:       "Service",
		Namespace:  namespace,
		Name:       serviceName,
	}
	input.Scope.Level = incident.ScopeLevelWorkload
	input.Scope.WorkloadRef = ref
	input.Scope.ResourceRef = ref
	input.AffectedResources = []rcav1alpha1.AffectedResource{
		{APIVersion: "v1", Kind: "Service", Namespace: namespace, Name: serviceName},
	}
}

func extractOTelResourceIdentity(event watcher.CorrelatorEvent) (serviceName string, resourceAttrs map[string]string, ok bool) {
	switch e := event.(type) {
	case watcher.OTelSpanErrorEvent:
		return e.ServiceName, e.ResourceAttrs, true
	case watcher.OTelSpanLatencySpikeEvent:
		return e.ServiceName, e.ResourceAttrs, true
	case watcher.OTelLogMatchEvent:
		return e.ServiceName, e.ResourceAttrs, true
	case watcher.OTelSpanEventEvent:
		return e.ServiceName, e.ResourceAttrs, true
	default:
		return "", nil, false
	}
}

func workloadFromOTelResourceAttrs(namespace string, attrs map[string]string) (*rcav1alpha1.IncidentObjectRef, rcav1alpha1.AffectedResource, bool) {
	type workloadKey struct {
		attrKey    string
		apiVersion string
		kind       string
	}

	candidates := []workloadKey{
		{attrKey: "k8s.deployment.name", apiVersion: "apps/v1", kind: "Deployment"},
		{attrKey: "k8s.statefulset.name", apiVersion: "apps/v1", kind: "StatefulSet"},
		{attrKey: "k8s.daemonset.name", apiVersion: "apps/v1", kind: "DaemonSet"},
		{attrKey: "k8s.job.name", apiVersion: "batch/v1", kind: "Job"},
		{attrKey: "k8s.cronjob.name", apiVersion: "batch/v1", kind: "CronJob"},
	}

	for _, candidate := range candidates {
		name := strings.TrimSpace(attrs[candidate.attrKey])
		if name == "" || namespace == "" {
			continue
		}
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: candidate.apiVersion,
			Kind:       candidate.kind,
			Namespace:  namespace,
			Name:       name,
		}
		return ref, rcav1alpha1.AffectedResource{
			APIVersion: candidate.apiVersion,
			Kind:       candidate.kind,
			Namespace:  namespace,
			Name:       name,
		}, true
	}

	return nil, rcav1alpha1.AffectedResource{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func extractBase(event watcher.CorrelatorEvent) watcher.BaseEvent {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.BaseEvent
	case watcher.OOMKilledEvent:
		return e.BaseEvent
	case watcher.ImagePullBackOffEvent:
		return e.BaseEvent
	case watcher.PodPendingTooLongEvent:
		return e.BaseEvent
	case watcher.GracePeriodViolationEvent:
		return e.BaseEvent
	case watcher.NodeNotReadyEvent:
		return e.BaseEvent
	case watcher.PodEvictedEvent:
		return e.BaseEvent
	case watcher.ProbeFailureEvent:
		return e.BaseEvent
	case watcher.StalledRolloutEvent:
		return e.BaseEvent
	case watcher.NodePressureEvent:
		return e.BaseEvent
	case watcher.StalledStatefulSetEvent:
		return e.BaseEvent
	case watcher.StalledDaemonSetEvent:
		return e.BaseEvent
	case watcher.JobFailedEvent:
		return e.BaseEvent
	case watcher.CronJobFailedEvent:
		return e.BaseEvent
	case watcher.OTelSpanErrorEvent:
		return e.BaseEvent
	case watcher.OTelSpanLatencySpikeEvent:
		return e.BaseEvent
	case watcher.OTelLogMatchEvent:
		return e.BaseEvent
	case watcher.OTelSpanEventEvent:
		return e.BaseEvent
	default:
		return watcher.BaseEvent{}
	}
}

func extractReason(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return "CrashLoopBackOff"
	case watcher.OOMKilledEvent:
		return e.Reason
	case watcher.ImagePullBackOffEvent:
		return e.Reason
	case watcher.PodPendingTooLongEvent:
		return "PendingTooLong"
	case watcher.GracePeriodViolationEvent:
		return "GracePeriodExceeded"
	case watcher.NodeNotReadyEvent:
		return e.Reason
	case watcher.PodEvictedEvent:
		return e.Reason
	case watcher.ProbeFailureEvent:
		return e.ProbeType
	case watcher.StalledRolloutEvent:
		return e.Reason
	case watcher.NodePressureEvent:
		return e.PressureType
	case watcher.StalledStatefulSetEvent:
		return e.Reason
	case watcher.StalledDaemonSetEvent:
		return e.Reason
	case watcher.JobFailedEvent:
		return e.Reason
	case watcher.CronJobFailedEvent:
		return e.Reason
	case watcher.OTelSpanErrorEvent:
		return e.StatusCode
	case watcher.OTelSpanLatencySpikeEvent:
		return "LatencySpike"
	case watcher.OTelLogMatchEvent:
		return e.Severity
	case watcher.OTelSpanEventEvent:
		return e.EventName
	default:
		return ""
	}
}

// extractTraceID returns the W3C trace-id carried by OTel-sourced events, or
// an empty string for K8s-event-sourced signals that have no trace context.
func extractTraceID(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.OTelSpanErrorEvent:
		return e.TraceID
	case watcher.OTelSpanLatencySpikeEvent:
		return e.TraceID
	case watcher.OTelLogMatchEvent:
		return e.TraceID
	case watcher.OTelSpanEventEvent:
		return e.TraceID
	default:
		return ""
	}
}

// emptyKVRegex matches whitespace + key= whose value is absent (end of string or
// followed by whitespace). Used to strip trailing/mid-string `message=` etc.
var emptyKVRegex = regexp.MustCompile(`(^|\s)[A-Za-z_][\w.-]*=(\s|$)`)

// stripEmptyKV removes `key=` fragments with empty values from a summary string.
// Repeats until stable so adjacent empties collapse.
func stripEmptyKV(s string) string {
	for {
		out := emptyKVRegex.ReplaceAllString(s, "$2")
		if out == s {
			break
		}
		s = out
	}
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

func buildSummary(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		s := fmt.Sprintf("CrashLoopBackOff restarts=%d threshold=%d", e.RestartCount, e.Threshold)
		if e.LastExitCode != 0 && e.ExitCodeCategory != "" {
			s = fmt.Sprintf("%s exitCode=%d category=%s description=%s", s, e.LastExitCode, e.ExitCodeCategory, e.ExitCodeDescription)
		}
		return s
	case watcher.OOMKilledEvent:
		return fmt.Sprintf("OOMKilled exitCode=%d reason=%s", e.ExitCode, e.Reason)
	case watcher.ImagePullBackOffEvent:
		return fmt.Sprintf("Image pull failure reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodPendingTooLongEvent:
		return fmt.Sprintf("Pod pending too long pendingFor=%s timeout=%s", e.PendingFor.String(), e.Timeout.String())
	case watcher.GracePeriodViolationEvent:
		return fmt.Sprintf("Pod deletion exceeded grace period grace=%ds overdue=%s", e.GracePeriodSeconds, e.OverdueFor.String())
	case watcher.NodeNotReadyEvent:
		return fmt.Sprintf("Node not ready reason=%s message=%s", e.Reason, e.Message)
	case watcher.PodEvictedEvent:
		return fmt.Sprintf("Pod evicted from node reason=%s message=%s", e.Reason, e.Message)
	case watcher.ProbeFailureEvent:
		return fmt.Sprintf("Probe failed probeType=%s message=%s", e.ProbeType, e.Message)
	case watcher.StalledRolloutEvent:
		return fmt.Sprintf("Deployment rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d message=%s",
			e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.Message)
	case watcher.NodePressureEvent:
		return fmt.Sprintf("Node %s condition=%s message=%s", e.NodeName, e.PressureType, e.Message)
	case watcher.StalledStatefulSetEvent:
		return fmt.Sprintf("StatefulSet rollout stalled reason=%s desiredReplicas=%d readyReplicas=%d updatedReplicas=%d message=%s",
			e.Reason, e.DesiredReplicas, e.ReadyReplicas, e.UpdatedReplicas, e.Message)
	case watcher.StalledDaemonSetEvent:
		return fmt.Sprintf("DaemonSet rollout stalled reason=%s desired=%d ready=%d updated=%d message=%s",
			e.Reason, e.DesiredNumberScheduled, e.NumberReady, e.UpdatedNumberScheduled, e.Message)
	case watcher.JobFailedEvent:
		return fmt.Sprintf("Job failed reason=%s message=%s", e.Reason, e.Message)
	case watcher.CronJobFailedEvent:
		return fmt.Sprintf("CronJob failed lastJob=%s reason=%s message=%s", e.LastJobName, e.Reason, e.Message)
	case watcher.OTelSpanErrorEvent:
		return fmt.Sprintf("Error span service=%s span=%s status=%s message=%s",
			e.ServiceName, e.SpanName, e.StatusCode, e.StatusMessage)
	case watcher.OTelSpanLatencySpikeEvent:
		return fmt.Sprintf("Latency spike service=%s span=%s durationNs=%d thresholdNs=%d",
			e.ServiceName, e.SpanName, e.DurationNanos, e.ThresholdNs)
	case watcher.OTelLogMatchEvent:
		return fmt.Sprintf("Log match service=%s severity=%s body=%s",
			e.ServiceName, e.Severity, e.Body)
	case watcher.OTelSpanEventEvent:
		return fmt.Sprintf("Span event service=%s event=%s", e.ServiceName, e.EventName)
	default:
		return ""
	}
}

// GuessDeploymentNameFromPod attempts to extract a Deployment name from a pod name
// following the Kubernetes naming convention: <deploy>-<replicaset-hash>-<pod-hash>.
func GuessDeploymentNameFromPod(podName string) string {
	parts := strings.Split(strings.TrimSpace(podName), "-")
	if len(parts) < 2 {
		return ""
	}

	last := parts[len(parts)-1]
	if len(last) == 5 {
		if len(parts) >= 3 && looksLikeReplicaSetHash(parts[len(parts)-2]) {
			return strings.Join(parts[:len(parts)-2], "-")
		}
		return strings.Join(parts[:len(parts)-1], "-")
	}

	return ""
}

func looksLikeReplicaSetHash(token string) bool {
	if len(token) < 8 || len(token) > 10 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
