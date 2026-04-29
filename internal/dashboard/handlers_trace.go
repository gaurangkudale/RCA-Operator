package dashboard

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

// ── /api/traces/{id} ──────────────────────────────────────────────────────────
//
// Returns a UI-friendly summary of a Jaeger trace: header stats, ordered
// span tree, error spans, per-service breakdown, and per-span k8s + tag
// context. The handler only depends on jaeger.Client.GetTrace, so the
// dashboard works without a Jaeger collector — it returns 503 when the
// client is nil.

// maxSpansReturned bounds the per-trace payload so a runaway trace doesn't
// produce a multi-megabyte JSON response or freeze the UI waterfall render.
// Error spans are kept preferentially when truncating.
const maxSpansReturned = 500

// boolTrue is the canonical string form of a true tag value as serialized by
// Jaeger's JSON model. Centralised so goconst stays happy.
const boolTrue = "true"

// statusError is the literal value assigned to traceResponse.Status when any
// span in the trace is flagged as an error. Centralised so goconst stays happy.
const statusError = "error"

var errTraceNotFound = errors.New("trace not found")

type traceResponse struct {
	TraceID          string             `json:"traceId"`
	StartTime        time.Time          `json:"startTime"`
	DurationMicros   int64              `json:"durationMicros"`
	SpanCount        int                `json:"spanCount"`
	ServiceCount     int                `json:"serviceCount"`
	Status           string             `json:"status"` // "ok" | "error"
	RootService      string             `json:"rootService,omitempty"`
	RootOperation    string             `json:"rootOperation,omitempty"`
	CriticalPathMs   int64              `json:"criticalPathMs,omitempty"`
	Spans            []traceSpan        `json:"spans"`
	ErrorSpans       []string           `json:"errorSpans,omitempty"` // span IDs flagged as errors
	ServiceBreakdown []traceServiceStat `json:"serviceBreakdown"`
	Truncated        bool               `json:"truncated,omitempty"` // true when SpanCount > len(Spans)
}

