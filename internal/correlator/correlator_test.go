package correlator

import (
	"fmt"
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

func imagePull(ns, pod, container, reason string) watcher.ImagePullBackOffEvent { //nolint:unparam
	return watcher.ImagePullBackOffEvent{
		BaseEvent:     watcher.BaseEvent{Namespace: ns, PodName: pod},
		ContainerName: container,
		Reason:        reason,
	}
}

func stalledRollout(ns, dep string) watcher.StalledRolloutEvent {
	return watcher.StalledRolloutEvent{
		BaseEvent:      watcher.BaseEvent{Namespace: ns},
		DeploymentName: dep,
	}
}

func nodeNotReady(ns, node, reason string) watcher.NodeNotReadyEvent { //nolint:unparam
	return watcher.NodeNotReadyEvent{
		BaseEvent: watcher.BaseEvent{Namespace: ns, NodeName: node},
		Reason:    reason,
	}
}

func podEvicted(ns, pod, node string) watcher.PodEvictedEvent { //nolint:unparam
	return watcher.PodEvictedEvent{
		BaseEvent: watcher.BaseEvent{Namespace: ns, PodName: pod, NodeName: node},
	}
}

func podHealthy(ns, pod string) watcher.PodHealthyEvent {
	return watcher.PodHealthyEvent{
		BaseEvent: watcher.BaseEvent{Namespace: ns, PodName: pod},
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
// Rule 1: CrashLoop + OOMKilled → escalated severity P2
// ─────────────────────────────────────────────────────────────────────────────

func TestRule1_CrashLoopPlusOOM(t *testing.T) {
	cases := []struct {
		name     string
		history  []watcher.CorrelatorEvent
		trigger  watcher.CorrelatorEvent
		wantFire bool
		wantSev  string
	}{
		{
			name:     "OOM in buffer + CrashLoop arriving",
			history:  []watcher.CorrelatorEvent{oomKilled("ns", "pod-a", "node-1", "app")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 5),
			wantFire: true, wantSev: "P2",
		},
		{
			name:     "CrashLoop in buffer + OOM arriving",
			history:  []watcher.CorrelatorEvent{crashLoop("ns", "pod-a", "node-1", "app", 5)},
			trigger:  oomKilled("ns", "pod-a", "node-1", "app"),
			wantFire: true, wantSev: "P2",
		},
		{
			name:     "CrashLoop only — no OOM",
			history:  []watcher.CorrelatorEvent{crashLoop("ns", "pod-a", "node-1", "app", 5)},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 6),
			wantFire: false,
		},
		{
			name:     "OOM for different pod — no fire",
			history:  []watcher.CorrelatorEvent{oomKilled("ns", "pod-b", "node-1", "app")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 5),
			wantFire: false,
		},
		{
			name:     "OOM in different namespace — no fire",
			history:  []watcher.CorrelatorEvent{oomKilled("other-ns", "pod-a", "node-1", "app")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 5),
			wantFire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := make([]Entry, len(tc.history))
			for i, e := range tc.history {
				entries[i] = Entry{Event: e, AddedAt: testNow}
			}
			result := ruleCrashLoopPlusOOM(tc.trigger, entries)
			if result.Fired != tc.wantFire {
				t.Fatalf("Fired=%v want %v", result.Fired, tc.wantFire)
			}
			if tc.wantFire {
				if result.Severity != tc.wantSev {
					t.Errorf("Severity=%q want %q", result.Severity, tc.wantSev)
				}
				if result.Rule != "CrashLoopPlusOOM" {
					t.Errorf("Rule=%q want CrashLoopPlusOOM", result.Rule)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 2: CrashLoop + StalledRollout in same namespace → BadDeploy, P2
// ─────────────────────────────────────────────────────────────────────────────

func TestRule2_CrashLoopPlusBadDeploy(t *testing.T) {
	cases := []struct {
		name     string
		history  []watcher.CorrelatorEvent
		trigger  watcher.CorrelatorEvent
		wantFire bool
	}{
		{
			name:     "StalledRollout same ns + CrashLoop fires",
			history:  []watcher.CorrelatorEvent{stalledRollout("ns", "my-deploy")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 3),
			wantFire: true,
		},
		{
			name:     "StalledRollout different ns — no fire",
			history:  []watcher.CorrelatorEvent{stalledRollout("other-ns", "my-deploy")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 3),
			wantFire: false,
		},
		{
			name:     "No stalled rollout in buffer — no fire",
			history:  []watcher.CorrelatorEvent{oomKilled("ns", "pod-a", "node-1", "app")},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 3),
			wantFire: false,
		},
		{
			name:     "Non-CrashLoop trigger — never fires",
			history:  []watcher.CorrelatorEvent{stalledRollout("ns", "my-deploy")},
			trigger:  imagePull("ns", "pod-a", "app", "ErrImagePull"),
			wantFire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := make([]Entry, len(tc.history))
			for i, e := range tc.history {
				entries[i] = Entry{Event: e, AddedAt: testNow}
			}
			result := ruleCrashLoopPlusBadDeploy(tc.trigger, entries)
			if result.Fired != tc.wantFire {
				t.Fatalf("Fired=%v want %v", result.Fired, tc.wantFire)
			}
			if tc.wantFire {
				if result.Severity != "P2" {
					t.Errorf("Severity=%q want P2", result.Severity)
				}
				if result.ScopeLevel != "Workload" {
					t.Errorf("ScopeLevel=%q want Workload", result.ScopeLevel)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 4: ImagePullBackOff + no prior PodHealthy → Registry P2
// ─────────────────────────────────────────────────────────────────────────────

func TestRule4_ImagePullNoHistory(t *testing.T) {
	cases := []struct {
		name     string
		history  []watcher.CorrelatorEvent
		trigger  watcher.CorrelatorEvent
		wantFire bool
		wantSev  string
	}{
		{
			name:     "No healthy event in buffer — fires P2",
			history:  []watcher.CorrelatorEvent{},
			trigger:  imagePull("ns", "pod-a", "app", "ErrImagePull"),
			wantFire: true, wantSev: "P2",
		},
		{
			name: "Healthy event for same pod in buffer — no fire",
			history: []watcher.CorrelatorEvent{
				podHealthy("ns", "pod-a"),
			},
			trigger:  imagePull("ns", "pod-a", "app", "ErrImagePull"),
			wantFire: false,
		},
		{
			name: "Healthy event for different pod — fires P2",
			history: []watcher.CorrelatorEvent{
				podHealthy("ns", "pod-b"),
			},
			trigger:  imagePull("ns", "pod-a", "app", "ErrImagePull"),
			wantFire: true, wantSev: "P2",
		},
		{
			name:     "Non-ImagePull trigger — never fires",
			history:  []watcher.CorrelatorEvent{},
			trigger:  crashLoop("ns", "pod-a", "node-1", "app", 3),
			wantFire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := make([]Entry, len(tc.history))
			for i, e := range tc.history {
				entries[i] = Entry{Event: e, AddedAt: testNow}
			}
			result := ruleImagePullNoHistory(tc.trigger, entries)
			if result.Fired != tc.wantFire {
				t.Fatalf("Fired=%v want %v", result.Fired, tc.wantFire)
			}
			if tc.wantFire {
				if result.Severity != tc.wantSev {
					t.Errorf("Severity=%q want %q", result.Severity, tc.wantSev)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Rule 5: NodeNotReady + eviction events → NodeFailure P1
// ─────────────────────────────────────────────────────────────────────────────

func TestRule5_NodeNotReadyPlusEviction(t *testing.T) {
	cases := []struct {
		name     string
		history  []watcher.CorrelatorEvent
		trigger  watcher.CorrelatorEvent
		wantFire bool
	}{
		{
			name: "PodEvicted in buffer + NodeNotReady arrives — fires",
			history: []watcher.CorrelatorEvent{
				podEvicted("ns", "pod-a", "node-1"),
			},
			trigger:  nodeNotReady("ns", "node-1", "KubeletNotReady"),
			wantFire: true,
		},
		{
			name: "NodeNotReady in buffer + PodEvicted arrives — fires",
			history: []watcher.CorrelatorEvent{
				nodeNotReady("ns", "node-1", "KubeletNotReady"),
			},
			trigger:  podEvicted("ns", "pod-a", "node-1"),
			wantFire: true,
		},
		{
			name: "Eviction on different node — no fire",
			history: []watcher.CorrelatorEvent{
				podEvicted("ns", "pod-a", "node-2"),
			},
			trigger:  nodeNotReady("ns", "node-1", "KubeletNotReady"),
			wantFire: false,
		},
		{
			name: "NodeNotReady for different node — no fire",
			history: []watcher.CorrelatorEvent{
				nodeNotReady("ns", "node-2", "KubeletNotReady"),
			},
			trigger:  podEvicted("ns", "pod-a", "node-1"),
			wantFire: false,
		},
		{
			name:     "NodeNotReady only — no fire",
			history:  []watcher.CorrelatorEvent{},
			trigger:  nodeNotReady("ns", "node-1", "KubeletNotReady"),
			wantFire: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := make([]Entry, len(tc.history))
			for i, e := range tc.history {
				entries[i] = Entry{Event: e, AddedAt: testNow}
			}
			result := ruleNodeNotReadyPlusEviction(tc.trigger, entries)
			if result.Fired != tc.wantFire {
				t.Fatalf("Fired=%v want %v", result.Fired, tc.wantFire)
			}
			if tc.wantFire {
				if result.Severity != "P1" {
					t.Errorf("Severity=%q want P1", result.Severity)
				}
				if result.ScopeLevel != "Cluster" {
					t.Errorf("ScopeLevel=%q want Cluster", result.ScopeLevel)
				}
				if result.Rule != "NodeNotReadyPlusEviction" {
					t.Errorf("Rule=%q want NodeNotReadyPlusEviction", result.Rule)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Correlator: Evaluate ordering and no-fire behaviour
// ─────────────────────────────────────────────────────────────────────────────

// TestCorrelator_Rule5TakesPrecedence verifies that the P1 NodeNotReady+Eviction
// rule still fires correctly from genuine node-level signals.
func TestCorrelator_Rule5TakesPrecedence(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return testNow }

	// Seed: evicted pod on node-1 (triggers rule 5 when NodeNotReady arrives).
	corr.Add(podEvicted("ns", "pod-a", "node-1"))

	// Trigger: NodeNotReady for node-1.
	// Rule 5 (P1) must fire.
	result := corr.Evaluate(nodeNotReady("ns", "node-1", "DiskPressure"))
	if !result.Fired {
		t.Fatal("expected a rule to fire")
	}
	if result.Rule != "NodeNotReadyPlusEviction" {
		t.Errorf("expected Rule5 to win, got rule=%q", result.Rule)
	}
	if result.Severity != "P1" {
		t.Errorf("expected P1 severity, got %q", result.Severity)
	}
}

// TestCorrelator_DoesNotRaiseNodeFailureFromPodCoFailure verifies that
// multiple pod failures on the same node no longer synthesize a NodeFailure
// incident without an actual node signal such as NodeNotReady.
func TestCorrelator_DoesNotRaiseNodeFailureFromPodCoFailure(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return testNow }

	corr.Add(crashLoop("ns", "pod-a", "node-1", "app", 3))
	corr.Add(oomKilled("ns", "pod-b", "node-1", "app"))

	result := corr.Evaluate(crashLoop("ns", "pod-c", "node-1", "app", 3))
	if result.Fired && result.ScopeLevel == "Cluster" {
		t.Fatalf("unexpected NodeFailure from pod co-failure: %+v", result)
	}
}

// TestCorrelator_NoFire verifies that no spurious result is returned when
// there are no correlated events for the incoming trigger.
func TestCorrelator_NoFire(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return testNow }

	// Buffer contains an event in a completely different namespace.
	corr.Add(oomKilled("other-ns", "pod-x", "node-1", "app"))

	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if result.Fired {
		t.Errorf("expected no rule to fire, got rule=%q", result.Rule)
	}
}

// TestCorrelator_WindowExpiry verifies that events outside the correlation
// window do not trigger rules.
func TestCorrelator_WindowExpiry(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)

	tick := testNow
	corr.buf.nowFn = func() time.Time { return tick }

	// Add OOM event at t=0.
	corr.Add(oomKilled("ns", "pod-a", "node-1", "app"))

	// Advance time past the window.
	tick = testNow.Add(6 * time.Minute)

	// Trigger CrashLoop for the same pod — OOM is now expired.
	result := corr.Evaluate(crashLoop("ns", "pod-a", "node-1", "app", 3))
	if result.Fired {
		t.Errorf("expected no rule to fire after window expiry, got rule=%q", result.Rule)
	}
}

// TestCorrelator_Add_IsReflectedInEvaluate verifies the end-to-end Add → Evaluate flow.
func TestCorrelator_Add_IsReflectedInEvaluate(t *testing.T) {
	corr := NewCorrelator(5 * time.Minute)
	corr.buf.nowFn = func() time.Time { return testNow }

	// Add CrashLoop first.
	corr.Add(crashLoop("ns", "pod-a", "node-1", "app", 3))

	// Now add OOM for the same pod — rule 1 should fire.
	corr.Add(oomKilled("ns", "pod-a", "node-1", "app"))
	result := corr.Evaluate(oomKilled("ns", "pod-a", "node-1", "app"))
	if !result.Fired {
		t.Fatal("expected rule 1 to fire")
	}
	if result.Rule != "CrashLoopPlusOOM" {
		t.Errorf("expected CrashLoopPlusOOM, got %q", result.Rule)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper function tests
// ─────────────────────────────────────────────────────────────────────────────

func TestExtractNodeForFailure(t *testing.T) {
	cases := []struct {
		event    watcher.CorrelatorEvent
		wantNode string
	}{
		{crashLoop("ns", "pod", "node-1", "c", 1), "node-1"},
		{oomKilled("ns", "pod", "node-2", "c"), "node-2"},
		{podEvicted("ns", "pod", "node-3"), "node-3"},
		{imagePull("ns", "pod", "c", "err"), ""},     // no node
		{nodeNotReady("ns", "node-4", "reason"), ""}, // not a pod failure type
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			got := extractNodeForFailure(tc.event)
			if got != tc.wantNode {
				t.Errorf("extractNodeForFailure(%T) = %q, want %q", tc.event, got, tc.wantNode)
			}
		})
	}
}

func TestFailurePodKey(t *testing.T) {
	cases := []struct {
		event   watcher.CorrelatorEvent
		wantKey string
	}{
		{crashLoop("ns", "pod-a", "node", "c", 1), "ns/pod-a"},
		{oomKilled("ns", "pod-b", "node", "c"), "ns/pod-b"},
		{podEvicted("ns", "pod-c", "node"), "ns/pod-c"},
		{imagePull("ns", "pod-d", "c", "err"), ""},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			got := failurePodKey(tc.event)
			if got != tc.wantKey {
				t.Errorf("failurePodKey(%T) = %q, want %q", tc.event, got, tc.wantKey)
			}
		})
	}
}
