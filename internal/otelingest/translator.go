package otelingest

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// Status code constants
const (
	statusCodeOK    = "STATUS_CODE_OK"
	statusCodeError = "STATUS_CODE_ERROR"
	statusCodeUnset = "STATUS_CODE_UNSET"
)

// Span kind constants
const (
	spanKindServer = "SERVER"
)

// Severity level constants
const (
	severityWarn  = "WARN"
	severityError = "ERROR"
)

// Translator converts inbound OTLP payloads into watcher.CorrelatorEvent signals
// and hands them to the injected EventEmitter. It is stateless and safe for
// concurrent use from HTTP handlers.
type Translator struct {
	cfg     Config
	emitter watcher.EventEmitter
	nowFn   func() time.Time
}

// NewTranslator constructs a Translator. nowFn is overridable for tests.
func NewTranslator(cfg Config, emitter watcher.EventEmitter) *Translator {
	return &Translator{cfg: cfg, emitter: emitter, nowFn: time.Now}
}

// TranslateResourceSpans iterates OTLP ResourceSpans and emits correlator
// events for any spans matching the configured trace filters. Returns the
// count of emitted events (useful for tests and metrics).
func (t *Translator) TranslateResourceSpans(rss []*tracepb.ResourceSpans) int {
	emitted := 0
	for _, rs := range rss {
		resAttrs := resourceAttrs(rs.GetResource())
		base := t.baseFromResource(resAttrs)
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				emitted += t.translateSpan(span, resAttrs, base)
			}
		}
	}
	return emitted
}

// TranslateResourceLogs iterates OTLP ResourceLogs and emits OTelLogMatchEvents
// for records meeting the severity filter. PII redaction is applied here
// (earliest point in the pipeline).
func (t *Translator) TranslateResourceLogs(rls []*logspb.ResourceLogs) int {
	minSevNum := severityNumberForText(t.cfg.LogFilters.MinSeverity)
	emitted := 0
	for _, rl := range rls {
		resAttrs := resourceAttrs(rl.GetResource())
		base := t.baseFromResource(resAttrs)
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				sevNum := int32(rec.GetSeverityNumber())
				if minSevNum > 0 && sevNum < minSevNum {
					continue
				}
				rawBody := anyValueToString(rec.GetBody())
				redactedBody := redact(rawBody, t.cfg.Redaction)
				attrs := keyValuesToMap(rec.GetAttributes())
				traceID := hexBytes(rec.GetTraceId())
				spanID := hexBytes(rec.GetSpanId())
				base.At = unixNanoToTime(rec.GetTimeUnixNano(), t.nowFn())

				evt := watcher.OTelLogMatchEvent{
					BaseEvent:     base,
					TraceID:       traceID,
					SpanID:        spanID,
					ServiceName:   resAttrs["service.name"],
					Severity:      severityTextFromNumber(sevNum, rec.GetSeverityText()),
					SeverityNum:   sevNum,
					Body:          redactedBody,
					BodyHash:      bodyHash(redactedBody),
					Attrs:         attrs,
					ResourceAttrs: resAttrs,
				}
				t.emitter.Emit(evt)
				emitted++
			}
		}
	}
	return emitted
}

// translateSpan evaluates one span against the trace filters and emits 0..N
// events depending on which filters trigger. A single span can produce both
// an error event and a latency-spike event; span events become their own signal.
func (t *Translator) translateSpan(span *tracepb.Span, resAttrs map[string]string, base watcher.BaseEvent) int {
	attrs := keyValuesToMap(span.GetAttributes())
	startNs := span.GetStartTimeUnixNano()
	endNs := span.GetEndTimeUnixNano()
	durationNs := int64(0)
	if endNs > startNs {
		durationNs = int64(endNs - startNs)
	}
	statusCode := statusCodeText(span.GetStatus())
	statusMsg := redact(span.GetStatus().GetMessage(), t.cfg.Redaction)
	spanKind := spanKindText(span.GetKind())
	traceID := hexBytes(span.GetTraceId())
	spanID := hexBytes(span.GetSpanId())
	parentSpanID := hexBytes(span.GetParentSpanId())
	serviceName := resAttrs["service.name"]
	spanName := span.GetName()
	startTime := unixNanoToTime(startNs, t.nowFn())
	endTime := unixNanoToTime(endNs, t.nowFn())
	base.At = endTime

	emitted := 0

	// Error filter: explicit ERROR status or 5xx HTTP response.
	if t.shouldEmitErrorSpan(statusCode, attrs) {
		evt := watcher.OTelSpanErrorEvent{
			BaseEvent:     base,
			TraceID:       traceID,
			SpanID:        spanID,
			ParentSpanID:  parentSpanID,
			ServiceName:   serviceName,
			SpanName:      spanName,
			SpanKind:      spanKind,
			StatusCode:    statusCode,
			StatusMessage: statusMsg,
			DurationNanos: durationNs,
			StartTime:     startTime,
			EndTime:       endTime,
			Attrs:         attrs,
			ResourceAttrs: resAttrs,
		}
		t.emitter.Emit(evt)
		emitted++
	}

	// Latency spike filter: independent of error status.
	if t.cfg.TraceFilters.LatencyP99Ms > 0 {
		threshold := int64(t.cfg.TraceFilters.LatencyP99Ms) * int64(time.Millisecond)
		if durationNs >= threshold {
			evt := watcher.OTelSpanLatencySpikeEvent{
				BaseEvent:     base,
				TraceID:       traceID,
				SpanID:        spanID,
				ParentSpanID:  parentSpanID,
				ServiceName:   serviceName,
				SpanName:      spanName,
				DurationNanos: durationNs,
				ThresholdNs:   threshold,
				StartTime:     startTime,
				EndTime:       endTime,
				Attrs:         attrs,
				ResourceAttrs: resAttrs,
			}
			t.emitter.Emit(evt)
			emitted++
		}
	}

	// Span events (exception, log, etc.) become their own signals.
	for _, se := range span.GetEvents() {
		seAttrs := keyValuesToMap(se.GetAttributes())
		// Redact any body-like attributes on the event itself.
		if msg, ok := seAttrs["exception.message"]; ok {
			seAttrs["exception.message"] = redact(msg, t.cfg.Redaction)
		}
		if st, ok := seAttrs["exception.stacktrace"]; ok {
			seAttrs["exception.stacktrace"] = redact(st, t.cfg.Redaction)
		}
		eventTime := unixNanoToTime(se.GetTimeUnixNano(), endTime)
		eventBase := base
		eventBase.At = eventTime
		evt := watcher.OTelSpanEventEvent{
			BaseEvent:     eventBase,
			TraceID:       traceID,
			SpanID:        spanID,
			ServiceName:   serviceName,
			EventName:     se.GetName(),
			EventTime:     eventTime,
			Attrs:         seAttrs,
			ResourceAttrs: resAttrs,
		}
		t.emitter.Emit(evt)
		emitted++
	}

	return emitted
}