type traceSpan struct {
	SpanID        string            `json:"spanId"`
	ParentSpanID  string            `json:"parentSpanId,omitempty"`
	Service       string            `json:"service"`
	Operation     string            `json:"operation"`
	StartOffsetUs int64             `json:"startOffsetUs"` // microseconds since trace start
	DurationUs    int64             `json:"durationUs"`
	Depth         int               `json:"depth"`
	Kind          string            `json:"kind,omitempty"` // CLIENT|SERVER|INTERNAL|...
	IsError       bool              `json:"isError,omitempty"`
	StatusCode    string            `json:"statusCode,omitempty"` // http or grpc status
	HTTPMethod    string            `json:"httpMethod,omitempty"`
	HTTPURL       string            `json:"httpUrl,omitempty"`
	DBStatement   string            `json:"dbStatement,omitempty"`
	DBSystem      string            `json:"dbSystem,omitempty"`
	PeerService   string            `json:"peerService,omitempty"`
	PodName       string            `json:"podName,omitempty"`
	PodNamespace  string            `json:"podNamespace,omitempty"`
	NodeName      string            `json:"nodeName,omitempty"`
	ContainerName string            `json:"containerName,omitempty"`
	ExceptionType string            `json:"exceptionType,omitempty"`
	ExceptionMsg  string            `json:"exceptionMessage,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}

type traceServiceStat struct {
	Service        string  `json:"service"`
	SpanCount      int     `json:"spanCount"`
	TotalDurUs     int64   `json:"totalDurationUs"`
	PercentOfTrace float64 `json:"percentOfTrace"`
}

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.jc == nil {
		http.Error(w, "trace lookup not configured", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
	id = strings.Trim(id, "/")
	if id == "" || !looksLikeTraceID(id) {
		http.Error(w, "invalid trace id", http.StatusBadRequest)
		return
	}

	// Traces are immutable historical data, so we cache aggressively to avoid
	// re-querying Jaeger storage on every modal open. The Jaeger Query API can
	// be slow on local backends (e.g. Elasticsearch), and the dashboard's
	// default 3s TTL is far too short for click-driven trace lookups.
	const traceCacheTTL = 5 * time.Minute
	key := "trace|" + id
	body, etag, err := s.cache.FetchWithTTL(key, traceCacheTTL, func() (any, error) {
		t, err := s.jc.GetTraceForDashboard(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if t == nil || len(t.Spans) == 0 {
			return nil, errTraceNotFound
		}
		return buildTraceResponse(id, t), nil
	})
	if err != nil {
		if errors.Is(err, errTraceNotFound) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, buildTraceResponse(id, nil))
			return
		}
		s.log.Error(err, "fetch trace", "id", id)
		http.Error(w, "failed to fetch trace", http.StatusBadGateway)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

func looksLikeTraceID(s string) bool {
	if len(s) < 8 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// buildTraceResponse converts a raw Jaeger trace into the UI-shaped payload.
// Returns an empty response (with the requested ID) when the trace is missing
// — the UI renders a "not found" state rather than erroring.
func buildTraceResponse(id string, t *jaeger.Trace) traceResponse {
	resp := traceResponse{TraceID: id, Status: "ok", Spans: []traceSpan{}, ServiceBreakdown: []traceServiceStat{}}
	if t == nil || len(t.Spans) == 0 {
		return resp
	}

	// Earliest start defines the trace origin. Latest (start+duration) defines
	// trace end; we use that for total duration so concurrent spans don't get
	// summed.
	var minStart, maxEnd int64
	minStart = t.Spans[0].StartTime
	for i := range t.Spans {
		sp := &t.Spans[i]
		if sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		end := sp.StartTime + sp.Duration
		if end > maxEnd {
			maxEnd = end
		}
	}
	totalDur := maxEnd - minStart
	resp.StartTime = time.UnixMicro(minStart).UTC()
	resp.DurationMicros = totalDur
	resp.SpanCount = len(t.Spans)

	// Build parent index for tree depth.
	idx := make(map[string]*jaeger.Span, len(t.Spans))
	for i := range t.Spans {
		idx[t.Spans[i].SpanID] = &t.Spans[i]
	}

	// Per-service totals (overlap-merged so concurrent spans don't double-count).
	svcIntervals := map[string][][2]int64{}
	svcSpanCount := map[string]int{}

	out := make([]traceSpan, 0, len(t.Spans))
	errorSpans := []string{}
	rootSpanID := ""
	rootStart := int64(0)

	for i := range t.Spans {
		sp := &t.Spans[i]
		svc := serviceFor(sp, t.Processes)
		ts := traceSpan{
			SpanID:        sp.SpanID,
			ParentSpanID:  parentOf(sp),
			Service:       svc,
			Operation:     sp.OperationName,
			StartOffsetUs: sp.StartTime - minStart,
			DurationUs:    sp.Duration,
			Depth:         spanDepth(sp, idx, 0),
		}
		extractTags(sp, &ts)
		extractProcessTags(t.Processes[sp.ProcessID], &ts)
		extractErrorEvents(sp, &ts)
		if ts.IsError {
			errorSpans = append(errorSpans, sp.SpanID)
		}
		out = append(out, ts)

		if rootSpanID == "" || (parentOf(sp) == "" && sp.StartTime <= rootStart) {
			if parentOf(sp) == "" {
				rootSpanID = sp.SpanID
				rootStart = sp.StartTime
				resp.RootService = svc
				resp.RootOperation = sp.OperationName
			}
		}

		svcSpanCount[svc]++
		svcIntervals[svc] = append(svcIntervals[svc], [2]int64{sp.StartTime, sp.StartTime + sp.Duration})
	}

	// Order spans: by start time, then by depth so siblings stay grouped.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartOffsetUs != out[j].StartOffsetUs {
			return out[i].StartOffsetUs < out[j].StartOffsetUs
		}
		return out[i].Depth < out[j].Depth
	})
	if len(errorSpans) > 0 {
		resp.Status = statusError
	}
	// Cap span list. Keep all error spans + the longest non-error spans up to
	// the cap so the waterfall stays meaningful for huge traces.
	if len(out) > maxSpansReturned {
		errSet := make(map[string]struct{}, len(errorSpans))
		for _, id := range errorSpans {
			errSet[id] = struct{}{}
		}
		errs := make([]traceSpan, 0, len(errorSpans))
		rest := make([]traceSpan, 0, len(out))
		for _, sp := range out {
			if _, ok := errSet[sp.SpanID]; ok {
				errs = append(errs, sp)
			} else {
				rest = append(rest, sp)
			}
		}
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].DurationUs > rest[j].DurationUs })
		room := max(maxSpansReturned-len(errs), 0)
		if room < len(rest) {
			rest = rest[:room]
		}
		out = append(errs, rest...)
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].StartOffsetUs != out[j].StartOffsetUs {
				return out[i].StartOffsetUs < out[j].StartOffsetUs
			}
			return out[i].Depth < out[j].Depth
		})
		resp.Truncated = true
	}
	resp.Spans = out
	resp.ErrorSpans = errorSpans

	// Service breakdown (merge overlapping intervals per service).
	svcs := make([]string, 0, len(svcIntervals))
	for s := range svcIntervals {
		svcs = append(svcs, s)
	}
	sort.Strings(svcs)
	for _, svc := range svcs {
		merged := mergeIntervals(svcIntervals[svc])
		var total int64
		for _, m := range merged {
			total += m[1] - m[0]
		}
		pct := 0.0
		if totalDur > 0 {
			pct = float64(total) / float64(totalDur) * 100
		}
		resp.ServiceBreakdown = append(resp.ServiceBreakdown, traceServiceStat{
			Service:        svc,
			SpanCount:      svcSpanCount[svc],
			TotalDurUs:     total,
			PercentOfTrace: pct,
		})
	}
	sort.SliceStable(resp.ServiceBreakdown, func(i, j int) bool {
		return resp.ServiceBreakdown[i].TotalDurUs > resp.ServiceBreakdown[j].TotalDurUs
	})
	resp.ServiceCount = len(svcs)

	// Critical path = longest root-to-leaf duration chain.
	if rootSpanID != "" {
		resp.CriticalPathMs = criticalPathMicros(rootSpanID, idx) / 1000
	}
	return resp
}

func parentOf(sp *jaeger.Span) string {
	for _, ref := range sp.References {
		if ref.RefType == "CHILD_OF" || ref.RefType == "FOLLOWS_FROM" {
			return ref.SpanID
		}
	}
	return ""
}

func spanDepth(sp *jaeger.Span, idx map[string]*jaeger.Span, guard int) int {
	if guard > 64 {
		return guard
	}
	p := parentOf(sp)
	if p == "" {
		return 0
	}
	parent, ok := idx[p]
	if !ok {
		return 0
	}
	return 1 + spanDepth(parent, idx, guard+1)
}

func serviceFor(sp *jaeger.Span, processes map[string]jaeger.Process) string {
	if p, ok := processes[sp.ProcessID]; ok {
		return p.ServiceName
	}
	return ""
}

// extractTags pulls a curated subset of OTel/Jaeger tags onto the span.
// We intentionally surface a small set of well-known keys so the UI panel
// stays compact; everything else lands in the freeform Tags map.
func extractTags(sp *jaeger.Span, ts *traceSpan) {
	tags := map[string]string{}
	for _, kv := range sp.Tags {
		v := stringValue(kv.Value)
		switch kv.Key {
		case "span.kind":
			ts.Kind = strings.ToUpper(v)
		case "error":
			if v == boolTrue {
				ts.IsError = true
			}
		case "otel.status_code":
			if strings.EqualFold(v, "ERROR") {
				ts.IsError = true
			}
			if v != "" {
				ts.StatusCode = v
			}
		case "http.method", "http.request.method":
			ts.HTTPMethod = v
		case "http.url", "url.full", "http.target":
			if ts.HTTPURL == "" {
				ts.HTTPURL = v
			}
		case "http.status_code", "http.response.status_code":
			ts.StatusCode = v
			if isHTTPError(v) {
				ts.IsError = true
			}
		case "rpc.grpc.status_code":
			ts.StatusCode = "grpc:" + v
			if v != "0" && v != "" {
				ts.IsError = true
			}
		case "db.statement":
			ts.DBStatement = truncate(v, 200)
		case "db.system":
			ts.DBSystem = v
		case "peer.service":
			ts.PeerService = v
		case "k8s.pod.name":
			ts.PodName = v
		case "k8s.namespace.name":
			ts.PodNamespace = v
		case "k8s.node.name":
			ts.NodeName = v
		case "k8s.container.name":
			ts.ContainerName = v
		default:
			if v != "" && len(tags) < 16 {
				tags[kv.Key] = truncate(v, 160)
			}
		}
	}
	if len(tags) > 0 {
		ts.Tags = tags
	}
}

func extractProcessTags(p jaeger.Process, ts *traceSpan) {
	for _, kv := range p.Tags {
		v := stringValue(kv.Value)
		switch kv.Key {
		case "k8s.pod.name":
			if ts.PodName == "" {
				ts.PodName = v
			}
		case "k8s.namespace.name":
			if ts.PodNamespace == "" {
				ts.PodNamespace = v
			}
		case "k8s.node.name":
			if ts.NodeName == "" {
				ts.NodeName = v
			}
		case "k8s.container.name":
			if ts.ContainerName == "" {
				ts.ContainerName = v
			}
		}
	}
}

// extractErrorEvents scans span logs for `exception.type` / `exception.message`
// fields. Jaeger encodes span logs as raw JSON arrays of {timestamp, fields[]}
// objects; we decode lazily and skip anything that doesn't match.
func extractErrorEvents(sp *jaeger.Span, ts *traceSpan) {
	for _, raw := range sp.Logs {
		s := string(raw)
		if !strings.Contains(s, "exception.") {
			continue
		}
		// Lightweight scan — full JSON parsing isn't needed since we just want
		// the value adjacent to a known key. This avoids defining a Log struct.
		if v := jsonStringValue(s, "exception.type"); v != "" && ts.ExceptionType == "" {
			ts.ExceptionType = v
			ts.IsError = true
		}
		if v := jsonStringValue(s, "exception.message"); v != "" && ts.ExceptionMsg == "" {
			ts.ExceptionMsg = truncate(v, 240)
			ts.IsError = true
		}
		if ts.ExceptionType != "" && ts.ExceptionMsg != "" {
			return
		}
	}
}

// jsonStringValue does a forgiving lookup for `"<key>"` followed by a string
// value somewhere in s. Good enough for Jaeger's log field encoding without
// pulling in a full parser.
func jsonStringValue(s, key string) string {
	needle := `"` + key + `"`
	_, rest, ok := strings.Cut(s, needle)
	if !ok {
		return ""
	}
	// Look for "value":"<...>"
	before, after, ok := strings.Cut(rest, `"value"`)
	if !ok || len(before) > 80 {
		return ""
	}
	rest = after
	_, after, ok = strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	val, _, ok := strings.Cut(after, `"`)
	if !ok {
		return ""
	}
	return val
}

