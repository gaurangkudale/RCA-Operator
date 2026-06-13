package dashboard

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// ── /api/trace-summary ──────────────────────────────────────────────────────
//
// Powers the dashboard's "Trace / Error Summary" panel. It aggregates recent
// OTel span/log signals into per-service error/latency stats.
//
// Source preference:
//  1. Live correlator buffer — richest (carries span durations → latency), but
//     only covers the buffer's sliding window (typically 5m).
//  2. Recent OTel IncidentReports — persistent fallback so the panel is still
//     populated when the buffer is empty; counts only (no span latency).
//
// Returns an empty services slice when neither source has data; the UI then
// renders a graceful empty-state.

const (
	defaultTraceSummaryLookback = 15 * time.Minute
	maxTraceSummaryLookback     = time.Hour
	maxTraceSummaryServices     = 8
)

// Signal kinds used to bucket OTel signals within a service aggregate.
const (
	traceKindError = "error" // span errors
	traceKindSlow  = "slow"  // latency spikes
	traceKindLog   = "log"   // logs / span-events (rendered as the grey "span" segment)
)

type traceSummaryResponse struct {
	Source             string                `json:"source"` // "live" | "incidents" | "none"
	WindowSeconds      int                   `json:"windowSeconds"`
	ErrorTraces        int                   `json:"errorTraces"`
	AvgCriticalPathMs  *int                  `json:"avgCriticalPathMs"`
	ServicesWithErrors int                   `json:"servicesWithErrors"`
	Services           []traceServiceSummary `json:"services"`
}

type traceServiceSummary struct {
	Name      string `json:"name"`
	Errors    int    `json:"errors"` // span errors + latency spikes (the red badge)
	SpanPct   int    `json:"spanPct"`
	ErrorPct  int    `json:"errorPct"`
	SlowPct   int    `json:"slowPct"`
	LatencyMs *int   `json:"latencyMs"`
}

// traceAgg accumulates per-service signal counts (and span durations when known).
type traceAgg struct {
	errors int     // OTel span errors
	slow   int     // OTel latency spikes
	other  int     // logs + span-events (rendered as the grey "span" segment)
	durNs  []int64 // span durations for error/slow signals (nanos)
}

