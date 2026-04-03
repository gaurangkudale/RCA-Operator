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
	triggerCounts := map[string]int{"CrashLoopBackOff": 3, "OOMKilled": 2}

	acc.Record(pairs, triggerCounts)

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
	if rec.TriggerCount != 3 {
		t.Errorf("expected trigger count=3, got %d", rec.TriggerCount)
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
	triggerCounts := map[string]int{"CrashLoopBackOff": 2}

	acc.nowFn = func() time.Time { return tick1 }
	acc.Record(pairs, triggerCounts)

	acc.nowFn = func() time.Time { return tick2 }
	acc.Record(pairs, triggerCounts)

	rec := acc.All()["CrashLoopBackOff:OOMKilled:samePod"]
	if rec.Occurrences != 2 {
		t.Errorf("expected 2 occurrences, got %d", rec.Occurrences)
	}
	if rec.TriggerCount != 4 {
		t.Errorf("expected trigger count=4 (2+2), got %d", rec.TriggerCount)
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
	acc.Record(pairs, map[string]int{"A": 5})

	rec := acc.All()["A:B:samePod"]
	if rec.Occurrences != 1 {
		t.Errorf("expected 1 occurrence (deduped within tick), got %d", rec.Occurrences)
	}
}

func TestAccumulator_ReadyPatterns(t *testing.T) {
	acc := NewAccumulator()
	t0 := time.Now()

	cfg := Config{
		MinOccurrences:      3,
		MinTimeSpan:         5 * time.Minute,
		ConfidenceThreshold: 0.5,
	}

	pair := EventPair{TriggerType: "A", ConditionType: "B", Scope: "samePod"}

	// Tick 1: not enough occurrences yet.
	acc.nowFn = func() time.Time { return t0 }
	acc.Record([]EventPair{pair}, map[string]int{"A": 1})
	if len(acc.ReadyPatterns(cfg)) != 0 {
		t.Error("expected no ready patterns after 1 tick")
	}

	// Tick 2-3: enough occurrences but not enough time span.
	acc.nowFn = func() time.Time { return t0.Add(time.Minute) }
	acc.Record([]EventPair{pair}, map[string]int{"A": 1})
	acc.nowFn = func() time.Time { return t0.Add(2 * time.Minute) }
	acc.Record([]EventPair{pair}, map[string]int{"A": 1})
	if len(acc.ReadyPatterns(cfg)) != 0 {
		t.Error("expected no ready patterns (time span too short)")
	}

	// Tick 4-5: enough occurrences AND time span.
	acc.nowFn = func() time.Time { return t0.Add(4 * time.Minute) }
	acc.Record([]EventPair{pair}, map[string]int{"A": 1})
	acc.nowFn = func() time.Time { return t0.Add(6 * time.Minute) }
	acc.Record([]EventPair{pair}, map[string]int{"A": 1})

	ready := acc.ReadyPatterns(cfg)
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready pattern, got %d", len(ready))
	}
	if ready[0].Pair.Key() != "A:B:samePod" {
		t.Errorf("unexpected ready pattern: %+v", ready[0])
	}
}

func TestAccumulator_ReadyPatterns_LowConfidence(t *testing.T) {
	acc := NewAccumulator()
	t0 := time.Now()

	cfg := Config{
		MinOccurrences:      2,
		MinTimeSpan:         time.Minute,
		ConfidenceThreshold: 0.8,
	}

	pair := EventPair{TriggerType: "A", ConditionType: "B", Scope: "samePod"}

	// Record 2 co-occurrences but with 10 trigger counts → confidence = 2/10 = 0.2.
	acc.nowFn = func() time.Time { return t0 }
	acc.Record([]EventPair{pair}, map[string]int{"A": 5})
	acc.nowFn = func() time.Time { return t0.Add(2 * time.Minute) }
	acc.Record([]EventPair{pair}, map[string]int{"A": 5})

	if len(acc.ReadyPatterns(cfg)) != 0 {
		t.Error("expected no ready patterns (confidence too low)")
	}
}

func TestAccumulator_Seed(t *testing.T) {
	acc := NewAccumulator()

	rec := &PatternRecord{
		Pair:         EventPair{TriggerType: "A", ConditionType: "B", Scope: "samePod"},
		FirstSeen:    time.Now().Add(-time.Hour),
		LastSeen:     time.Now().Add(-5 * time.Minute),
		Occurrences:  10,
		TriggerCount: 15,
		RuleName:     "auto-a-b-samepod",
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

func TestPatternRecord_Confidence(t *testing.T) {
	tests := []struct {
		occurrences  int
		triggerCount int
		want         float64
	}{
		{5, 10, 0.5},
		{10, 10, 1.0},
		{0, 10, 0.0},
		{0, 0, 0.0},
		{7, 10, 0.7},
	}
	for _, tt := range tests {
		rec := &PatternRecord{Occurrences: tt.occurrences, TriggerCount: tt.triggerCount}
		got := rec.Confidence()
		if got != tt.want {
			t.Errorf("Confidence(%d/%d) = %f, want %f", tt.occurrences, tt.triggerCount, got, tt.want)
		}
	}
}
