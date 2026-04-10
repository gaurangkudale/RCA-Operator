package ingest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/exporter/aggregator"
	"github.com/gaurangkudale/rca-operator/internal/exporter/bridge"
	"github.com/gaurangkudale/rca-operator/internal/exporter/events"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

// TestExportLogs_FullPipeline_CreatesIncidentReport exercises the entire
// Phase-2 vertical slice in-process: a real gRPC LogsServiceServer dispatches
// into a real ErrorRateAggregator, which fires a real bridge.Bridge that
// calls a real reporter.Reporter writing to a controller-runtime fake client.
//
// If this test passes, the wiring contract between the four packages is
// correct: any breakage in shape (proto field names, severity floor,
// EnsureIncident signature, IncidentReport CRD shape) surfaces here rather
// than at deploy time.
func TestExportLogs_FullPipeline_CreatesIncidentReport(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	rep := reporter.NewReporter(cl, logr.Discard())

	br := bridge.New(rep, "rca-exporter", logr.Discard(), context.Background)

	agg := aggregator.New(aggregator.Config{
		Window:    time.Minute,
		Threshold: 3,
	}, br.Handle)

	recv := NewLogsReceiver(agg, logr.Discard())

	// Spin the gRPC server up on an in-memory listener so the test does not
	// touch the network. bufconn is the controller-runtime / grpc-go
	// idiomatic way to host a server inside a unit test.
	const bufSize = 1 << 20
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(srv, recv)

	go func() {
		_ = srv.Serve(lis)
	}()
	defer srv.Stop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}
	defer conn.Close()

	client := collogspb.NewLogsServiceClient(conn)

	req := buildExportRequest(
		"dev", "payment-service", "payment-7d4f8-abc", "payment-service",
		3, // 3 ERROR records, exactly at threshold -> single spike
		logspb.SeverityNumber_SEVERITY_NUMBER_ERROR,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Export(ctx, req); err != nil {
		t.Fatalf("OTLP Export RPC failed: %v", err)
	}

	// Reporter.EnsureIncident is synchronous (it's called inline from the
	// SpikeHandler, which is called inline from Observe, which is called
	// inline from Export). By the time Export returns, the IncidentReport
	// must already exist on the fake client. Listing is sufficient.
	var list rcav1alpha1.IncidentReportList
	if err := cl.List(context.Background(), &list, ctrlclient.InNamespace("dev")); err != nil {
		t.Fatalf("failed to list IncidentReports: %v", err)
	}

	if len(list.Items) != 1 {
		t.Fatalf("expected 1 IncidentReport, got %d", len(list.Items))
	}

	got := list.Items[0]
	if got.Spec.AgentRef != "rca-exporter" {
		t.Errorf("expected agentRef=rca-exporter, got %q", got.Spec.AgentRef)
	}
	if got.Spec.IncidentType != bridge.IncidentTypeLogErrorSpike {
		t.Errorf("expected incidentType=%q, got %q", bridge.IncidentTypeLogErrorSpike, got.Spec.IncidentType)
	}
	if got.Status.Severity != bridge.SeverityLogErrorSpike {
		t.Errorf("expected severity=%q, got %q", bridge.SeverityLogErrorSpike, got.Status.Severity)
	}
	if got.Spec.Scope.Namespace != "dev" {
		t.Errorf("expected scope.namespace=dev, got %q", got.Spec.Scope.Namespace)
	}
}

// INFO-level records must NOT trip the aggregator regardless of volume.
// This is the negative complement to the happy-path test above and guards
// against a regression in errorSeverityFloor.
func TestExportLogs_NonErrorRecordsAreIgnored(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := rcav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add RCA scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rcav1alpha1.IncidentReport{}).
		Build()

	var capturedSpikes int
	recordingHandler := func(events.LogErrorSpikeEvent) { capturedSpikes++ }

	agg := aggregator.New(aggregator.Config{
		Window:    time.Minute,
		Threshold: 1, // Trip on first error if it ever happens.
	}, recordingHandler)

	recv := NewLogsReceiver(agg, logr.Discard())

	req := buildExportRequest(
		"dev", "api-gateway", "api-gateway-9k2m1-xyz", "api-gateway",
		50, // 50 INFO records: way above threshold but wrong severity
		logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
	)
	if _, err := recv.Export(context.Background(), req); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if capturedSpikes != 0 {
		t.Errorf("expected 0 spikes for INFO logs, got %d", capturedSpikes)
	}

	var list rcav1alpha1.IncidentReportList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected 0 IncidentReports, got %d", len(list.Items))
	}
}

// buildExportRequest constructs an OTLP ExportLogsServiceRequest with N
// LogRecords at the given severity, all sharing the same Resource (ns/svc/
// pod/container). One ResourceLogs / one ScopeLogs is sufficient — the OTLP
// spec allows arbitrary nesting but the receiver walks all three levels
// uniformly so a single bucket exercises every code path.
func buildExportRequest(namespace, service, pod, container string, count int, severity logspb.SeverityNumber) *collogspb.ExportLogsServiceRequest {
	records := make([]*logspb.LogRecord, 0, count)
	now := uint64(time.Now().UnixNano())
	for i := 0; i < count; i++ {
		records = append(records, &logspb.LogRecord{
			TimeUnixNano:   now + uint64(i),
			SeverityNumber: severity,
			Body: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_StringValue{StringValue: "synthetic test message"},
			},
		})
	}

	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					strAttr(attrK8sNamespace, namespace),
					strAttr(attrServiceName, service),
					strAttr(attrK8sPodName, pod),
					strAttr(attrK8sContainerName, container),
				},
			},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: records,
			}},
		}},
	}
}

func strAttr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: value},
		},
	}
}
