package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var registerOnce sync.Once

var (
	incidentsDetectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_incidents_detected_total",
			Help: "Total number of incident lifecycles detected by the operator.",
		},
		[]string{"agent", "incident_type", "severity"},
	)
	incidentsResolvedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_incidents_resolved_total",
			Help: "Total number of incident lifecycles resolved by the operator.",
		},
		[]string{"agent", "incident_type", "severity"},
	)
	notificationsSentTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_notifications_sent_total",
			Help: "Total number of incident notification attempts grouped by channel, action, and outcome.",
		},
		[]string{"channel", "action", "outcome", "severity"},
	)

	// Phase 1 additions
	signalsProcessedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "rca_signals_processed_total",
			Help: "Total number of signals processed by the signal pipeline.",
		},
		[]string{"event_type", "agent"},
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
	incidentsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rca_incidents_active",
			Help: "Number of currently active incidents.",
		},
		[]string{"agent", "incident_type", "severity"},
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
			incidentsDetectedTotal,
			incidentsResolvedTotal,
			notificationsSentTotal,
			signalsProcessedTotal,
			signalProcessingDuration,
			ruleEvaluationsTotal,
			correlationBufferSize,
			incidentsActive,
			notificationDuration,
		)
	})
}

func RecordIncidentDetected(agent, incidentType, severity string) {
	incidentsDetectedTotal.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

func RecordIncidentResolved(agent, incidentType, severity string) {
	incidentsResolvedTotal.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Inc()
}

func RecordNotification(channel, action, outcome, severity string) {
	notificationsSentTotal.WithLabelValues(safe(channel), safe(action), safe(outcome), safe(severity)).Inc()
}

// RecordSignalProcessed records a signal event processed.
func RecordSignalProcessed(eventType, agent string) {
	signalsProcessedTotal.WithLabelValues(safe(eventType), safe(agent)).Inc()
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

// SetIncidentsActive sets the active incidents gauge.
func SetIncidentsActive(agent, incidentType, severity string, count float64) {
	incidentsActive.WithLabelValues(safe(agent), safe(incidentType), safe(severity)).Set(count)
}

// ObserveNotificationDuration records notification dispatch duration.
func ObserveNotificationDuration(channel string, seconds float64) {
	notificationDuration.WithLabelValues(safe(channel)).Observe(seconds)
}

func safe(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
