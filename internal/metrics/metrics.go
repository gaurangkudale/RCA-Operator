// Package metrics holds the Prometheus collectors for the RCA Operator's
// incident lifecycle. Collectors are registered with controller-runtime's
// shared registry on init so anything imported by main.go reaches the
// /metrics endpoint without further wiring.
//
// All collectors follow the Phase 1 architecture spec:
//   - rca_signals_received_total       — counter of signals entering the pipeline
//   - rca_signals_deduplicated_total   — counter of signals suppressed by dedup
//   - rca_incidents_detecting_total    — counter of new incidents in Detecting phase
//   - rca_incidents_activated_total    — counter of Detecting → Active transitions
//   - rca_incidents_resolved_total     — counter of Active|Detecting → Resolved transitions
//   - rca_active_incidents             — gauge of currently non-Resolved incidents
//   - rca_incident_transition_seconds  — histogram of phase transition durations
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const namespace = "rca"

// labelUnknown is the placeholder used when a callsite supplies an empty
// label value, keeping label cardinality bounded for ad-hoc dashboards that
// reject empty strings.
const labelUnknown = "unknown"

var (
	// SignalsReceived counts every signal accepted by the correlator,
	// labelled by event type (e.g. CrashLoopBackOff, OTelSpanError).
	SignalsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "signals_received_total",
		Help:      "Total signals accepted by the correlator pipeline.",
	}, []string{"event_type"})

	// SignalsDeduplicated counts signals suppressed because an open
	// IncidentReport with the same fingerprint already absorbed them.
	SignalsDeduplicated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "signals_deduplicated_total",
		Help:      "Signals suppressed by IncidentReport deduplication.",
	}, []string{"event_type"})

	// IncidentsDetecting counts new IncidentReports created in the
	// Detecting phase (the entry point of the incident lifecycle).
	IncidentsDetecting = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "incidents_detecting_total",
		Help:      "IncidentReports that entered the Detecting phase.",
	}, []string{"incident_type", "severity"})

	// IncidentsActivated counts Detecting → Active transitions.
	IncidentsActivated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "incidents_activated_total",
		Help:      "IncidentReports promoted from Detecting to Active.",
	}, []string{"incident_type", "severity"})

	// IncidentsResolved counts transitions to the Resolved phase from any
	// upstream phase.
	IncidentsResolved = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "incidents_resolved_total",
		Help:      "IncidentReports that reached the Resolved phase.",
	}, []string{"incident_type", "severity"})

	// ActiveIncidents tracks the current number of non-Resolved
	// IncidentReports. Bumped on Detecting create, decremented on Resolve.
	// Reconciler bootstrap may also call SetActiveIncidents to align the
	// gauge with cluster reality after a restart.
	ActiveIncidents = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "active_incidents",
		Help:      "Currently non-Resolved IncidentReports.",
	}, []string{"incident_type", "severity"})

	// IncidentTransitionSeconds observes the wall-clock duration of phase
	// transitions, e.g. how long an incident sat in Detecting before being
	// promoted to Active. Buckets are tuned for typical Kubernetes incident
	// timescales (10s … 1h).
	IncidentTransitionSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "incident_transition_seconds",
		Help:      "Wall-clock duration of IncidentReport phase transitions.",
		Buckets:   []float64{10, 30, 60, 120, 300, 600, 1800, 3600},
	}, []string{"from_phase", "to_phase"})
)

func init() {
	crmetrics.Registry.MustRegister(
		SignalsReceived,
		SignalsDeduplicated,
		IncidentsDetecting,
		IncidentsActivated,
		IncidentsResolved,
		ActiveIncidents,
		IncidentTransitionSeconds,
	)
}

// RecordSignalReceived bumps the received counter for the given event type.
// Empty event types are coerced to "unknown" so the label cardinality stays
// bounded (Prometheus won't reject the empty string but ad-hoc dashboards
// often do).
func RecordSignalReceived(eventType string) {
	if eventType == "" {
		eventType = labelUnknown
	}
	SignalsReceived.WithLabelValues(eventType).Inc()
}

// RecordSignalDeduplicated bumps the dedup counter for the given event type.
func RecordSignalDeduplicated(eventType string) {
	if eventType == "" {
		eventType = labelUnknown
	}
	SignalsDeduplicated.WithLabelValues(eventType).Inc()
}

// RecordIncidentDetecting bumps the detecting counter and the active gauge
// for a newly-created IncidentReport.
func RecordIncidentDetecting(incidentType, severity string) {
	t, s := normLabels(incidentType, severity)
	IncidentsDetecting.WithLabelValues(t, s).Inc()
	ActiveIncidents.WithLabelValues(t, s).Inc()
}

// RecordIncidentActivated bumps the activated counter and observes the
// detecting→active duration when both timestamps are non-zero.
func RecordIncidentActivated(incidentType, severity string, transitionSeconds float64) {
	t, s := normLabels(incidentType, severity)
	IncidentsActivated.WithLabelValues(t, s).Inc()
	if transitionSeconds > 0 {
		IncidentTransitionSeconds.WithLabelValues("detecting", "active").Observe(transitionSeconds)
	}
}

// RecordIncidentResolved bumps the resolved counter, decrements the active
// gauge, and observes the from→resolved transition. fromPhase should be the
// phase the incident was in immediately before resolution (Detecting or
// Active). transitionSeconds may be 0 if no reference timestamp is known.
//
// The active gauge is decremented unconditionally; bootstrap reconcilers
// should call SetActiveIncidents to seed the gauge before the first
// transition fires, otherwise the gauge can briefly read negative on
// startup.
func RecordIncidentResolved(incidentType, severity, fromPhase string, transitionSeconds float64) {
	t, s := normLabels(incidentType, severity)
	IncidentsResolved.WithLabelValues(t, s).Inc()
	ActiveIncidents.WithLabelValues(t, s).Dec()
	from := normPhase(fromPhase)
	if transitionSeconds > 0 {
		IncidentTransitionSeconds.WithLabelValues(from, "resolved").Observe(transitionSeconds)
	}
}

// SetActiveIncidents sets the active gauge for a given (incident_type,
// severity) pair to an absolute value. Use during reconciler bootstrap to
// reconcile the gauge with the live state of the cluster after a restart.
func SetActiveIncidents(incidentType, severity string, value float64) {
	t, s := normLabels(incidentType, severity)
	ActiveIncidents.WithLabelValues(t, s).Set(value)
}

func normLabels(incidentType, severity string) (string, string) {
	if incidentType == "" {
		incidentType = labelUnknown
	}
	if severity == "" {
		severity = labelUnknown
	}
	return incidentType, severity
}

func normPhase(p string) string {
	switch p {
	case "Detecting", "detecting":
		return "detecting"
	case "Active", "active":
		return "active"
	case "Resolved", "resolved":
		return "resolved"
	}
	return labelUnknown
}
