// Package aggregator implements stateless-friendly stream detectors that turn
// raw log records into higher-level CorrelatorEvents. The first detector is
// ErrorRateAggregator: a per-service sliding-window counter that fires a
// LogErrorSpikeEvent when the count of ERROR-or-higher records crosses a
// configured threshold inside the window.
//
// Design notes:
//
//   - The aggregator is in-memory and per-process. Because the rca-exporter is
//     deliberately deployed as a single replica in the MVP (see
//     config/rca-exporter/deployment.yaml), there is no need for a distributed
//     state store. Multi-replica horizontal scaling will require a follow-up
//     PR with consistent hashing or a shared backend.
//
//   - A cooldown gate prevents the same incident from being re-emitted on
//     every subsequent error within the window. The cooldown defaults to
//     reporter.SignalCooldown (5 min), matching the dedup window the reporter
//     itself enforces, so the aggregator never fires faster than the reporter
//     can absorb.
//
//   - Sample messages are kept as a bounded ring (last N) so the
//     IncidentReport summary self-explains without forcing the on-call to
//     leave kubectl.
package aggregator

import (
	"sync"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/exporter/events"
)

// DefaultCooldown matches reporter.SignalCooldown (5m). Duplicated here as a
// constant rather than imported to keep this package free of any
// reporter/k8s coupling — aggregator.go is pure stream logic and unit-testable
// without a fake client.
const DefaultCooldown = 5 * time.Minute

// DefaultSampleSize is the number of recent error messages retained per
// service window for inclusion in the IncidentReport summary.
const DefaultSampleSize = 3

// LogRecord is the minimal projection of an OTLP LogRecord that the
// aggregator needs. Producers (the OTLP receiver) flatten incoming OTLP
// payloads into this shape so the aggregator never depends on protobuf types.
type LogRecord struct {
	Timestamp time.Time
	Namespace string
	Service   string
	Pod       string
	Container string
	Message   string
}

// SpikeHandler is the callback invoked when the aggregator detects a spike.
// The bridge package wires this to reporter.EnsureIncident.
type SpikeHandler func(events.LogErrorSpikeEvent)

// Config holds the tunables for an ErrorRateAggregator. Zero values fall back
// to documented defaults so a caller can construct one with `Config{Window:
// time.Minute, Threshold: 10}` and not worry about the rest.
type Config struct {
	// Window is the rolling detection window. Errors older than now-Window
	// are pruned on every Observe call.
	Window time.Duration

	// Threshold is the minimum error count inside Window required to fire.
	Threshold int

	// Cooldown is the minimum interval between two spikes for the same key.
	// Defaults to DefaultCooldown when zero.
	Cooldown time.Duration

	// SampleSize is the maximum number of sample messages retained per key.
	// Defaults to DefaultSampleSize when zero.
	SampleSize int

	// Now is an injectable clock for tests. Defaults to time.Now.
	Now func() time.Time
}

// ErrorRateAggregator is the per-service error-rate detector. It is safe for
// concurrent Observe calls from multiple OTLP gRPC handlers.
type ErrorRateAggregator struct {
	cfg     Config
	onSpike SpikeHandler

	mu      sync.Mutex
	windows map[string]*serviceWindow
}

// serviceWindow holds the per-(namespace,service) sliding-window state.
// timestamps is a FIFO of error-record timestamps inside the window; older
// entries are popped from the head on every Observe.
type serviceWindow struct {
	timestamps []time.Time
	samples    []string
	lastFired  time.Time
	lastPod    string
	lastCont   string
}

// New constructs an aggregator. The caller must supply a non-nil onSpike
// callback (the bridge in production, a test recorder in unit tests).
func New(cfg Config, onSpike SpikeHandler) *ErrorRateAggregator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultCooldown
	}
	if cfg.SampleSize <= 0 {
		cfg.SampleSize = DefaultSampleSize
	}
	return &ErrorRateAggregator{
		cfg:     cfg,
		onSpike: onSpike,
		windows: make(map[string]*serviceWindow),
	}
}

// Observe records a single error log record and, if the threshold has been
// crossed and the cooldown has elapsed, invokes the SpikeHandler synchronously.
//
// The caller is expected to filter for ERROR severity before calling Observe;
// the aggregator does not interpret severity itself so it stays trivially
// testable and allows alternative classifiers (regex, ML) to be plugged into
// the receiver layer later without touching this code.
func (a *ErrorRateAggregator) Observe(rec LogRecord) {
	if rec.Service == "" {
		// Records without a service.name are unattributable and would all
		// collapse into a single bogus key. Drop them silently — the OTel
		// Collector's k8sattributes processor is responsible for filling
		// this in upstream, and missing it is a configuration error worth
		// surfacing through the exporter's own self-metrics rather than
		// fabricating a fake service identity.
		return
	}
	if a.cfg.Threshold <= 0 || a.cfg.Window <= 0 {
		// Misconfigured aggregator — never fire. Validation upstream
		// (cmd/rca-exporter flag parsing) should prevent this from
		// happening at runtime.
		return
	}

	key := rec.Namespace + "/" + rec.Service
	now := a.cfg.Now()
	cutoff := now.Add(-a.cfg.Window)

	a.mu.Lock()
	w, ok := a.windows[key]
	if !ok {
		w = &serviceWindow{}
		a.windows[key] = w
	}

	// Prune entries that have fallen out of the rolling window. The
	// timestamps slice is monotonically non-decreasing because Observe is
	// called from a single OTLP request handler per record (and the OTel
	// pipeline preserves arrival order), so a simple linear walk from the
	// head is correct and O(k) per call where k = number of expired entries.
	i := 0
	for i < len(w.timestamps) && w.timestamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		w.timestamps = w.timestamps[i:]
	}

	w.timestamps = append(w.timestamps, rec.Timestamp)

	// Track the most recent pod/container for incident attribution. Without
	// this an incident would have no concrete pod to point at when the
	// dashboard renders the affected resources.
	if rec.Pod != "" {
		w.lastPod = rec.Pod
	}
	if rec.Container != "" {
		w.lastCont = rec.Container
	}

	if rec.Message != "" {
		w.samples = append(w.samples, rec.Message)
		if len(w.samples) > a.cfg.SampleSize {
			// Drop the oldest sample to keep the ring bounded. Recent
			// messages are more diagnostically valuable than the first
			// errors of the burst, which are often a single repeating
			// stack trace.
			w.samples = w.samples[len(w.samples)-a.cfg.SampleSize:]
		}
	}

	count := len(w.timestamps)
	shouldFire := count >= a.cfg.Threshold &&
		(w.lastFired.IsZero() || now.Sub(w.lastFired) >= a.cfg.Cooldown)

	if !shouldFire {
		a.mu.Unlock()
		return
	}

	// Build the event under the lock so the snapshot is consistent, but
	// invoke the callback after releasing it. SpikeHandler talks to the
	// Kubernetes API and must not be called while holding a.mu — that would
	// serialize all OTLP requests behind a single API roundtrip.
	evt := events.LogErrorSpikeEvent{
		At:             now,
		Namespace:      rec.Namespace,
		Service:        rec.Service,
		Pod:            w.lastPod,
		Container:      w.lastCont,
		ErrorCount:     count,
		WindowSeconds:  int(a.cfg.Window.Seconds()),
		Threshold:      a.cfg.Threshold,
		SampleMessages: append([]string(nil), w.samples...),
	}
	w.lastFired = now
	a.mu.Unlock()

	a.onSpike(evt)
}
