package autodetect

import (
	"testing"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func makeEntry(eventType watcher.EventType, ns, pod, node string, at time.Time) correlator.Entry {
	base := watcher.BaseEvent{
		At:        at,
		Namespace: ns,
		PodName:   pod,
		NodeName:  node,
	}
	switch eventType {
	case watcher.EventTypeCrashLoopBackOff:
		return correlator.Entry{
			Event:   watcher.CrashLoopBackOffEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypeOOMKilled:
		return correlator.Entry{
			Event:   watcher.OOMKilledEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypeNodeNotReady:
		return correlator.Entry{
			Event:   watcher.NodeNotReadyEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypePodEvicted:
		return correlator.Entry{
			Event:   watcher.PodEvictedEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypeImagePullBackOff:
		return correlator.Entry{
			Event:   watcher.ImagePullBackOffEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypePodHealthy:
		return correlator.Entry{
			Event:   watcher.PodHealthyEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypePodDeleted:
		return correlator.Entry{
			Event:   watcher.PodDeletedEvent{BaseEvent: base},
			AddedAt: at,
		}
	case watcher.EventTypeProbeFailure:
		return correlator.Entry{
			Event:   watcher.ProbeFailureEvent{BaseEvent: base},
			AddedAt: at,
		}
	default:
		return correlator.Entry{
			Event:   watcher.CrashLoopBackOffEvent{BaseEvent: base},
			AddedAt: at,
		}
	}
}

func TestMinePatterns_SamePod(t *testing.T) {
	now := time.Now()
	entries := []correlator.Entry{
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-1", "node-1", now),
		makeEntry(watcher.EventTypeOOMKilled, "default", "pod-1", "node-1", now.Add(time.Second)),
	}

	result := MinePatterns(entries)

	if len(result.Pairs) != 2 {
		t.Fatalf("expected 2 pairs (A→B and B→A), got %d", len(result.Pairs))
	}

	// Both should be samePod scope (tightest).
	for _, p := range result.Pairs {
		if p.Scope != "samePod" {
			t.Errorf("expected samePod scope, got %q for %s→%s", p.Scope, p.TriggerType, p.ConditionType)
		}
	}

	// Check trigger counts.
	if result.TriggerCounts["CrashLoopBackOff"] != 1 {
		t.Errorf("expected CrashLoopBackOff count=1, got %d", result.TriggerCounts["CrashLoopBackOff"])
	}
	if result.TriggerCounts["OOMKilled"] != 1 {
		t.Errorf("expected OOMKilled count=1, got %d", result.TriggerCounts["OOMKilled"])
	}
}

func TestMinePatterns_SameNode(t *testing.T) {
	now := time.Now()
	entries := []correlator.Entry{
		makeEntry(watcher.EventTypeNodeNotReady, "default", "", "node-1", now),
		makeEntry(watcher.EventTypePodEvicted, "kube-system", "pod-a", "node-1", now.Add(time.Second)),
	}

	result := MinePatterns(entries)

	// Different pods (one has no pod name), same node → sameNode scope.
	found := false
	for _, p := range result.Pairs {
		if p.TriggerType == "NodeNotReady" && p.ConditionType == "PodEvicted" && p.Scope == "sameNode" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NodeNotReady→PodEvicted at sameNode scope, got pairs: %+v", result.Pairs)
	}
}

func TestMinePatterns_SkipsLifecycleEvents(t *testing.T) {
	now := time.Now()
	entries := []correlator.Entry{
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-1", "", now),
		makeEntry(watcher.EventTypePodHealthy, "default", "pod-1", "", now.Add(time.Second)),
		makeEntry(watcher.EventTypePodDeleted, "default", "pod-1", "", now.Add(2*time.Second)),
	}

	result := MinePatterns(entries)

	// PodHealthy and PodDeleted should be skipped — only 1 non-lifecycle event type.
	if len(result.Pairs) != 0 {
		t.Errorf("expected 0 pairs (lifecycle events filtered), got %d: %+v", len(result.Pairs), result.Pairs)
	}
}

func TestMinePatterns_SingleEventType(t *testing.T) {
	now := time.Now()
	entries := []correlator.Entry{
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-1", "", now),
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-2", "", now.Add(time.Second)),
	}

	result := MinePatterns(entries)

	// Same event type on different pods → no pairs (need 2+ distinct types).
	if len(result.Pairs) != 0 {
		t.Errorf("expected 0 pairs for same event type, got %d", len(result.Pairs))
	}
}

func TestMinePatterns_TightestScopeWins(t *testing.T) {
	now := time.Now()
	// Same pod, same node, same namespace — should only get samePod.
	entries := []correlator.Entry{
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-1", "node-1", now),
		makeEntry(watcher.EventTypeOOMKilled, "default", "pod-1", "node-1", now.Add(time.Second)),
	}

	result := MinePatterns(entries)

	for _, p := range result.Pairs {
		if p.Scope != "samePod" {
			t.Errorf("expected only samePod scope (tightest), got %q for %s→%s", p.Scope, p.TriggerType, p.ConditionType)
		}
	}
}

func TestMinePatterns_MultiplePairsMultipleScopes(t *testing.T) {
	now := time.Now()
	entries := []correlator.Entry{
		// Same pod pair.
		makeEntry(watcher.EventTypeCrashLoopBackOff, "default", "pod-1", "node-1", now),
		makeEntry(watcher.EventTypeOOMKilled, "default", "pod-1", "node-1", now.Add(time.Second)),
		// Different pod, same node — should produce sameNode pairs for new type combos.
		makeEntry(watcher.EventTypeProbeFailure, "default", "pod-2", "node-1", now.Add(2*time.Second)),
	}

	result := MinePatterns(entries)

	if len(result.Pairs) == 0 {
		t.Fatal("expected at least one pair")
	}

	// CrashLoopBackOff↔OOMKilled should be samePod.
	// CrashLoopBackOff↔ProbeFailure and OOMKilled↔ProbeFailure should be sameNode
	// (since pod-1 and pod-2 are different pods but same node).
	scopes := make(map[string]string)
	for _, p := range result.Pairs {
		scopes[p.TriggerType+":"+p.ConditionType] = p.Scope
	}

	if s, ok := scopes["CrashLoopBackOff:OOMKilled"]; ok && s != "samePod" {
		t.Errorf("CrashLoopBackOff:OOMKilled expected samePod, got %s", s)
	}
}

func TestMinePatterns_EmptyBuffer(t *testing.T) {
	result := MinePatterns(nil)
	if len(result.Pairs) != 0 {
		t.Errorf("expected 0 pairs for empty buffer, got %d", len(result.Pairs))
	}
	if len(result.TriggerCounts) != 0 {
		t.Errorf("expected 0 trigger counts for empty buffer, got %d", len(result.TriggerCounts))
	}
}

func TestRuleName(t *testing.T) {
	tests := []struct {
		pair EventPair
		want string
	}{
		{
			pair: EventPair{TriggerType: "CrashLoopBackOff", ConditionType: "OOMKilled", Scope: "samePod"},
			want: "auto-crashloopbackoff-oomkilled-samepod",
		},
		{
			pair: EventPair{TriggerType: "NodeNotReady", ConditionType: "PodEvicted", Scope: "sameNode"},
			want: "auto-nodenotready-podevicted-samenode",
		},
	}
	for _, tt := range tests {
		got := RuleName(tt.pair)
		if got != tt.want {
			t.Errorf("RuleName(%+v) = %q, want %q", tt.pair, got, tt.want)
		}
	}
}

func TestRuleName_Truncation(t *testing.T) {
	pair := EventPair{
		TriggerType:   "VeryLongEventTypeName",
		ConditionType: "AnotherVeryLongEventTypeName",
		Scope:         "sameNamespace",
	}
	name := RuleName(pair)
	if len(name) > 63 {
		t.Errorf("expected name <= 63 chars, got %d: %q", len(name), name)
	}
}
