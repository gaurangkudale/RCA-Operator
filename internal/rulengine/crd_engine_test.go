package rulengine

import (
	"testing"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// ---- helpers ---------------------------------------------------------------

func spanErr(node, traceID, spanID string, attrs map[string]string) watcher.OTelSpanErrorEvent {
	return watcher.OTelSpanErrorEvent{
		BaseEvent:   watcher.BaseEvent{At: time.Now(), Namespace: "demo", PodName: "api-0", NodeName: node},
		TraceID:     traceID,
		SpanID:      spanID,
		ServiceName: "checkout",
		SpanName:    "POST /order",
		SpanKind:    "SERVER",
		StatusCode:  "STATUS_CODE_ERROR",
		Attrs:       attrs,
	}
}

func logMatch(traceID, body string, sevNum int32, sevText string) watcher.OTelLogMatchEvent {
	return watcher.OTelLogMatchEvent{
		BaseEvent:   watcher.BaseEvent{At: time.Now(), Namespace: "demo", PodName: "api-0"},
		TraceID:     traceID,
		ServiceName: "checkout",
		Severity:    sevText,
		SeverityNum: sevNum,
		Body:        body,
		BodyHash:    "deadbeefcafef00d",
	}
}

func bufFor(events ...watcher.CorrelatorEvent) []correlator.Entry {
	out := make([]correlator.Entry, 0, len(events))
	for _, e := range events {
		out = append(out, correlator.Entry{Event: e, AddedAt: time.Now()})
	}
	return out
}

// ---- attribute predicate tests ---------------------------------------------

func TestEvaluateAttribute_EqualsAndDefaultOp(t *testing.T) {
	attrs := map[string]string{"http.status_code": "503"}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "http.status_code", Op: "Equals", Value: "503"}) {
		t.Error("Equals 503 should match")
	}
	// Empty Op defaults to Equals.
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "http.status_code", Value: "503"}) {
		t.Error("default (empty) Op should behave as Equals")
	}
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "http.status_code", Op: "Equals", Value: "200"}) {
		t.Error("Equals 200 should not match")
	}
}

func TestEvaluateAttribute_NotEquals(t *testing.T) {
	attrs := map[string]string{"service.name": "checkout"}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "service.name", Op: "NotEquals", Value: "auth"}) {
		t.Error("NotEquals auth should match")
	}
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "service.name", Op: "NotEquals", Value: "checkout"}) {
		t.Error("NotEquals checkout should not match")
	}
	// Absent key: NotEquals is satisfied vacuously (value is not equal to anything).
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "missing", Op: "NotEquals", Value: "x"}) {
		t.Error("NotEquals on absent key should match")
	}
}

func TestEvaluateAttribute_ContainsAndNotContains(t *testing.T) {
	attrs := map[string]string{"log.body": "payment failed for order 42"}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "log.body", Op: "Contains", Value: "payment"}) {
		t.Error("Contains payment should match")
	}
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "log.body", Op: "Contains", Value: "shipment"}) {
		t.Error("Contains shipment should not match")
	}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "log.body", Op: "NotContains", Value: "shipment"}) {
		t.Error("NotContains shipment should match")
	}
}

func TestEvaluateAttribute_Regex(t *testing.T) {
	attrs := map[string]string{"span.name": "GET /api/v1/orders"}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "span.name", Op: "Regex", Value: `^GET /api/v\d+/.+$`}) {
		t.Error("Regex should match versioned API path")
	}
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "span.name", Op: "Regex", Value: `^POST`}) {
		t.Error("Regex ^POST should not match GET span")
	}
	// Invalid regex returns false, does not panic.
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "span.name", Op: "Regex", Value: `[invalid(`}) {
		t.Error("invalid regex should evaluate to false")
	}
}

func TestEvaluateAttribute_ExistsAndNotExists(t *testing.T) {
	attrs := map[string]string{"trace.id": "abc123", "empty.attr": ""}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "trace.id", Op: "Exists"}) {
		t.Error("Exists trace.id should match")
	}
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "missing", Op: "Exists"}) {
		t.Error("Exists on missing key should not match")
	}
	// Empty string is not "present" for Exists semantics.
	if evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "empty.attr", Op: "Exists"}) {
		t.Error("Exists on empty-string attribute should not match")
	}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "missing", Op: "NotExists"}) {
		t.Error("NotExists on missing key should match")
	}
	if !evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: "empty.attr", Op: "NotExists"}) {
		t.Error("NotExists on empty attribute should match")
	}
}

