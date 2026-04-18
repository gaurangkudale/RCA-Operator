package correlator

import (
	"sort"
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
//
// In addition to the primary chronological entries slice, Buffer maintains a
// secondary byTrace index that maps each OTel trace-id to the list of entries
// it has seen so SnapshotByTrace can return a per-trace slice in O(k) rather
// than scanning the full window. The byTrace index is rebuilt on every purge
// so it stays consistent with entries without requiring deletion bookkeeping.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	byTrace map[string][]Entry
	window  time.Duration
	nowFn   func() time.Time // injectable for tests
	subs    map[chan<- Entry]struct{}
}

// NewBuffer returns a Buffer with the given time window.
func NewBuffer(window time.Duration) *Buffer {
	return &Buffer{
		window:  window,
		nowFn:   time.Now,
		byTrace: make(map[string][]Entry),
		subs:    make(map[chan<- Entry]struct{}),
	}
}

// Subscribe registers ch to receive every new Entry appended to the buffer.
// Delivery is non-blocking: if the subscriber's channel is full the entry is
// dropped for that subscriber so a slow consumer cannot stall the event loop.
// The returned unsub function removes the subscription; it is safe to call
// multiple times.
func (b *Buffer) Subscribe(ch chan<- Entry) func() {
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[chan<- Entry]struct{})
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Add appends event e to the buffer, pruning entries that have fallen outside
// the correlation window first.
func (b *Buffer) Add(e watcher.CorrelatorEvent) {
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	entry := Entry{Event: e, AddedAt: now}
	b.entries = append(b.entries, entry)
	if traceID := extractTraceID(e); traceID != "" {
		b.byTrace[traceID] = append(b.byTrace[traceID], entry)
	}
	for ch := range b.subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

// purgeOld removes entries whose addedAt timestamp is before now-window, and
// rebuilds the byTrace index from the remaining entries.
// Must be called with b.mu held.
func (b *Buffer) purgeOld(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for i < len(b.entries) && b.entries[i].AddedAt.Before(cutoff) {
		i++
	}
	if i == 0 {
		return
	}
	b.entries = b.entries[i:]
	// Rebuild the byTrace index from the surviving entries. This is cheaper
	// than per-entry deletion bookkeeping and keeps the index consistent.
	b.byTrace = make(map[string][]Entry, len(b.byTrace))
	for _, entry := range b.entries {
		if traceID := extractTraceID(entry.Event); traceID != "" {
			b.byTrace[traceID] = append(b.byTrace[traceID], entry)
		}
	}
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

// SnapshotByTrace returns a copy of the entries in the window whose event
// carries the given trace-id. Returns an empty slice when traceID is empty
// or unknown. Like Snapshot, stale entries are purged before the read.
func (b *Buffer) SnapshotByTrace(traceID string) []Entry {
	if traceID == "" {
		return nil
	}
	now := b.nowFn()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.purgeOld(now)
	src := b.byTrace[traceID]
	if len(src) == 0 {
		return nil
	}
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// extractTraceID pulls the W3C trace-id carried by OTel-sourced events. It
// returns an empty string for K8s-sourced events that have no trace context.
// Kept as a package-private helper so the buffer stays decoupled from the
// signals package's public API.
func extractTraceID(e watcher.CorrelatorEvent) string {
	switch ev := e.(type) {
	case watcher.OTelSpanErrorEvent:
		return ev.TraceID
	case watcher.OTelSpanLatencySpikeEvent:
		return ev.TraceID
	case watcher.OTelLogMatchEvent:
		return ev.TraceID
	case watcher.OTelSpanEventEvent:
		return ev.TraceID
	default:
		return ""
	}
}

// CorrelationResult is returned by Correlator.Evaluate. When Fired is true the
// caller should use Severity and Summary in place of the defaults derived from
// the single-event classification. IncidentType is no longer overridden by
// rules — incident types are self-describing from the raw event type.
type CorrelationResult struct {
	Fired    bool
	Severity string
	Summary  string
	Rule     string // name of the rule that fired, for logging
	// Resource overrides the event's pod/resource name for incident dedup and
	// creation. Set by rules that correlate across different resources (e.g.
	// rollout correlation uses deployment name and node-failure correlation uses
	// node name) so that the resulting incident groups under the correct
	// canonical resource identifier.
	Resource string
	// ScopeLevel declares the scope for the Resource override. When set to
	// "Cluster", Resource is a node name; when "Workload", it is a deployment.
	ScopeLevel string
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

// WithRules overrides the rule set used by the correlator. Rules are sorted by
// descending priority so the highest-priority rule fires first.
func WithRules(rules []Rule) CorrelatorOption {
	return func(c *Correlator) {
		sorted := append([]Rule(nil), rules...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Priority() == sorted[j].Priority() {
				return sorted[i].Name() < sorted[j].Name()
			}
			return sorted[i].Priority() > sorted[j].Priority()
		})
		c.rules = sorted
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

// Buffer exposes the underlying sliding-window buffer so external subsystems
// (notably the incident graph builder) can snapshot events for a trace without
// needing to share the Correlator's private state.
func (c *Correlator) Buffer() *Buffer {
	return c.buf
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
