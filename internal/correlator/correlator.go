package correlator

import (
	"sync"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// entry holds a single event and the time it was added to the buffer.
type entry struct {
	event   watcher.CorrelatorEvent
	addedAt time.Time
}

// Buffer is a sliding-window event store. Events older than the configured
// window are discarded on each write. It is safe for concurrent use.
type Buffer struct {
	mu      sync.Mutex
	entries []entry
	window  time.Duration
	nowFn   func() time.Time // injectable for tests
}

// newBuffer returns a Buffer with the given time window.
func newBuffer(window time.Duration) *Buffer {
	return &Buffer{window: window, nowFn: time.Now}
}

// Add appends event e to the buffer, pruning entries that have fallen outside
// the correlation window first.
func (b *Buffer) Add(e watcher.CorrelatorEvent) {
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	b.entries = append(b.entries, entry{event: e, addedAt: now})
}

// purgeOld removes entries whose addedAt timestamp is before now-window.
// Must be called with b.mu held.
func (b *Buffer) purgeOld(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for i < len(b.entries) && b.entries[i].addedAt.Before(cutoff) {
		i++
	}
	b.entries = b.entries[i:]
}

// snapshot returns a copy of the current entries after purging stale ones.
func (b *Buffer) snapshot() []entry {
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	out := make([]entry, len(b.entries))
	copy(out, b.entries)
	return out
}

// CorrelationResult is returned by Correlator.Evaluate. When Fired is true the
// caller should use IncidentType, Severity, and Summary in place of the defaults
// derived from the single-event classication.
type CorrelationResult struct {
	Fired        bool
	IncidentType string
	Severity     string
	Summary      string
	Rule         string // name of the rule that fired, for logging
	// Resource overrides the event's pod/resource name for incident dedup and
	// creation. Set by rules that correlate across different resources (e.g.
	// Rule 2 uses deployment name, Rules 3 & 5 use node name) so that the
	// resulting incident groups under the correct canonical resource identifier.
	Resource string
}

// Correlator maintains a sliding-window buffer of recent events and evaluates
// the five correlation rules on each new event.
type Correlator struct {
	buf *Buffer
}

// NewCorrelator returns a Correlator with the given correlation time window.
func NewCorrelator(window time.Duration) *Correlator {
	return &Correlator{buf: newBuffer(window)}
}

// Add records event e in the buffer so future calls to Evaluate can correlate
// it against subsequent events.
func (c *Correlator) Add(e watcher.CorrelatorEvent) {
	c.buf.Add(e)
}

// Evaluate runs all correlation rules against the current buffer contents and
// the incoming event. The first rule that fires is returned; if no rule fires,
// a zero CorrelationResult (Fired=false) is returned.
func (c *Correlator) Evaluate(event watcher.CorrelatorEvent) CorrelationResult {
	entries := c.buf.snapshot()
	for _, rule := range allRules {
		if result := rule(event, entries); result.Fired {
			return result
		}
	}
	return CorrelationResult{}
}

// Option is a functional option for Consumer.
type Option func(*Consumer)

// WithCorrelator attaches a Correlator to the Consumer so that multi-event
// correlation rules are evaluated on every incoming event.
func WithCorrelator(c *Correlator) Option {
	return func(consumer *Consumer) {
		consumer.correlator = c
	}
}
