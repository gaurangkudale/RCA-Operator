package autodetect

import (
	"maps"
	"sync"
	"time"
)

// PatternRecord tracks the history of a detected event co-occurrence pattern
// across multiple analysis ticks.
type PatternRecord struct {
	Pair         EventPair
	FirstSeen    time.Time
	LastSeen     time.Time
	Occurrences  int    // times this pair was observed co-occurring
	TriggerCount int    // times the trigger event appeared (for conditional probability)
	RuleName     string // name of auto-created rule, "" if not yet created
}

// Confidence returns the conditional probability P(condition|trigger) for this pattern.
func (r *PatternRecord) Confidence() float64 {
	if r.TriggerCount == 0 {
		return 0
	}
	return float64(r.Occurrences) / float64(r.TriggerCount)
}

// Accumulator maintains pattern history across analysis ticks.
// It is safe for concurrent use.
type Accumulator struct {
	mu       sync.Mutex
	patterns map[string]*PatternRecord
	nowFn    func() time.Time
}

// NewAccumulator returns an empty Accumulator.
func NewAccumulator() *Accumulator {
	return &Accumulator{
		patterns: make(map[string]*PatternRecord),
		nowFn:    time.Now,
	}
}

// Record updates the accumulator with pairs and trigger counts from a single
// analysis tick.
func (a *Accumulator) Record(pairs []EventPair, triggerCounts map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.nowFn()

	// Track which patterns were seen in this tick to avoid double-counting.
	seenThisTick := make(map[string]bool)

	for _, pair := range pairs {
		key := pair.Key()
		if seenThisTick[key] {
			continue
		}
		seenThisTick[key] = true

		rec, exists := a.patterns[key]
		if !exists {
			rec = &PatternRecord{
				Pair:      pair,
				FirstSeen: now,
			}
			a.patterns[key] = rec
		}
		rec.LastSeen = now
		rec.Occurrences++
	}

	// Update trigger counts: for each pattern, increment its TriggerCount
	// by the trigger event's count in this tick.
	for key, rec := range a.patterns {
		if !seenThisTick[key] {
			continue
		}
		if tc, ok := triggerCounts[rec.Pair.TriggerType]; ok {
			rec.TriggerCount += tc
		}
	}
}

// ReadyPatterns returns all patterns that exceed the configuration thresholds
// and are eligible for rule creation.
func (a *Accumulator) ReadyPatterns(cfg Config) []*PatternRecord {
	a.mu.Lock()
	defer a.mu.Unlock()

	var ready []*PatternRecord
	for _, rec := range a.patterns {
		if rec.Occurrences < cfg.MinOccurrences {
			continue
		}
		if rec.LastSeen.Sub(rec.FirstSeen) < cfg.MinTimeSpan {
			continue
		}
		if rec.Confidence() < cfg.ConfidenceThreshold {
			continue
		}
		ready = append(ready, rec)
	}
	return ready
}

// All returns a snapshot of all tracked patterns. The returned map is a copy
// of the keys; the PatternRecord pointers are shared.
func (a *Accumulator) All() map[string]*PatternRecord {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make(map[string]*PatternRecord, len(a.patterns))
	maps.Copy(out, a.patterns)
	return out
}

// Seed adds a pre-existing pattern record (e.g., from a previously created
// auto-rule discovered at startup). If a record for the key already exists it
// is not overwritten.
func (a *Accumulator) Seed(key string, rec *PatternRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.patterns[key]; !exists {
		a.patterns[key] = rec
	}
}

// Count returns the number of tracked patterns.
func (a *Accumulator) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.patterns)
}
