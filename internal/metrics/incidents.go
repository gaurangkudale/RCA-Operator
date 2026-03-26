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
)

func init() {
	registerOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(
			incidentsDetectedTotal,
			incidentsResolvedTotal,
			notificationsSentTotal,
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

func safe(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
