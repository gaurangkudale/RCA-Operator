package autodetect

import "time"

// Config controls the automatic correlation rule detection feature.
type Config struct {
	// Enabled is the master toggle. When false the detector goroutine is a no-op.
	Enabled bool

	// AnalysisInterval is how often the detector snapshots the buffer and mines
	// for patterns. Default 60s.
	AnalysisInterval time.Duration

	// MinOccurrences is the minimum number of times a co-occurring event pair
	// must be observed before a rule can be created. Default 5.
	MinOccurrences int

	// MinTimeSpan is the minimum duration between a pattern's first and last
	// observation before it qualifies for rule creation. Default 10m.
	MinTimeSpan time.Duration

	// ConfidenceThreshold is the minimum P(B|A) conditional probability
	// required to promote a pattern to a rule. Range 0.0–1.0. Default 0.7.
	ConfidenceThreshold float64

	// MaxAutoRules caps the total number of auto-generated RCACorrelationRule
	// CRDs the detector will create. Default 20.
	MaxAutoRules int

	// ExpiryDuration is how long after the last observation an auto-generated
	// rule persists before being deleted. Default 1h.
	ExpiryDuration time.Duration

	// PriorityFloor is the lowest priority assigned to an auto-generated rule.
	// Default 10.
	PriorityFloor int

	// PriorityCeiling is the highest priority assigned to an auto-generated rule.
	// Default 50. User-created rules default to 100+, so auto-rules always lose.
	PriorityCeiling int
}

// DefaultConfig returns a Config with production-safe defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:             false,
		AnalysisInterval:    60 * time.Second,
		MinOccurrences:      5,
		MinTimeSpan:         10 * time.Minute,
		ConfidenceThreshold: 0.7,
		MaxAutoRules:        20,
		ExpiryDuration:      time.Hour,
		PriorityFloor:       10,
		PriorityCeiling:     50,
	}
}

// Priority computes the auto-rule priority from a confidence score.
// Higher confidence → higher priority, clamped to [PriorityFloor, PriorityCeiling].
func (c Config) Priority(confidence float64) int {
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return c.PriorityFloor + int(float64(c.PriorityCeiling-c.PriorityFloor)*confidence)
}
