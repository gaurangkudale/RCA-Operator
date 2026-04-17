package incident

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

const (
	ScopeLevelCluster   = "Cluster"
	ScopeLevelNamespace = "Namespace"
	ScopeLevelWorkload  = "Workload"
	ScopeLevelPod       = "Pod"
)

type Input struct {
	Namespace         string
	AgentRef          string
	IncidentType      string
	Severity          string
	Summary           string
	Reason            string
	Message           string
	DedupKey          string
	ObservedAt        time.Time
	Scope             rcav1alpha1.IncidentScope
	AffectedResources []rcav1alpha1.AffectedResource
	// TraceID is the W3C trace-id hex string from an OTel-sourced event, when
	// available. It is persisted as an IncidentReport annotation so operators
	// can correlate an incident back to the originating distributed trace.
	TraceID string
	// FiredRule is the name of the correlation rule that produced the incident
	// classification, when a correlator rule fired. Empty for single-signal
	// incidents. Persisted as an IncidentReport annotation.
	FiredRule string
}

// Fingerprint returns a stable canonical identity for the incident based on its
// scope.
//
// For Kubernetes-native incident types, fingerprinting remains scope-based so
// related controller/platform symptoms on the same resource can coalesce.
//
// For OTel incident types, IncidentType is included so service-runtime errors
// (span errors, log matches, latency spikes) are not collapsed into unrelated
// Kubernetes lifecycle incidents for the same workload.
func (in Input) Fingerprint() string {
	scope := in.Scope
	var parts []string

	switch scope.Level {
	case ScopeLevelCluster:
		parts = append(parts, ScopeLevelCluster)
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	case ScopeLevelWorkload:
		parts = append(parts, ScopeLevelWorkload)
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
		if scope.WorkloadRef != nil {
			parts = append(parts, strings.ToLower(scope.WorkloadRef.Kind), scope.WorkloadRef.Name)
		}
	case ScopeLevelNamespace:
		parts = append(parts, ScopeLevelNamespace)
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
	case ScopeLevelPod:
		parts = append(parts, ScopeLevelPod)
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	default:
		if in.Namespace != "" {
			parts = append(parts, in.Namespace)
		}
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	}

	if IsOTelIncidentType(in.IncidentType) {
		parts = append(parts, "type", in.IncidentType)
	}

	return strings.Join(parts, "|")
}

// IsOTelIncidentType returns true when the incident type originates from
// telemetry ingest (spans/logs/span-events) rather than Kubernetes watchers.
func IsOTelIncidentType(incidentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(incidentType), "OTel")
}

func FingerprintHash(fingerprint string) string {
	sum := sha1.Sum([]byte(fingerprint))
	return hex.EncodeToString(sum[:6])
}

func SummaryFromParts(incidentType, reason, message string) string {
	switch {
	case reason != "" && message != "":
		return fmt.Sprintf("%s reason=%s message=%s", incidentType, reason, message)
	case reason != "":
		return fmt.Sprintf("%s reason=%s", incidentType, reason)
	case message != "":
		return fmt.Sprintf("%s message=%s", incidentType, message)
	default:
		return incidentType
	}
}
