// Package ingest implements the rca-exporter's external ingress: an OTLP
// Logs gRPC server that accepts log records from any compliant upstream
// (typically Fluent Bit -> OpenTelemetry Collector). Records are flattened
// into aggregator.LogRecord and dispatched to the configured aggregator,
// which decides when to fire a LogErrorSpike incident.
//
// Why a hand-rolled receiver instead of importing the OTel Collector's
// receiver/otlpreceiver:
//
//   - The collector receiver pulls in the entire collector pipeline
//     (config, telemetry, pdata, internal interfaces) — hundreds of MB of
//     transitive deps that are excessive for a single-purpose exporter.
//
//   - go.opentelemetry.io/proto/otlp is already an indirect dep of this
//     module (it ships with the OTLP trace exporter), so registering a gRPC
//     handler against the generated LogsServiceServer interface adds zero
//     new transitive cost.
//
//   - We only need ~30 lines of flattening logic; the collector's full
//     processor pipeline (batch, memory_limiter, k8sattributes) is the
//     upstream collector's job, not ours.
package ingest

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/gaurangkudale/rca-operator/internal/exporter/aggregator"
)

// Resource attribute keys we look for. These match the OpenTelemetry
// semantic conventions for Kubernetes
// (https://github.com/open-telemetry/semantic-conventions/blob/main/docs/resource/k8s.md).
// The OTel Collector's k8sattributes processor populates them automatically
// when the collector runs as a DaemonSet with hostNetwork or downwardAPI.
const (
	attrServiceName       = "service.name"
	attrK8sNamespace      = "k8s.namespace.name"
	attrK8sPodName        = "k8s.pod.name"
	attrK8sContainerName  = "k8s.container.name"
	attrK8sDeploymentName = "k8s.deployment.name"
)

// errorSeverityFloor is the minimum SeverityNumber considered "error" for
// detection purposes. The OTLP spec partitions severity into bands of 4 per
// level (TRACE/DEBUG/INFO/WARN/ERROR/FATAL); ERROR starts at 17. Anything at
// or above ERROR — including all four ERROR sub-levels and FATAL — counts.
const errorSeverityFloor = logspb.SeverityNumber_SEVERITY_NUMBER_ERROR

// LogsReceiver is a minimal OTLP Logs gRPC service. It implements
// collogspb.LogsServiceServer and forwards every error-classified record to
// the injected aggregator.
type LogsReceiver struct {
	collogspb.UnimplementedLogsServiceServer

	agg *aggregator.ErrorRateAggregator
	log logr.Logger
}

// NewLogsReceiver constructs a receiver that delegates to the supplied
// aggregator. agg must be non-nil; the constructor does not panic in order
// to keep cmd/rca-exporter wiring linear, but the receiver will silently
// drop every record if it is.
func NewLogsReceiver(agg *aggregator.ErrorRateAggregator, log logr.Logger) *LogsReceiver {
	return &LogsReceiver{
		agg: agg,
		log: log.WithName("otlp-logs-receiver"),
	}
}

// Export is the single OTLP Logs RPC. It walks the nested
// ResourceLogs -> ScopeLogs -> LogRecords structure, extracts Kubernetes
// resource attributes once per ResourceLogs entry, classifies each record by
// severity, and pushes errors to the aggregator. The response is always an
// empty success — the OTLP spec uses partial success only for explicit
// rejection, and we never reject (a misclassified record is preferable to
// dropping client data and confusing on-call engineers chasing missing logs).
func (r *LogsReceiver) Export(
	ctx context.Context,
	req *collogspb.ExportLogsServiceRequest,
) (*collogspb.ExportLogsServiceResponse, error) {
	if r.agg == nil || req == nil {
		return &collogspb.ExportLogsServiceResponse{}, nil
	}

	for _, rl := range req.GetResourceLogs() {
		// Extract resource-level attributes once per ResourceLogs bucket.
		// Per the OTLP spec, every LogRecord under a single ResourceLogs
		// shares the same Resource, so this is correct and significantly
		// cheaper than re-walking attributes per record.
		resAttrs := flattenAttrs(rl.GetResource().GetAttributes())
		namespace := resAttrs[attrK8sNamespace]
		pod := resAttrs[attrK8sPodName]
		container := resAttrs[attrK8sContainerName]
		service := resAttrs[attrServiceName]
		if service == "" {
			// Fall back to deployment name when service.name is missing.
			// Apps that aren't OTel-instrumented but are tagged by the
			// k8sattributes processor will have k8s.deployment.name but no
			// service.name. The fallback ensures we still get attribution.
			service = resAttrs[attrK8sDeploymentName]
		}
		if service == "" && pod != "" {
			// Final fallback: derive a stable service-ish identity from the
			// pod name by trimming the trailing replicaset/pod hash. For a
			// pod like "payment-service-7d4f8-abc" this yields
			// "payment-service". Imperfect but better than dropping the
			// record entirely.
			service = stripPodHash(pod)
		}

		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				if lr.GetSeverityNumber() < errorSeverityFloor {
					continue
				}
				r.agg.Observe(aggregator.LogRecord{
					Timestamp: timestampFromRecord(lr),
					Namespace: namespace,
					Service:   service,
					Pod:       pod,
					Container: container,
					Message:   bodyAsString(lr.GetBody()),
				})
			}
		}
	}

	return &collogspb.ExportLogsServiceResponse{}, nil
}

