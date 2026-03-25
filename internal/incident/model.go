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
}

func (in Input) Fingerprint() string {
	scope := in.Scope
	parts := []string{in.IncidentType}

	switch scope.Level {
	case ScopeLevelCluster:
		if scope.ResourceRef != nil {
			parts = append(parts, strings.ToLower(scope.ResourceRef.Kind), scope.ResourceRef.Name)
		}
	case ScopeLevelWorkload:
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
		if scope.WorkloadRef != nil {
			parts = append(parts, strings.ToLower(scope.WorkloadRef.Kind), scope.WorkloadRef.Name)
		}
	case ScopeLevelNamespace:
		if scope.Namespace != "" {
			parts = append(parts, scope.Namespace)
		}
	case ScopeLevelPod:
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

	return strings.Join(parts, "|")
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