func TestEvaluateAttribute_NumericComparisons(t *testing.T) {
	attrs := map[string]string{"http.status_code": "503", "duration.ms": "1250"}
	cases := []struct {
		op       string
		key      string
		value    string
		expected bool
	}{
		{"Gte", "http.status_code", "500", true},
		{"Gte", "http.status_code", "600", false},
		{"Gt", "http.status_code", "503", false},
		{"Gt", "http.status_code", "502", true},
		{"Lte", "duration.ms", "2000", true},
		{"Lt", "duration.ms", "1250", false},
		{"Lt", "duration.ms", "1251", true},
	}
	for _, c := range cases {
		got := evaluateAttribute(attrs, rcav1alpha1.AttributeMatch{Key: c.key, Op: c.op, Value: c.value})
		if got != c.expected {
			t.Errorf("%s %s %s: got %v want %v", c.key, c.op, c.value, got, c.expected)
		}
	}
	// Non-numeric values short-circuit to false rather than panic.
	if evaluateAttribute(map[string]string{"foo": "bar"}, rcav1alpha1.AttributeMatch{Key: "foo", Op: "Gte", Value: "1"}) {
		t.Error("non-numeric lhs should fail numeric comparison")
	}
}

func TestEvaluateAttribute_UnknownOpReturnsFalse(t *testing.T) {
	if evaluateAttribute(map[string]string{"k": "v"}, rcav1alpha1.AttributeMatch{Key: "k", Op: "BogusOp", Value: "v"}) {
		t.Error("unknown op should yield false, not panic")
	}
}

// ---- attributesMatch tests -------------------------------------------------

func TestAttributesMatch_EmptyMatchesAlwaysTrue(t *testing.T) {
	ev := spanErr("node-a", "t1", "s1", nil)
	if !attributesMatch(ev, nil) {
		t.Error("empty matches should return true")
	}
}

func TestAttributesMatch_NonAttributesEventFailsUnlessNotExists(t *testing.T) {
	crash := watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "demo", PodName: "api-0"}, ContainerName: "api"}
	// A K8s-event rule that tries to match an OTel attribute should fail hard.
	matches := []rcav1alpha1.AttributeMatch{{Key: "http.status_code", Op: "Equals", Value: "500"}}
	if attributesMatch(crash, matches) {
		t.Error("CrashLoopBackOffEvent has no Attributes() — Equals should fail")
	}
	// NotExists is the one predicate that succeeds on an event without attributes.
	notExists := []rcav1alpha1.AttributeMatch{{Key: "http.status_code", Op: "NotExists"}}
	if !attributesMatch(crash, notExists) {
		t.Error("NotExists should succeed when event has no Attributes()")
	}
}

func TestAttributesMatch_AllPredicatesMustMatchAND(t *testing.T) {
	ev := spanErr("node-a", "t1", "s1", map[string]string{
		"http.status_code": "503",
	})
	// Two predicates both satisfied.
	ok := []rcav1alpha1.AttributeMatch{
		{Key: "http.status_code", Op: "Gte", Value: "500"},
		{Key: "service.name", Op: "Equals", Value: "checkout"}, // promoted field
	}
	if !attributesMatch(ev, ok) {
		t.Error("both predicates satisfied should return true")
	}
	// Second predicate fails → overall false.
	bad := []rcav1alpha1.AttributeMatch{
		{Key: "http.status_code", Op: "Gte", Value: "500"},
		{Key: "service.name", Op: "Equals", Value: "auth"},
	}
	if attributesMatch(ev, bad) {
		t.Error("AND logic: one failing predicate should flip result")
	}
}

// ---- conditionsMet integration -------------------------------------------

