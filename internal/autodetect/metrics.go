package autodetect

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	patternsTracked = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rca_autodetect_patterns_tracked",
		Help: "Current number of event co-occurrence patterns being tracked.",
	})
	rulesActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rca_autodetect_rules_active",
		Help: "Current number of auto-generated RCACorrelationRule CRDs.",
	})
	rulesCreatedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rca_autodetect_rules_created_total",
		Help: "Total auto-generated rules created.",
	})
	rulesExpiredTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rca_autodetect_rules_expired_total",
		Help: "Total auto-generated rules expired and deleted.",
	})
	analysisDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "rca_autodetect_analysis_duration_seconds",
		Help:    "Time spent per analysis tick.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	metrics.Registry.MustRegister(
		patternsTracked,
		rulesActive,
		rulesCreatedTotal,
		rulesExpiredTotal,
		analysisDuration,
	)
}

// RecordRuleCreated increments the created counter.
func RecordRuleCreated() {
	rulesCreatedTotal.Inc()
}

// RecordRuleExpired increments the expired counter.
func RecordRuleExpired() {
	rulesExpiredTotal.Inc()
}

// SetPatternsTracked sets the current number of tracked patterns.
func SetPatternsTracked(n int) {
	patternsTracked.Set(float64(n))
}

// SetRulesActive sets the current number of active auto-generated rules.
func SetRulesActive(n int) {
	rulesActive.Set(float64(n))
}

// ObserveAnalysisDuration records the duration of one analysis tick.
func ObserveAnalysisDuration(seconds float64) {
	analysisDuration.Observe(seconds)
}
