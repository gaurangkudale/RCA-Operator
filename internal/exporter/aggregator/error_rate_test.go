package aggregator

import (
	"sync"
	"testing"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/exporter/events"
)

// fakeClock is a deterministic time source so tests can advance "now" by
// known amounts without sleeping. The aggregator's only time call is
// cfg.Now(), so this fully covers its temporal behaviour.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recorder captures every spike the aggregator emits during a test so
// assertions can inspect ordering, count, and field contents.
type recorder struct {
	mu     sync.Mutex
	spikes []events.LogErrorSpikeEvent
}

func (r *recorder) handle(e events.LogErrorSpikeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spikes = append(r.spikes, e)
}

func (r *recorder) snapshot() []events.LogErrorSpikeEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.LogErrorSpikeEvent, len(r.spikes))
	copy(out, r.spikes)
	return out
}

// newTestAggregator returns an aggregator wired to a fake clock and a
// recorder, with conservative defaults that all tests can override per-call.
func newTestAggregator(t *testing.T, cfg Config) (*ErrorRateAggregator, *fakeClock, *recorder) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)}
	rec := &recorder{}
	cfg.Now = clk.Now
	return New(cfg, rec.handle), clk, rec
}

func errRecord(clk *fakeClock, ns, svc, pod, msg string) LogRecord {
	return LogRecord{
		Timestamp: clk.Now(),
		Namespace: ns,
		Service:   svc,
		Pod:       pod,
		Container: svc,
		Message:   msg,
	}
}

// Below threshold: 4 errors with threshold=5 must not fire.
func TestErrorRateAggregator_BelowThresholdDoesNotFire(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    time.Minute,
		Threshold: 5,
	})

	for i := 0; i < 4; i++ {
		agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "boom"))
	}

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("expected 0 spikes below threshold, got %d", got)
	}
}

// At threshold: the Nth call exactly hits the threshold and must fire once.
func TestErrorRateAggregator_AtThresholdFiresOnce(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    time.Minute,
		Threshold: 3,
	})

	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-1"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-2"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-3"))

	spikes := rec.snapshot()
	if len(spikes) != 1 {
		t.Fatalf("expected exactly 1 spike at threshold, got %d", len(spikes))
	}
	if spikes[0].Service != "payment" || spikes[0].Namespace != "dev" {
		t.Errorf("unexpected spike attribution: %+v", spikes[0])
	}
	if spikes[0].ErrorCount != 3 {
		t.Errorf("expected ErrorCount=3, got %d", spikes[0].ErrorCount)
	}
}

// Cooldown gate: a second spike inside the cooldown window must be suppressed,
// but a third spike after the cooldown elapses must fire again.
func TestErrorRateAggregator_CooldownSuppression(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    10 * time.Minute,
		Threshold: 2,
		Cooldown:  5 * time.Minute,
	})

	// Trigger the first spike.
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "first"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "first"))

	// Inside the cooldown — must NOT produce a second spike, even though
	// the threshold is still met (count keeps growing).
	clk.Advance(2 * time.Minute)
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "during-cooldown"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "during-cooldown"))

	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("expected 1 spike inside cooldown, got %d", got)
	}

	// After the cooldown elapses the next observation should fire again.
	clk.Advance(4 * time.Minute) // total elapsed = 6m, > 5m cooldown
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "after-cooldown"))

	spikes := rec.snapshot()
	if len(spikes) != 2 {
		t.Fatalf("expected 2 spikes after cooldown, got %d", len(spikes))
	}
}

// Pruning: errors older than the window must not count toward the threshold.
// Window = 1m, threshold = 3. Send 2 errors at t=0, advance 90s, send 2 more
// errors. The window now contains only the last 2, so threshold is NOT met
// and no spike should fire.
func TestErrorRateAggregator_PrunesOldEntries(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    time.Minute,
		Threshold: 3,
	})

	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "old"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "old"))

	clk.Advance(90 * time.Second)
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "new"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "new"))

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("expected 0 spikes after pruning, got %d", got)
	}
}

// Per-service isolation: spikes on payment must not interfere with api-gateway.
func TestErrorRateAggregator_ServicesAreIsolated(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    time.Minute,
		Threshold: 2,
	})

	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "p"))
	agg.Observe(errRecord(clk, "dev", "api-gateway", "gw-0", "g"))
	// Each service still has only 1 error — neither should fire yet.
	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("expected 0 spikes after one error per service, got %d", got)
	}

	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "p"))
	// payment hits threshold, api-gateway should remain quiet.
	spikes := rec.snapshot()
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike (payment only), got %d", len(spikes))
	}
	if spikes[0].Service != "payment" {
		t.Errorf("expected payment spike, got %s", spikes[0].Service)
	}
}

// Sample bounding: SampleMessages must not exceed the configured sample size,
// and must contain the most recent messages (not the oldest).
func TestErrorRateAggregator_SampleMessagesBounded(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:     time.Minute,
		Threshold:  2,
		SampleSize: 2,
	})

	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-1"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-2"))
	agg.Observe(errRecord(clk, "dev", "payment", "payment-0", "msg-3"))

	// First spike fired at the 2nd observation. After the 3rd observation
	// the cooldown is active so no new spike — we inspect the first.
	spikes := rec.snapshot()
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike, got %d", len(spikes))
	}
	got := spikes[0].SampleMessages
	if len(got) != 2 {
		t.Fatalf("expected 2 samples, got %d (%v)", len(got), got)
	}
	if got[0] != "msg-1" || got[1] != "msg-2" {
		t.Errorf("unexpected sample contents at first fire: %v", got)
	}
}

// Records without service.name must be dropped (not crash, not bucket into
// an empty key). This guards against a misconfigured upstream collector.
func TestErrorRateAggregator_DropsRecordsWithoutService(t *testing.T) {
	agg, clk, rec := newTestAggregator(t, Config{
		Window:    time.Minute,
		Threshold: 1,
	})

	agg.Observe(LogRecord{
		Timestamp: clk.Now(),
		Namespace: "dev",
		Service:   "",
		Message:   "orphan",
	})

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("expected 0 spikes for service-less record, got %d", got)
	}
}
