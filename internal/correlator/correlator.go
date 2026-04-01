package correlator

import (
	"sync"
	"time"

	"k8s.io/client-go/tools/events"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// Entry holds a single event and the time it was added to the buffer.
type Entry struct {
	Event   watcher.CorrelatorEvent
	AddedAt time.Time
}

// Buffer is a sliding-window event store. Events older than the configured
// window are discarded on each write. It is safe for concurrent use.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	window  time.Duration
	nowFn   func() time.Time // injectable for tests
}

// NewBuffer returns a Buffer with the given time window.
func NewBuffer(window time.Duration) *Buffer {
	return &Buffer{window: window, nowFn: time.Now}
}

// Add appends event e to the buffer, pruning entries that have fallen outside
// the correlation window first.
func (b *Buffer) Add(e watcher.CorrelatorEvent) {
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	b.entries = append(b.entries, Entry{Event: e, AddedAt: now})
}

// purgeOld removes entries whose addedAt timestamp is before now-window.
// Must be called with b.mu held.
func (b *Buffer) purgeOld(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for i < len(b.entries) && b.entries[i].AddedAt.Before(cutoff) {
		i++
	}
	b.entries = b.entries[i:]
}

// Snapshot returns a copy of the current entries after purging stale ones.
func (b *Buffer) Snapshot() []Entry {
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	out := make([]Entry, len(b.entries))
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
	// rollout correlation uses deployment name and node-failure correlation uses
	// node name) so that the
	// resulting incident groups under the correct canonical resource identifier.
	Resource string
}

// RuleEngine evaluates collected events and may override the default
// single-signal classification with a correlated incident result.
type RuleEngine interface {
	Add(event watcher.CorrelatorEvent)
	Evaluate(event watcher.CorrelatorEvent) CorrelationResult
}

// Rule is a registered correlation rule evaluated by the rule engine.
type Rule interface {
	Name() string
	Priority() int
	Evaluate(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult
}

type CorrelatorOption func(*Correlator)

// WithRules overrides the rule set used by the correlator.
func WithRules(rules []Rule) CorrelatorOption {
	return func(c *Correlator) {
		c.rules = append([]Rule(nil), rules...)
	}
}

// Correlator maintains a sliding-window buffer of recent events and evaluates
// registered correlation rules on each new event.
type Correlator struct {
	buf   *Buffer
	rules []Rule
}

// NewCorrelator returns a Correlator with the given correlation time window
// and the currently registered rule set.
func NewCorrelator(window time.Duration, opts ...CorrelatorOption) *Correlator {
	c := &Correlator{
		buf:   NewBuffer(window),
		rules: RegisteredRules(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Add records event e in the buffer so future calls to Evaluate can correlate
// it against subsequent events.
func (c *Correlator) Add(e watcher.CorrelatorEvent) {
	c.buf.Add(e)
}

// Evaluate runs all registered rules against the current buffer contents and
// the incoming event. The first rule that fires is returned; if no rule fires,
// a zero CorrelationResult (Fired=false) is returned.
func (c *Correlator) Evaluate(event watcher.CorrelatorEvent) CorrelationResult {
	entries := c.buf.Snapshot()
	for _, rule := range c.rules {
		if result := rule.Evaluate(event, entries); result.Fired {
			return result
		}
	}
	return CorrelationResult{}
}

// Option is a functional option for Consumer.
type Option func(*Consumer)

// WithRuleEngine attaches a rule engine to the consumer so that multi-signal
// correlation rules are evaluated on every incoming event.
func WithRuleEngine(engine RuleEngine) Option {
	return func(consumer *Consumer) {
		consumer.ruleEngine = engine
	}
}

// WithEventRecorder attaches a Kubernetes EventRecorder to the Consumer.
// When set, the consumer emits corev1.Events on IncidentReport CRs at key
// lifecycle points (detected, resolved, re-opened), making them visible via
// kubectl describe incidentreport.
func WithEventRecorder(r events.EventRecorder) Option {
	return func(consumer *Consumer) {
		consumer.rep.Recorder = r
	}
}
