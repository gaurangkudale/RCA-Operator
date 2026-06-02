package dashboard

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

const nsPerMs = int64(1_000_000)

func TestTraceSummaryFromBuffer(t *testing.T) {
	buf := correlator.NewBuffer(5 * time.Minute)
	buf.Add(watcher.OTelSpanErrorEvent{ServiceName: "payment", TraceID: "t1", DurationNanos: 800 * nsPerMs})
	buf.Add(watcher.OTelSpanErrorEvent{ServiceName: "payment", TraceID: "t2", DurationNanos: 600 * nsPerMs})
	buf.Add(watcher.OTelSpanLatencySpikeEvent{ServiceName: "payment", TraceID: "t3", DurationNanos: 1200 * nsPerMs})
	buf.Add(watcher.OTelLogMatchEvent{ServiceName: "payment", TraceID: "t1"})
	buf.Add(watcher.OTelSpanErrorEvent{ServiceName: "checkout", TraceID: "t4", DurationNanos: 300 * nsPerMs})

	s := &Server{buffer: buf}
	resp, ok := s.traceSummaryFromBuffer(15 * time.Minute)
	if !ok {
		t.Fatal("expected ok=true with buffered OTel events")
	}
	if resp.Source != "live" {
		t.Errorf("source = %q, want live", resp.Source)
	}
	if resp.WindowSeconds != 300 {
		t.Errorf("windowSeconds = %d, want 300 (clamped to 5m buffer window)", resp.WindowSeconds)
	}
	if resp.ErrorTraces != 4 {
		t.Errorf("errorTraces = %d, want 4", resp.ErrorTraces)
	}
	if resp.ServicesWithErrors != 2 {
		t.Errorf("servicesWithErrors = %d, want 2", resp.ServicesWithErrors)
	}
	if resp.AvgCriticalPathMs == nil || *resp.AvgCriticalPathMs != 725 {
		t.Errorf("avgCriticalPathMs = %v, want 725", resp.AvgCriticalPathMs)
	}
	if len(resp.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(resp.Services))
	}
	p := resp.Services[0]
	if p.Name != "payment" || p.Errors != 3 {
		t.Errorf("services[0] = %+v, want payment with Errors=3", p)
	}
	if p.ErrorPct != 50 || p.SlowPct != 25 || p.SpanPct != 25 {
		t.Errorf("payment pcts = error %d / slow %d / span %d, want 50/25/25", p.ErrorPct, p.SlowPct, p.SpanPct)
	}
	if p.LatencyMs == nil || *p.LatencyMs != 866 {
		t.Errorf("payment latencyMs = %v, want 866", p.LatencyMs)
	}
	if resp.Services[1].Name != "checkout" {
		t.Errorf("services[1].Name = %q, want checkout", resp.Services[1].Name)
	}
}

func TestTraceSummaryFromBufferEmptyFallsBack(t *testing.T) {
	s := &Server{buffer: correlator.NewBuffer(5 * time.Minute)}
	if _, ok := s.traceSummaryFromBuffer(15 * time.Minute); ok {
		t.Error("expected ok=false for an empty buffer so the caller falls back to incidents")
	}
}

