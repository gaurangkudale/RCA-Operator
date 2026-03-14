package correlator

import (
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

// Phase constants for the Incident lifecycle. These mirror the kubebuilder
// validation enum on IncidentReportStatus.Phase and are exported for use by
// other packages (e.g. notification pipelines, tests).
const (
	PhaseDetecting = "Detecting"
	PhaseActive    = "Active"
	PhaseResolved  = "Resolved"
)

// Incident is the in-memory representation of a detected reliability incident.
// It mirrors the fields persisted in an IncidentReport custom resource and is
// used internally by the correlator pipeline before a CR is written to the
// Kubernetes API server.
type Incident struct {
	// ID is the Kubernetes name assigned to the IncidentReport CR
	// (auto-generated via GenerateName).
	ID string

	// Phase tracks the current lifecycle state: Detecting → Active → Resolved.
	Phase string

	// Severity is the assessed impact level (P1–P4).
	Severity string

	// IncidentType categorises the root cause signal
	// (CrashLoop, OOM, BadDeploy, NodeFailure, Registry, etc.).
	IncidentType string

	// AffectedResources lists the Kubernetes resources implicated in this incident.
	AffectedResources []rcav1alpha1.AffectedResource

	// CorrelatedSignals is the ordered list of raw signal strings received so
	// far, capped at maxSignalEntries.
	CorrelatedSignals []string

	// Timeline is the ordered sequence of timestamped events for this incident,
	// capped at maxTimelineEntries.
	Timeline []rcav1alpha1.TimelineEvent

	// StartTime is when the first signal for this incident was observed.
	StartTime time.Time

	// ResolvedTime is set when the incident transitions to Resolved.
	// It is nil while the incident is still open.
	ResolvedTime *time.Time

	// AgentRef is the name of the RCAAgent CR that owns this incident.
	AgentRef string
}