func (s *Server) handleTraceSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lookback := defaultTraceSummaryLookback
	if v := r.URL.Query().Get("lookback"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d <= maxTraceSummaryLookback {
			lookback = d
		}
	}
	key := "trace-summary|" + lookback.String()
	body, etag, err := s.cache.Fetch(key, func() (any, error) {
		return s.computeTraceSummary(r.Context(), lookback)
	})
	if err != nil {
		s.log.Error(err, "compute trace summary")
		http.Error(w, "failed to compute trace summary", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

func (s *Server) computeTraceSummary(ctx context.Context, lookback time.Duration) (traceSummaryResponse, error) {
	if s.buffer != nil {
		if resp, ok := s.traceSummaryFromBuffer(lookback); ok {
			return resp, nil
		}
	}
	return s.traceSummaryFromIncidents(ctx, lookback)
}

// traceSummaryFromBuffer aggregates live OTel signals from the correlator's
// sliding window. ok is false when the window holds no OTel signals so the
// caller can fall back to incidents.
func (s *Server) traceSummaryFromBuffer(lookback time.Duration) (traceSummaryResponse, bool) {
	eff := lookback
	if win := s.buffer.Window(); win > 0 && win < eff {
		eff = win
	}
	cutoff := time.Now().Add(-eff)

	bySvc := map[string]*traceAgg{}
	traceSet := map[string]struct{}{}
	var allDur []int64

	for _, e := range s.buffer.Snapshot() {
		if e.AddedAt.Before(cutoff) {
			continue
		}
		var svc, tid string
		var dur int64
		kind := "" // "error" | "slow" | "" (other)
		switch ev := e.Event.(type) {
		case watcher.OTelSpanErrorEvent:
			svc, tid, dur, kind = ev.ServiceName, ev.TraceID, ev.DurationNanos, traceKindError
		case watcher.OTelSpanLatencySpikeEvent:
			svc, tid, dur, kind = ev.ServiceName, ev.TraceID, ev.DurationNanos, traceKindSlow
		case watcher.OTelLogMatchEvent:
			svc, tid = ev.ServiceName, ev.TraceID
		case watcher.OTelSpanEventEvent:
			svc, tid = ev.ServiceName, ev.TraceID
		default:
			continue
		}
		if svc == "" {
			svc = "unknown"
		}
		a := bySvc[svc]
		if a == nil {
			a = &traceAgg{}
			bySvc[svc] = a
		}
		switch kind {
		case traceKindError:
			a.errors++
		case traceKindSlow:
			a.slow++
		default:
			a.other++
		}
		if dur > 0 {
			a.durNs = append(a.durNs, dur)
			allDur = append(allDur, dur)
		}
		if kind != "" && tid != "" {
			traceSet[tid] = struct{}{}
		}
	}

	if len(bySvc) == 0 {
		return traceSummaryResponse{}, false
	}
	resp := buildTraceServices(bySvc, true)
	resp.Source = "live"
	resp.WindowSeconds = int(eff.Seconds())
	resp.ErrorTraces = len(traceSet)
	if len(allDur) > 0 {
		ms := int(avgNs(allDur) / 1_000_000)
		resp.AvgCriticalPathMs = &ms
	}
	return resp, true
}

// traceSummaryFromIncidents derives a counts-only summary from recent OTel
// IncidentReports. Span latency is unavailable here, so LatencyMs/AvgCriticalPathMs
// stay nil and the UI shows "—".
func (s *Server) traceSummaryFromIncidents(ctx context.Context, lookback time.Duration) (traceSummaryResponse, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := s.client.List(ctx, list); err != nil {
		return traceSummaryResponse{}, err
	}
	cutoff := time.Now().Add(-lookback)
	bySvc := map[string]*traceAgg{}
	traceSet := map[string]struct{}{}

	for i := range list.Items {
		it := &list.Items[i]
		if !incident.IsOTelIncidentType(it.Spec.IncidentType) {
			continue
		}
		when := otelIncidentTime(it)
		if when.IsZero() || when.Before(cutoff) {
			continue
		}
		svc := otelIncidentService(it)
		if svc == "" {
			svc = it.Namespace
		}
		if svc == "" {
			svc = "unknown"
		}
		a := bySvc[svc]
		if a == nil {
			a = &traceAgg{}
			bySvc[svc] = a
		}
		switch classifyOTelIncidentType(it.Spec.IncidentType) {
		case traceKindSlow:
			a.slow++
		case traceKindLog:
			a.other++
		default:
			a.errors++
		}
		for _, tid := range collectTraceIDs(it.Annotations) {
			if tid != "" {
				traceSet[tid] = struct{}{}
			}
		}
	}

	resp := buildTraceServices(bySvc, false)
	resp.Source = "incidents"
	resp.WindowSeconds = int(lookback.Seconds())
	resp.ErrorTraces = len(traceSet)
	return resp, nil
}

// buildTraceServices turns per-service aggregates into the wire shape: sorted
// by problem volume, capped, with span/error/slow percentages that sum to 100.
func buildTraceServices(bySvc map[string]*traceAgg, withLatency bool) traceSummaryResponse {
	out := make([]traceServiceSummary, 0, len(bySvc))
	servicesWithErrors := 0
	for name, a := range bySvc {
		total := a.errors + a.slow + a.other
		if total == 0 {
			continue
		}
		ep := pctOf(a.errors, total)
		sp := pctOf(a.slow, total)
		gp := max(100-ep-sp, 0)
		ts := traceServiceSummary{Name: name, Errors: a.errors + a.slow, ErrorPct: ep, SlowPct: sp, SpanPct: gp}
		if withLatency && len(a.durNs) > 0 {
			ms := int(avgNs(a.durNs) / 1_000_000)
			ts.LatencyMs = &ms
		}
		if a.errors+a.slow > 0 {
			servicesWithErrors++
		}
		out = append(out, ts)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > maxTraceSummaryServices {
		out = out[:maxTraceSummaryServices]
	}
	return traceSummaryResponse{Services: out, ServicesWithErrors: servicesWithErrors}
}

func otelIncidentTime(it *rcav1alpha1.IncidentReport) time.Time {
	if t := it.Status.LastObservedAt; t != nil {
		return t.Time
	}
	if t := it.Status.ActiveAt; t != nil {
		return t.Time
	}
	if t := it.Status.FirstObservedAt; t != nil {
		return t.Time
	}
	return time.Time{}
}

func otelIncidentService(it *rcav1alpha1.IncidentReport) string {
	sc := it.Spec.Scope
	if sc.WorkloadRef != nil && sc.WorkloadRef.Name != "" {
		return sc.WorkloadRef.Name
	}
	if sc.ResourceRef != nil && sc.ResourceRef.Name != "" {
		return sc.ResourceRef.Name
	}
	for _, r := range it.Status.AffectedResources {
		switch r.Kind {
		case kindService, kindDeployment, kindStatefulSet, kindDaemonSet, kindReplicaSet:
			if r.Name != "" {
				return r.Name
			}
		}
	}
	for _, r := range it.Status.AffectedResources {
		if r.Name != "" {
			return r.Name
		}
	}
	return ""
}

func classifyOTelIncidentType(t string) string {
	lt := strings.ToLower(t)
	switch {
	case strings.Contains(lt, "latency"), strings.Contains(lt, "slow"):
		return traceKindSlow
	case strings.Contains(lt, "log"):
		return traceKindLog
	default:
		return traceKindError
	}
}

func pctOf(n, total int) int {
	if total <= 0 {
		return 0
	}
	return (n*100 + total/2) / total
}

func avgNs(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	var sum int64
	for _, x := range xs {
		sum += x
	}
	return sum / int64(len(xs))
}