func TestComputeTraceSummaryFromIncidents(t *testing.T) {
	now := time.Now()
	mk := func(name, typ, svc, trace string, age time.Duration) *rcav1alpha1.IncidentReport {
		return &rcav1alpha1.IncidentReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   "rca-demo",
				Annotations: map[string]string{"rca.rca-operator.tech/trace-id": trace},
			},
			Spec: rcav1alpha1.IncidentReportSpec{
				IncidentType: typ,
				Scope:        rcav1alpha1.IncidentScope{WorkloadRef: &rcav1alpha1.IncidentObjectRef{Kind: "Deployment", Name: svc}},
			},
			Status: rcav1alpha1.IncidentReportStatus{LastObservedAt: &metav1.Time{Time: now.Add(-age)}},
		}
	}
	s := newTestServer(t,
		mk("i1", "OTelSpanError", "payment", "t1", 1*time.Minute),
		mk("i2", "OTelLogMatch", "payment", "t2", 2*time.Minute),
		mk("i3", "OTelSpanError", "checkout", "t3", 3*time.Minute),
		mk("i4", "OTelSpanError", "payment", "t8", 40*time.Minute),  // outside 15m window → excluded
		mk("i5", "CrashLoopBackOff", "worker", "t9", 1*time.Minute), // non-OTel → excluded
	)
	// buffer is nil → computeTraceSummary uses the incident fallback.
	resp, err := s.computeTraceSummary(context.Background(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "incidents" {
		t.Errorf("source = %q, want incidents", resp.Source)
	}
	if resp.AvgCriticalPathMs != nil {
		t.Errorf("avgCriticalPathMs = %v, want nil (latency unavailable from incidents)", resp.AvgCriticalPathMs)
	}
	if resp.ErrorTraces != 3 {
		t.Errorf("errorTraces = %d, want 3 (t1,t2,t3; t8/t9 excluded)", resp.ErrorTraces)
	}
	if resp.ServicesWithErrors != 2 {
		t.Errorf("servicesWithErrors = %d, want 2", resp.ServicesWithErrors)
	}
	if len(resp.Services) != 2 {
		t.Fatalf("services = %d, want 2 (payment, checkout)", len(resp.Services))
	}
	byName := map[string]traceServiceSummary{}
	for _, svc := range resp.Services {
		byName[svc.Name] = svc
	}
	pay, ok := byName["payment"]
	if !ok {
		t.Fatal("expected a payment service entry")
	}
	if pay.ErrorPct != 50 || pay.SpanPct != 50 {
		t.Errorf("payment pcts = error %d / span %d, want 50/50 (1 span-error + 1 log)", pay.ErrorPct, pay.SpanPct)
	}
	if pay.LatencyMs != nil {
		t.Errorf("payment latencyMs = %v, want nil", pay.LatencyMs)
	}
	if _, ok := byName["worker"]; ok {
		t.Error("non-OTel incident's service should be excluded")
	}
}

func TestClassifyOTelIncidentType(t *testing.T) {
	cases := map[string]string{
		"OTelSpanError":        "error",
		"OTelSpanLatencySpike": "slow",
		"OTelSlowSpan":         "slow",
		"OTelLogMatch":         "log",
		"OTelSpanEvent":        "error",
	}
	for in, want := range cases {
		if got := classifyOTelIncidentType(in); got != want {
			t.Errorf("classifyOTelIncidentType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPctOfAndAvgNs(t *testing.T) {
	if got := pctOf(1, 3); got != 33 {
		t.Errorf("pctOf(1,3) = %d, want 33", got)
	}
	if got := pctOf(2, 3); got != 67 {
		t.Errorf("pctOf(2,3) = %d, want 67", got)
	}
	if got := pctOf(0, 0); got != 0 {
		t.Errorf("pctOf(0,0) = %d, want 0", got)
	}
	if got := avgNs([]int64{100 * nsPerMs, 300 * nsPerMs}); got != 200*nsPerMs {
		t.Errorf("avgNs = %d, want %d", got, 200*nsPerMs)
	}
	if got := avgNs(nil); got != 0 {
		t.Errorf("avgNs(nil) = %d, want 0", got)
	}
}

func TestBuildTraceServicesCapsAndSorts(t *testing.T) {
	bySvc := map[string]*traceAgg{}
	for i, n := range []int{1, 9, 5, 3, 7, 2, 8, 4, 6, 10} {
		bySvc[string(rune('a'+i))] = &traceAgg{errors: n}
	}
	resp := buildTraceServices(bySvc, true)
	if len(resp.Services) != maxTraceSummaryServices {
		t.Fatalf("services = %d, want capped at %d", len(resp.Services), maxTraceSummaryServices)
	}
	if resp.Services[0].Errors != 10 || resp.Services[1].Errors != 9 {
		t.Errorf("not sorted by Errors desc: got %d,%d", resp.Services[0].Errors, resp.Services[1].Errors)
	}
}
