// Package otel provides OpenTelemetry setup and span helpers for the RCA Operator.
// When no OTLP endpoint is configured the package installs no-op providers so
// instrumentation call-sites are always safe to invoke.
package otel

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds the settings needed to initialise the OTel SDK.
type Config struct {
	// Endpoint is the OTLP gRPC collector address (e.g. "signoz-collector:4317").
	// Empty means OTel is disabled — no-op providers are used.
	Endpoint string

	// ServiceName is the service.name resource attribute. Defaults to "rca-operator".
	ServiceName string

	// SamplingRate is the trace sampling ratio (0.0–1.0). Defaults to 1.0.
	SamplingRate float64

	// Insecure disables TLS on the gRPC connection (typical for in-cluster collectors).
	Insecure bool
}

// Setup initialises the global OTel TracerProvider and MeterProvider with OTLP
// gRPC exporters pointing at the configured endpoint (typically SigNoz or an
// OTel Collector). It returns a shutdown function that must be deferred by the
// caller and that flushes pending spans and metric data points.
//
// When cfg.Endpoint is empty the function is a no-op — the default no-op
// providers stay in place.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	if cfg.Endpoint == "" {
		return noop, nil
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "rca-operator"
	}
	if cfg.SamplingRate <= 0 || cfg.SamplingRate > 1.0 {
		cfg.SamplingRate = 1.0
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return noop, err
	}

	dialOpts := []grpc.DialOption{}
	if cfg.Insecure {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	// ── Trace exporter ─────────────────────────────────────────────────────────
	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		otlptracegrpc.WithDialOption(dialOpts...),
	)
	if err != nil {
		return noop, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SamplingRate))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// ── Metrics exporter ────────────────────────────────────────────────────────
	// Operator-level metrics (reconcile counts, signal totals, etc.) are exported
	// via OTLP alongside traces so they appear in the same backend (SigNoz, etc.).
	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		otlpmetricgrpc.WithDialOption(dialOpts...),
	)
	if err != nil {
		// Non-fatal: if metrics export fails to initialise, traces still work.
		_ = tp.Shutdown(ctx)
		return noop, err
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			metric.WithInterval(30*time.Second),
		)),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Return a combined shutdown that flushes both providers.
	return func(sCtx context.Context) error {
		if tpErr := tp.Shutdown(sCtx); tpErr != nil {
			return tpErr
		}
		return mp.Shutdown(sCtx)
	}, nil
}
