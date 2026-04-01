// Package signals implements the explicit signal processing pipeline:
// Normalize → Enrich → Deduplicate.
package signals

import (
	"fmt"
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
func DefaultMappings() []SignalMapping {
	return []SignalMapping{
		{EventType: "CrashLoopBackOff", IncidentType: "CrashLoop", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "OOMKilled", IncidentType: "OOM", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "ImagePullBackOff", IncidentType: "Registry", Severity: "P3", ScopeLevel: "Workload"},
		{EventType: "PodPendingTooLong", IncidentType: "BadDeploy", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "GracePeriodViolation", IncidentType: "GracePeriodViolation", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "NodeNotReady", IncidentType: "NodeFailure", Severity: "P1", ScopeLevel: "Cluster"},
		{EventType: "PodEvicted", IncidentType: "NodeFailure", Severity: "P2", ScopeLevel: "Pod"},
		{EventType: "ProbeFailure", IncidentType: "ProbeFailure", Severity: "P3", ScopeLevel: "Pod"},
		{EventType: "StalledRollout", IncidentType: "BadDeploy", Severity: "P2", ScopeLevel: "Workload"},
		{EventType: "NodePressure", IncidentType: "NodeFailure", Severity: "P2", ScopeLevel: "Cluster"},
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
	summary := buildSummary(event)

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

	// Handle StalledRollout special case: workload ref is the Deployment.
	if sr, ok := event.(watcher.StalledRolloutEvent); ok {
		ref := &rcav1alpha1.IncidentObjectRef{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  sr.Namespace,
			Name:       sr.DeploymentName,
		}
		input.Scope.WorkloadRef = ref
		input.Scope.ResourceRef = ref
		input.AffectedResources = []rcav1alpha1.AffectedResource{
			{APIVersion: "apps/v1", Kind: "Deployment", Namespace: sr.Namespace, Name: sr.DeploymentName},
		}
	}

	return input
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
	default:
		return ""
	}
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
