package autodetect

import (
	"testing"
	"time"
)

func TestAccumulator_Record(t *testing.T) {
	acc := NewAccumulator()
	now := time.Now()
	acc.nowFn = func() time.Time { return now }

	pairs := []EventPair{
		{TriggerType: "CrashLoopBackOff", ConditionType: "OOMKilled", Scope: "samePod"},
	}

	acc.Record(pairs)

	all := acc.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(all))
	}

	rec := all["CrashLoopBackOff:OOMKilled:samePod"]
	if rec == nil {
		t.Fatal("expected pattern not found")
	}
	if rec.Occurrences != 1 {
		t.Errorf("expected 1 occurrence, got %d", rec.Occurrences)
	}
	if !rec.FirstSeen.Equal(now) {
		t.Errorf("expected FirstSeen=%v, got %v", now, rec.FirstSeen)
	}
}

func TestAccumulator_Record_MultipleTicks(t *testing.T) {
	acc := NewAccumulator()
	tick1 := time.Now()
	tick2 := tick1.Add(2 * time.Minute)

	pairs := []EventPair{
		{TriggerType: "CrashLoopBackOff", ConditionType: "OOMKilled", Scope: "samePod"},
	}

	acc.nowFn = func() time.Time { return tick1 }
	acc.Record(pairs)

	acc.nowFn = func() time.Time { return tick2 }
	acc.Record(pairs)

	rec := acc.All()["CrashLoopBackOff:OOMKilled:samePod"]
	if rec.Occurrences != 2 {
		t.Errorf("expected 2 occurrences, got %d", rec.Occurrences)
	}
	if !rec.FirstSeen.Equal(tick1) {
		t.Errorf("expected FirstSeen=%v, got %v", tick1, rec.FirstSeen)
	}
	if !rec.LastSeen.Equal(tick2) {
		t.Errorf("expected LastSeen=%v, got %v", tick2, rec.LastSeen)
	}
}

func TestAccumulator_Record_DedupWithinTick(t *testing.T) {
	acc := NewAccumulator()
	acc.nowFn = func() time.Time { return time.Now() }

	// Same pair appearing twice in one tick should only count once.
	pairs := []EventPair{
		{TriggerType: "A", ConditionType: "B", Scope: "samePod"},
		{TriggerType: "A", ConditionType: "B", Scope: "samePod"},
	}
	acc.Record(pairs)

	rec := acc.All()["A:B:samePod"]
	if rec.Occurrences != 1 {
		t.Errorf("expected 1 occurrence (deduped within tick), got %d", rec.Occurrences)
	}
}

func TestAccumulator_ReadyPatterns(t *testing.T) {
	acc := NewAccumulator()
	t0 := time.Now()

	cfg := Config{
		MinOccurrences: 3,
		MinTimeSpan:    5 * time.Minute,
	}

	pair := EventPair{TriggerType: "A", ConditionType: "B", Scope: "samePod"}

	// Tick 1: not enough occurrences yet.
	acc.nowFn = func() time.Time { return t0 }
	acc.Record([]EventPair{pair})
	if len(acc.ReadyPatterns(cfg)) != 0 {
		t.Error("expected no ready patterns after 1 tick")
	}

	// Tick 2-3: enough occurrences but not enough time span.
	acc.nowFn = func() time.Time { return t0.Add(time.Minute) }
	acc.Record([]EventPair{pair})
	acc.nowFn = func() time.Time { return t0.Add(2 * time.Minute) }
	acc.Record([]EventPair{pair})
	if len(acc.ReadyPatterns(cfg)) != 0 {
		t.Error("expected no ready patterns (time span too short)")
	}

	// Tick 4-5: enough occurrences AND time span.
	acc.nowFn = func() time.Time { return t0.Add(4 * time.Minute) }
	acc.Record([]EventPair{pair})
	acc.nowFn = func() time.Time { return t0.Add(6 * time.Minute) }
	acc.Record([]EventPair{pair})

	ready := acc.ReadyPatterns(cfg)
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready pattern, got %d", len(ready))
	}
	if ready[0].Pair.Key() != "A:B:samePod" {
		t.Errorf("unexpected ready pattern: %+v", ready[0])
	}
}

func TestAccumulator_Seed(t *testing.T) {
	acc := NewAccumulator()

	rec := &PatternRecord{
		Pair:        EventPair{TriggerType: "A", ConditionType: "B", Scope: "samePod"},
		FirstSeen:   time.Now().Add(-time.Hour),
		LastSeen:    time.Now().Add(-5 * time.Minute),
		Occurrences: 10,
		RuleName:    "auto-a-b-samepod",
	}

	acc.Seed("A:B:samePod", rec)
	if acc.Count() != 1 {
		t.Errorf("expected 1 pattern after seed, got %d", acc.Count())
	}

	// Seeding again should not overwrite.
	rec2 := &PatternRecord{Occurrences: 999}
	acc.Seed("A:B:samePod", rec2)

	got := acc.All()["A:B:samePod"]
	if got.Occurrences != 10 {
		t.Errorf("expected original occurrences=10, got %d", got.Occurrences)
	}
}
