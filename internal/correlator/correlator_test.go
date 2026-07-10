package correlator

import (
	"testing"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers to build test events
// ─────────────────────────────────────────────────────────────────────────────

var testNow = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

func crashLoop(ns, pod, node, container string, restarts int32) watcher.CrashLoopBackOffEvent { //nolint:unparam
	return watcher.CrashLoopBackOffEvent{
		BaseEvent:     watcher.BaseEvent{Namespace: ns, PodName: pod, NodeName: node},
		ContainerName: container,
		RestartCount:  restarts,
	}
}

func oomKilled(ns, pod, node, container string) watcher.OOMKilledEvent {
	return watcher.OOMKilledEvent{
		BaseEvent:     watcher.BaseEvent{Namespace: ns, PodName: pod, NodeName: node},
		ContainerName: container,
	}
}

// makeBuffer returns a Buffer with a fixed nowFn for deterministic tests.
func makeBuffer(window time.Duration) *Buffer {
	b := NewBuffer(window)
	b.nowFn = func() time.Time { return testNow }
	return b
}

// addAt adds an event to the buffer with a custom addedAt time, bypassing nowFn.
func addAt(b *Buffer, e watcher.CorrelatorEvent, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, Entry{Event: e, AddedAt: at})
}

// ─────────────────────────────────────────────────────────────────────────────
// Buffer tests
// ─────────────────────────────────────────────────────────────────────────────

func TestBuffer_AddAndPurge(t *testing.T) {
	b := NewBuffer(5 * time.Minute)

	tick := testNow
	b.nowFn = func() time.Time { return tick }

	// Add an event at t=0.
	b.Add(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if len(b.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(b.entries))
	}

	// Advance time past the window and add a second event.
	tick = testNow.Add(6 * time.Minute)
	b.Add(crashLoop("ns", "pod-b", "node-1", "app", 3))

	// The first event should have been purged.
	if len(b.entries) != 1 {
		t.Fatalf("expected 1 entry after purge, got %d", len(b.entries))
	}
	cl, ok := b.entries[0].Event.(watcher.CrashLoopBackOffEvent)
	if !ok || cl.PodName != "pod-b" {
		t.Fatalf("expected pod-b to remain, got %+v", b.entries[0].Event)
	}
}

func TestBuffer_Snapshot(t *testing.T) {
	b := makeBuffer(5 * time.Minute)

	// Add two events within the window.
	addAt(b, crashLoop("ns", "pod-a", "node-1", "app", 1), testNow.Add(-2*time.Minute))
	addAt(b, oomKilled("ns", "pod-a", "node-1", "app"), testNow.Add(-1*time.Minute))

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries in snapshot, got %d", len(snap))
	}

	// Mutating the snapshot must not affect the buffer.
	snap[0] = Entry{}
	if _, ok := b.entries[0].Event.(watcher.CrashLoopBackOffEvent); !ok {
		t.Fatal("snapshot mutation affected buffer")
	}
}

func TestBuffer_Snapshot_PurgesExpired(t *testing.T) {
	b := makeBuffer(5 * time.Minute)
	addAt(b, crashLoop("ns", "pod-a", "node-1", "app", 1), testNow.Add(-10*time.Minute))
	addAt(b, oomKilled("ns", "pod-b", "node-1", "app"), testNow.Add(-1*time.Minute))

	snap := b.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry after expiry purge, got %d", len(snap))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Correlator: rule injection, evaluate ordering, and no-fire behaviour.
// All correlation rules are now CRD-driven. These tests inject rules via
// WithRules() to exercise the Correlator infrastructure.
// ─────────────────────────────────────────────────────────────────────────────

// testRule is defined in testutil_test.go.

func TestCorrelator_InjectedRuleFires(t *testing.T) {
	rule := testRule("TestOOM", 400, func(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
		if _, ok := event.(watcher.CrashLoopBackOffEvent); !ok {
			return CorrelationResult{}
		}
		for _, en := range entries {
			if _, ok := en.Event.(watcher.OOMKilledEvent); ok {
				return CorrelationResult{Fired: true, Severity: "P2", Summary: "test-oom"}
			}
		}
		return CorrelationResult{}
	})

	corr := NewCorrelator(5*time.Minute, WithRules([]Rule{rule}))
	corr.buf.nowFn = func() time.Time { return testNow }

	corr.Add(oomKilled("ns", "pod-a", "node-1", "app"))
	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 5))
	if !result.Fired {
		t.Fatal("expected injected rule to fire")
	}
	if result.Severity != "P2" {
		t.Errorf("Severity=%q want P2", result.Severity)
	}
	if result.Rule != "TestOOM" {
		t.Errorf("Rule=%q want TestOOM", result.Rule)
	}
}

func TestCorrelator_PriorityOrdering(t *testing.T) {
	lowPriority := testRule("Low", 100, func(event watcher.CorrelatorEvent, _ []Entry) CorrelationResult {
		return CorrelationResult{Fired: true, Severity: "P3", Summary: "low"}
	})
	highPriority := testRule("High", 500, func(event watcher.CorrelatorEvent, _ []Entry) CorrelationResult {
		return CorrelationResult{Fired: true, Severity: "P1", Summary: "high"}
	})

	// Inject in wrong order — correlator should sort by priority.
	corr := NewCorrelator(5*time.Minute, WithRules([]Rule{lowPriority, highPriority}))
	corr.buf.nowFn = func() time.Time { return testNow }

	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if !result.Fired {
		t.Fatal("expected a rule to fire")
	}
	if result.Rule != "High" {
		t.Errorf("expected High to win (priority 500), got rule=%q", result.Rule)
	}
	if result.Severity != "P1" {
		t.Errorf("expected P1 severity, got %q", result.Severity)
	}
}

func TestCorrelator_NoFire(t *testing.T) {
	neverFires := testRule("Never", 100, func(_ watcher.CorrelatorEvent, _ []Entry) CorrelationResult {
		return CorrelationResult{}
	})

	corr := NewCorrelator(5*time.Minute, WithRules([]Rule{neverFires}))
	corr.buf.nowFn = func() time.Time { return testNow }

	corr.Add(oomKilled("other-ns", "pod-x", "node-1", "app"))
	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if result.Fired {
		t.Errorf("expected no rule to fire, got rule=%q", result.Rule)
	}
}

func TestCorrelator_WindowExpiry(t *testing.T) {
	rule := testRule("TestOOM", 400, func(event watcher.CorrelatorEvent, entries []Entry) CorrelationResult {
		if _, ok := event.(watcher.CrashLoopBackOffEvent); !ok {
			return CorrelationResult{}
		}
		for _, en := range entries {
			if _, ok := en.Event.(watcher.OOMKilledEvent); ok {
				return CorrelationResult{Fired: true, Severity: "P2", Summary: "oom"}
			}
		}
		return CorrelationResult{}
	})

	corr := NewCorrelator(5*time.Minute, WithRules([]Rule{rule}))
	tick := testNow
	corr.buf.nowFn = func() time.Time { return tick }

	corr.Add(oomKilled("ns", "pod-a", "node-1", "app"))

	// Advance time past the window.
	tick = testNow.Add(6 * time.Minute)

	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if result.Fired {
		t.Errorf("expected no rule to fire after window expiry, got rule=%q", result.Rule)
	}
}

func TestCorrelator_ZeroRulesNoFire(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return testNow }

	corr.Add(oomKilled("ns", "pod-a", "node-1", "app"))
	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if result.Fired {
		t.Errorf("expected no fire with zero registered rules, got rule=%q", result.Rule)
	}
}