// Serve registers the receiver on a fresh gRPC server, binds it to addr, and
// blocks until the listener stops. The caller is expected to invoke
// grpcServer.GracefulStop() (or to cancel the surrounding context and rely
// on net.Listener closure) at shutdown. Returning the *grpc.Server lets the
// exporter binary wire its own shutdown sequence.
func Serve(ctx context.Context, addr string, recv *LogsReceiver, log logr.Logger) (*grpc.Server, net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	srv := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(srv, recv)

	log.Info("OTLP logs gRPC receiver listening", "address", addr)

	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Error(err, "OTLP logs gRPC server stopped")
		}
	}()

	go func() {
		<-ctx.Done()
		log.Info("Shutting down OTLP logs gRPC receiver")
		srv.GracefulStop()
	}()

	return srv, lis, nil
}

// flattenAttrs converts a slice of OTLP KeyValue pairs into a flat
// map[string]string. Only string-typed values are extracted because every
// resource attribute we care about (service.name, k8s.* names) is a string
// per the semantic conventions. Non-string values are silently ignored.
func flattenAttrs(attrs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		if kv == nil || kv.GetValue() == nil {
			continue
		}
		if s, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
			out[kv.GetKey()] = s.StringValue
		}
	}
	return out
}

// bodyAsString reads the LogRecord.Body AnyValue oneof and returns its
// string representation. The vast majority of OTLP log bodies emitted by
// language SDKs and Fluent Bit are StringValue; we only handle that case
// directly to avoid pulling in protojson formatting for the rare structured
// body. Non-string bodies become an empty message and the record is still
// counted toward the spike (the count is what matters for detection; the
// sample is just for triage).
func bodyAsString(body *commonpb.AnyValue) string {
	if body == nil {
		return ""
	}
	if s, ok := body.GetValue().(*commonpb.AnyValue_StringValue); ok {
		return s.StringValue
	}
	return ""
}

// timestampFromRecord prefers TimeUnixNano (the event time the producer
// stamped) but falls back to ObservedTimeUnixNano (when the collector
// observed the record). Both are uint64 nanoseconds since epoch per the
// OTLP spec; either may be zero if upstream did not set it, in which case
// we fall back to time.Now so the record is still placed inside the current
// window.
func timestampFromRecord(lr *logspb.LogRecord) time.Time {
	if t := lr.GetTimeUnixNano(); t != 0 {
		return time.Unix(0, int64(t))
	}
	if t := lr.GetObservedTimeUnixNano(); t != 0 {
		return time.Unix(0, int64(t))
	}
	return time.Now()
}

// stripPodHash trims the trailing "-<replicaset>-<pod>" suffix from a pod
// name to recover an approximate workload identity for service attribution.
// This is a best-effort fallback used only when both service.name and
// k8s.deployment.name are absent — i.e. when the upstream collector is
// misconfigured. Examples:
//
//	"payment-service-7d4f8-abc"   -> "payment-service"
//	"api-gateway-9k2m1-xyz"       -> "api-gateway"
//	"single"                       -> "single"
func stripPodHash(pod string) string {
	parts := strings.Split(pod, "-")
	if len(parts) <= 2 {
		return pod
	}
	// Heuristic: drop the last two segments (replicaset hash + pod hash)
	// when both look hash-ish (mixed alphanumeric, no obvious word). We err
	// on the side of trimming because the alternative (a per-pod service
	// identity) creates one incident per replica which is exactly what the
	// service-scoped DedupKey was designed to avoid.
	return strings.Join(parts[:len(parts)-2], "-")
}
