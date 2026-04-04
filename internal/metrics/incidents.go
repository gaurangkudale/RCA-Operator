package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var registerOnce sync.Once

var (
	// ── Phase 1 Spec Metrics ─────────────────────────────────────────────────
	// These metric names match the Phase 1 architecture spec exactly.

	// rca_signals_received_total counts every raw signal entering the pipeline.
	signalsReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_signals_received_total",
			Help: "Total number of signals received by the signal pipeline.",
		},
		[]string{"event_type", "agent"},
	)

	// rca_signals_deduplicated_total counts signals suppressed by the deduplicator.
	signalsDedupTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_signals_deduplicated_total",
			Help: "Total number of signals suppressed by deduplication.",
		},
		[]string{"event_type"},
	)

	// rca_incidents_detecting_total counts new incidents entering the Detecting phase.
	incidentsDetectingTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_incidents_detecting_total",
			Help: "Total number of incidents that entered the Detecting phase.",
		},
		[]string{"agent", "incident_type", "severity"},
	)

	// rca_incidents_activated_total counts incidents promoted to Active.
	incidentsActivatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_incidents_activated_total",
			Help: "Total number of incidents promoted from Detecting to Active.",
		},
		[]string{"agent", "incident_type", "severity"},
	)

	// rca_incidents_resolved_total counts incidents that reached Resolved.
	incidentsResolvedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_incidents_resolved_total",
			Help: "Total number of incidents resolved by the operator.",
		},
		[]string{"agent", "incident_type", "severity"},
	)

	// rca_active_incidents is the current gauge of open incidents.
	activeIncidents = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rca_active_incidents",
			Help: "Number of currently active (non-resolved) incidents.",
		},
		[]string{"agent", "incident_type", "severity"},
	)

	// rca_incident_transition_seconds measures time spent in each phase.
	incidentTransitionSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rca_incident_transition_seconds",
			Help:    "Duration in seconds of incident phase transitions (detecting→active, active→resolved).",
			Buckets: []float64{10, 30, 60, 120, 300, 600, 1800, 3600},
		},
		[]string{"from_phase", "to_phase"},
	)

	// ── Operational Metrics ──────────────────────────────────────────────────

	notificationsSentTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_notifications_sent_total",
			Help: "Total number of incident notification attempts grouped by channel, action, and outcome.",
		},
		[]string{"channel", "action", "outcome", "severity"},
	)

	signalProcessingDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rca_signal_processing_duration_seconds",
			Help:    "Duration of signal processing in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"event_type"},
	)

	ruleEvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_rule_evaluations_total",
			Help: "Total number of rule evaluations, labeled by rule name and whether it fired.",
		},
		[]string{"rule_name", "fired"},
	)

	correlationBufferSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rca_correlation_buffer_size",
			Help: "Current number of events in the correlation buffer.",
		},
		[]string{"agent"},
	)

	notificationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "rca_notification_duration_seconds",
			Help:    "Duration of notification dispatch in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"channel"},
	)
)

func init() {
	registerOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(
			signalsReceivedTotal,
			signalsDedupTotal,
			incidentsDetectingTotal,
			incidentsActivatedTotal,
			incidentsResolvedTotal,
			activeIncidents,
			incidentTransitionSeconds,
			notificationsSentTotal,
			signalProcessingDuration,
			ruleEvaluationsTotal,
			correlationBufferSize,
			notificationDuration,
		)
	})
}

// ── Phase 1 Spec Recording Functions ─────────────────────────────────────────

// RecordSignalReceived increments the signals-received counter.
func RecordSignalReceived(eventType, agent string) {
	signalsReceivedTotal.WithLabelValues(safe(eventType), safe(agent)).Inc()
}

// RecordSignalDeduplicated increments the signals-deduplicated counter.
func RecordSignalDeduplicated(eventType string) {
	signalsDedupTotal.WithLabelValues(safe(eventType)).Inc()
}

// RecordIncidentDetecting increments the detecting-total counter when an
// incident first enters the Detecting phase.
func RecordIncidentDetecting(agent, incidentType, severity string) {
	incidentsDetectingTotal.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

// RecordIncidentActivated increments the activated-total counter when an
// incident transitions from Detecting to Active.
func RecordIncidentActivated(agent, incidentType, severity string) {
	incidentsActivatedTotal.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

// RecordIncidentResolved increments the resolved-total counter.
func RecordIncidentResolved(agent, incidentType, severity string) {
	incidentsResolvedTotal.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

// SetActiveIncidents sets the active incidents gauge.
func SetActiveIncidents(agent, incidentType, severity string, count float64) {
	activeIncidents.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Set(count)
}

// IncActiveIncidents increments the active incidents gauge by one.
func IncActiveIncidents(agent, incidentType, severity string) {
	activeIncidents.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

// DecActiveIncidents decrements the active incidents gauge by one.
func DecActiveIncidents(agent, incidentType, severity string) {
	activeIncidents.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Dec()
}

// ObserveIncidentTransition records the duration of a phase transition.
func ObserveIncidentTransition(fromPhase, toPhase string, seconds float64) {
	incidentTransitionSeconds.WithLabelValues(safe(fromPhase), safe(toPhase)).Observe(seconds)
}

// ── Operational Recording Functions ──────────────────────────────────────────

// RecordSignalProcessed is an alias for RecordSignalReceived for backward compatibility.
func RecordSignalProcessed(eventType, agent string) {
	RecordSignalReceived(eventType, agent)
}

// ObserveSignalDuration records the duration of signal processing.
func ObserveSignalDuration(eventType string, seconds float64) {
	signalProcessingDuration.WithLabelValues(safe(eventType)).Observe(seconds)
}

// RecordRuleEvaluation records a rule evaluation outcome.
func RecordRuleEvaluation(ruleName string, fired bool) {
	f := "false"
	if fired {
		f = "true"
	}
	ruleEvaluationsTotal.WithLabelValues(safe(ruleName), f).Inc()
}

// SetCorrelationBufferSize sets the current buffer size gauge.
func SetCorrelationBufferSize(agent string, size float64) {
	correlationBufferSize.WithLabelValues(safe(agent)).Set(size)
}

// RecordNotification records a notification send attempt.
func RecordNotification(channel, action, outcome, severity string) {
	notificationsSentTotal.WithLabelValues(safe(channel), safe(action), safe(outcome), safe(severity)).Inc()
}

// ObserveNotificationDuration records notification dispatch duration.
func ObserveNotificationDuration(channel string, seconds float64) {
	notificationDuration.WithLabelValues(safe(channel)).Observe(seconds)
}

// ── Deprecated aliases (remove after dashboard/alerting migration) ───────────

// RecordIncidentDetected is a backward-compatible alias for RecordIncidentDetecting.
func RecordIncidentDetected(agent, incidentType, severity string) {
	RecordIncidentDetecting(agent, incidentType, severity)
}

// SetIncidentsActive is a backward-compatible alias for SetActiveIncidents.
func SetIncidentsActive(agent, incidentType, severity string, count float64) {
	SetActiveIncidents(agent, incidentType, severity, count)
}

func safe(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