func isHTTPError(code string) bool {
	if len(code) < 1 {
		return false
	}
	return code[0] == '4' || code[0] == '5'
}

func stringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return boolTrue
		}
		return "false"
	case float64:
		// Jaeger encodes ints as JSON numbers; keep them integer-shaped when
		// they are.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func mergeIntervals(in [][2]int64) [][2]int64 {
	if len(in) <= 1 {
		return in
	}
	cp := make([][2]int64, len(in))
	copy(cp, in)
	sort.Slice(cp, func(i, j int) bool { return cp[i][0] < cp[j][0] })
	out := [][2]int64{cp[0]}
	for _, iv := range cp[1:] {
		last := &out[len(out)-1]
		if iv[0] <= last[1] {
			if iv[1] > last[1] {
				last[1] = iv[1]
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// criticalPathMicros walks children from root and returns the longest
// chain (parent_dur + max(child)). Cycle-guarded by depth limit.
func criticalPathMicros(rootID string, idx map[string]*jaeger.Span) int64 {
	children := map[string][]string{}
	for id, sp := range idx {
		p := parentOf(sp)
		if p != "" {
			children[p] = append(children[p], id)
		}
		_ = id
	}
	var walk func(id string, depth int) int64
	walk = func(id string, depth int) int64 {
		if depth > 128 {
			return 0
		}
		sp, ok := idx[id]
		if !ok {
			return 0
		}
		var maxChild int64
		for _, ch := range children[id] {
			d := walk(ch, depth+1)
			if d > maxChild {
				maxChild = d
			}
		}
		return sp.Duration + maxChild
	}
	return walk(rootID, 0)
}