// shouldEmitErrorSpan applies the TraceFilters gates.
func (t *Translator) shouldEmitErrorSpan(statusCode string, attrs map[string]string) bool {
	if t.cfg.TraceFilters.StatusCodeERROR && statusCode == statusCodeError {
		return true
	}
	if t.cfg.TraceFilters.HTTPStatusGte > 0 {
		for _, k := range []string{"http.status_code", "http.response.status_code"} {
			if v, ok := attrs[k]; ok {
				if n, err := strconv.Atoi(v); err == nil && n >= t.cfg.TraceFilters.HTTPStatusGte {
					return true
				}
			}
		}
	}
	return false
}

// baseFromResource populates BaseEvent fields from k8sattributes-enriched
// resource attributes. Missing keys yield empty strings (downstream scope
// resolution gracefully handles nil values).
func (t *Translator) baseFromResource(ra map[string]string) watcher.BaseEvent {
	return watcher.BaseEvent{
		AgentName: t.cfg.AgentName,
		Namespace: ra["k8s.namespace.name"],
		PodName:   ra["k8s.pod.name"],
		PodUID:    ra["k8s.pod.uid"],
		NodeName:  ra["k8s.node.name"],
	}
}

// ---- conversion helpers ----------------------------------------------------

func resourceAttrs(r *resourcepb.Resource) map[string]string {
	if r == nil {
		return map[string]string{}
	}
	return keyValuesToMap(r.GetAttributes())
}

func keyValuesToMap(kvs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv == nil || kv.GetKey() == "" {
			continue
		}
		out[kv.GetKey()] = anyValueToString(kv.GetValue())
	}
	return out
}

func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(val.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(val.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(val.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		var parts []string
		for _, item := range val.ArrayValue.GetValues() {
			parts = append(parts, anyValueToString(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *commonpb.AnyValue_KvlistValue:
		m := keyValuesToMap(val.KvlistValue.GetValues())
		var parts []string
		for k, v := range m {
			parts = append(parts, k+"="+v)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return ""
}

func statusCodeText(s *tracepb.Status) string {
	if s == nil {
		return statusCodeUnset
	}
	switch s.GetCode() {
	case tracepb.Status_STATUS_CODE_OK:
		return statusCodeOK
	case tracepb.Status_STATUS_CODE_ERROR:
		return statusCodeError
	default:
		return statusCodeUnset
	}
}

func spanKindText(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "CLIENT"
	case tracepb.Span_SPAN_KIND_SERVER:
		return spanKindServer
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return "INTERNAL"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "PRODUCER"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "CONSUMER"
	default:
		return "UNSPECIFIED"
	}
}

// severityNumberForText maps the Helm min-severity string to the OTel severity
// number (INFO=9, WARN=13, ERROR=17, FATAL=21 — upper bound of each tier).
func severityNumberForText(text string) int32 {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case "TRACE":
		return 1
	case "DEBUG":
		return 5
	case "INFO":
		return 9
	case severityWarn, "WARNING":
		return 13
	case severityError:
		return 17
	case "FATAL":
		return 21
	}
	return 0 // no filter
}

// severityTextFromNumber maps OTel severity numbers to canonical text buckets,
// preferring the record's own text when present and non-empty.
func severityTextFromNumber(num int32, given string) string {
	if given != "" {
		return given
	}
	switch {
	case num >= 21:
		return "FATAL"
	case num >= 17:
		return severityError
	case num >= 13:
		return severityWarn
	case num >= 9:
		return "INFO"
	case num >= 5:
		return "DEBUG"
	case num >= 1:
		return "TRACE"
	}
	return ""
}

func hexBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

func unixNanoToTime(ns uint64, fallback time.Time) time.Time {
	if ns == 0 {
		return fallback
	}
	return time.Unix(0, int64(ns))
}
