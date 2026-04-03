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

	// MaxAutoRules caps the total number of auto-generated RCACorrelationRule
	// CRDs the detector will create. Default 20.
	MaxAutoRules int

	// ExpiryDuration is how long after the last observation an auto-generated
	// rule persists before being deleted. Default 1h.
	ExpiryDuration time.Duration

	// AutoRulePriority is the fixed priority assigned to all auto-generated rules.
	// Default 30. User-created rules default to 100+, so auto-rules always lose.
	AutoRulePriority int
}

// DefaultConfig returns a Config with production-safe defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:          false,
		AnalysisInterval: 60 * time.Second,
		MinOccurrences:   5,
		MinTimeSpan:      10 * time.Minute,
		MaxAutoRules:     20,
		ExpiryDuration:   time.Hour,
		AutoRulePriority: 30,
	}
}
