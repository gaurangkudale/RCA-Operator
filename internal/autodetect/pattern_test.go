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

	// Canonical pair ordering: one pair per unique combination (no mirror duplicates).
	if len(result.Pairs) != 1 {
		t.Fatalf("expected 1 canonical pair, got %d: %+v", len(result.Pairs), result.Pairs)
	}

	p := result.Pairs[0]
	if p.Scope != ScopeSamePod {
		t.Errorf("expected samePod scope, got %q", p.Scope)
	}
	// Trigger should be lexicographically first.
	if p.TriggerType != "CrashLoopBackOff" || p.ConditionType != "OOMKilled" {
		t.Errorf("expected CrashLoopBackOff→OOMKilled, got %s→%s", p.TriggerType, p.ConditionType)
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
	// Canonical ordering: NodeNotReady < PodEvicted lexicographically.
	found := false
	for _, p := range result.Pairs {
		if p.TriggerType == "NodeNotReady" && p.ConditionType == "PodEvicted" && p.Scope == ScopeSameNode {
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
		if p.Scope != ScopeSamePod {
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
		// Different pod, same node. Pod-level events should still stay in samePod scope.
		makeEntry(watcher.EventTypeProbeFailure, "default", "pod-2", "node-1", now.Add(2*time.Second)),
	}

	result := MinePatterns(entries)

	if len(result.Pairs) != 1 {
		t.Fatalf("expected exactly 1 pair, got %d: %+v", len(result.Pairs), result.Pairs)
	}

	// Only CrashLoopBackOff↔OOMKilled should be emitted at samePod scope.
	// Pod-level cross-pod events must NOT emit sameNode correlations.
	scopes := make(map[string]string)
	for _, p := range result.Pairs {
		scopes[p.TriggerType+":"+p.ConditionType] = p.Scope
	}

	if s, ok := scopes["CrashLoopBackOff:OOMKilled"]; ok && s != ScopeSamePod {
		t.Errorf("CrashLoopBackOff:OOMKilled expected samePod, got %s", s)
	}
	if _, ok := scopes["CrashLoopBackOff:ProbeFailure"]; ok {
		t.Error("unexpected CrashLoopBackOff:ProbeFailure pair for pod-level cross-pod events")
	}
	if _, ok := scopes["OOMKilled:ProbeFailure"]; ok {
		t.Error("unexpected OOMKilled:ProbeFailure pair for pod-level cross-pod events")
	}
}

func TestMinePatterns_EmptyBuffer(t *testing.T) {
	result := MinePatterns(nil)
	if len(result.Pairs) != 0 {
		t.Errorf("expected 0 pairs for empty buffer, got %d", len(result.Pairs))
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
