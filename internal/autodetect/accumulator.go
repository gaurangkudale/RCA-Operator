package autodetect

import (
	"maps"
	"sync"
	"time"
)

// PatternRecord tracks the history of a detected event co-occurrence pattern
// across multiple analysis ticks.
type PatternRecord struct {
	Pair        EventPair
	FirstSeen   time.Time
	LastSeen    time.Time
	Occurrences int    // times this pair was observed co-occurring
	RuleName    string // name of auto-created rule, "" if not yet created
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

// Record updates the accumulator with pairs from a single analysis tick.
func (a *Accumulator) Record(pairs []EventPair) {
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
// auto-rule discovered at startup). The pair is normalized to canonical order
// before keying, and if a record already exists it is not overwritten.
func (a *Accumulator) Seed(rec *PatternRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rec == nil {
		return
	}

	rec.Pair = NormalizePair(rec.Pair)
	key := rec.Pair.Key()

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