func TestConditionsMet_SameTraceAndAttributeFilter(t *testing.T) {
	eng := &CRDRuleEngine{}
	traceA := "aaaabbbbccccddddeeeeffff00001111"
	traceB := "2222333344445555666677778888aaaa"

	trigger := spanErr("node-1", traceA, "s1",
		map[string]string{"http.status_code": "503"})
	// Buffer entries: one log in the same trace (match), one in a different trace (no match).
	sameTraceLog := logMatch(traceA, "timeout calling db", 17, "ERROR")
	otherTraceLog := logMatch(traceB, "timeout calling db", 17, "ERROR")
	entries := bufFor(sameTraceLog, otherTraceLog)

	conditions := []rcav1alpha1.RuleCondition{{
		EventType: string(watcher.EventTypeOTelLogMatch),
		Scope:     "sameTrace",
		Attributes: []rcav1alpha1.AttributeMatch{
			{Key: "log.severity", Op: "Equals", Value: "ERROR"},
		},
	}}
	if !eng.conditionsMet(trigger, conditions, entries) {
		t.Fatal("sameTrace + log.severity=ERROR should match sameTraceLog")
	}

	// Remove the same-trace log — rule should no longer fire.
	if eng.conditionsMet(trigger, conditions, bufFor(otherTraceLog)) {
		t.Fatal("only different-trace log present: rule must not fire")
	}
}

func TestConditionsMet_NegateRespectsAttributes(t *testing.T) {
	eng := &CRDRuleEngine{}
	traceA := "aaaabbbbccccddddeeeeffff00001111"
	trigger := spanErr("node-1", traceA, "s1", nil)

	// A matching severity=ERROR log exists; negate=true should reject.
	entries := bufFor(logMatch(traceA, "boom", 17, "ERROR"))
	neg := []rcav1alpha1.RuleCondition{{
		EventType: string(watcher.EventTypeOTelLogMatch),
		Scope:     "sameTrace",
		Negate:    true,
		Attributes: []rcav1alpha1.AttributeMatch{
			{Key: "log.severity", Op: "Equals", Value: "ERROR"},
		},
	}}
	if eng.conditionsMet(trigger, neg, entries) {
		t.Error("negated condition should fail when matching entry exists")
	}

	// Only a FATAL severity log exists → ERROR predicate fails → negated succeeds.
	entries2 := bufFor(logMatch(traceA, "boom", 21, "FATAL"))
	if !eng.conditionsMet(trigger, neg, entries2) {
		t.Error("negated ERROR predicate should succeed when no ERROR log present")
	}
}

func TestSameTraceID_RequiresBothSides(t *testing.T) {
	// Event without trace ID (K8s event) should never match sameTrace.
	crash := watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "demo", PodName: "api-0"}}
	span := spanErr("node-1", "t1", "s1", nil)
	if sameTraceID(crash, span) {
		t.Error("K8s event vs OTel span: sameTrace should not match")
	}
	// Two spans in same trace: match.
	spanA := spanErr("node-1", "t1", "s1", nil)
	spanB := spanErr("node-1", "t1", "s2", nil)
	if !sameTraceID(spanA, spanB) {
		t.Error("two spans in same trace should match")
	}
	// Different traces: no match.
	spanC := spanErr("node-1", "t2", "s3", nil)
	if sameTraceID(spanA, spanC) {
		t.Error("different trace IDs should not match")
	}
}

func TestExtractBase_CoversOTelEvents(t *testing.T) {
	span := spanErr("node-1", "t1", "s1", nil)
	if b := ExtractBase(span); b.Namespace != "demo" || b.PodName != "api-0" || b.NodeName != "node-1" {
		t.Errorf("OTelSpanErrorEvent base not extracted: %+v", b)
	}
	latency := watcher.OTelSpanLatencySpikeEvent{BaseEvent: watcher.BaseEvent{Namespace: "demo", PodName: "api-0"}}
	if b := ExtractBase(latency); b.Namespace != "demo" {
		t.Errorf("OTelSpanLatencySpikeEvent base not extracted: %+v", b)
	}
	log := watcher.OTelLogMatchEvent{BaseEvent: watcher.BaseEvent{Namespace: "demo", PodName: "api-0"}}
	if b := ExtractBase(log); b.Namespace != "demo" {
		t.Errorf("OTelLogMatchEvent base not extracted: %+v", b)
	}
	se := watcher.OTelSpanEventEvent{BaseEvent: watcher.BaseEvent{Namespace: "demo", PodName: "api-0"}}
	if b := ExtractBase(se); b.Namespace != "demo" {
		t.Errorf("OTelSpanEventEvent base not extracted: %+v", b)
	}
}
