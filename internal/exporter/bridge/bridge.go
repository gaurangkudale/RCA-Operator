// Package bridge wires the rca-exporter's stream detectors (aggregator) to
// the existing internal/reporter package so detected spikes become real
// IncidentReport CRs in the cluster.
//
// Keeping this glue in its own package lets the aggregator stay free of any
// Kubernetes coupling (it has no client.Client dependency, so it's trivially
// unit-testable) while still letting the production binary reuse 100% of the
// Phase-1 reporter pipeline — including its dedup, reopen, and cooldown
// semantics — without copying a single line of incident-lifecycle logic.
package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"

	"github.com/gaurangkudale/rca-operator/internal/exporter/aggregator"
	"github.com/gaurangkudale/rca-operator/internal/exporter/events"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

// IncidentTypeLogErrorSpike is the value written to IncidentReport
// status.incidentType for spikes produced by this exporter. It must remain
// stable across releases because operators write notification routing rules
// against it.
const IncidentTypeLogErrorSpike = "LogErrorSpike"

// SeverityLogErrorSpike is the default severity for a single-source error
// spike. P3 is intentional: an error burst with no corroborating signal
// (deployment change, trace failure, K8s event) is suspicious but not
// page-worthy on its own. A future cross-source correlation rule can elevate
// it once trace and change-tracking ingestion lands.
const SeverityLogErrorSpike = "P3"

// Bridge converts LogErrorSpikeEvents into IncidentReport CRs by delegating
// to a reporter.Reporter. The exporter constructs one Bridge at startup and
// passes Bridge.Handle as the aggregator's SpikeHandler callback.
type Bridge struct {
	rep      *reporter.Reporter
	agentRef string
	log      logr.Logger
	// ctxFn returns the context to use for each EnsureIncident call. The
	// exporter binary supplies its root signal-handling context here so that
	// in-flight CR writes are cancelled cleanly on SIGTERM.
	ctxFn func() context.Context
}

// New constructs a Bridge. agentRef is written to the IncidentReport's
// spec.agentRef and label so dashboards and notification routes can filter
// for exporter-sourced incidents (Phase 1 watchers use the in-cluster
// RCAAgent name; Phase 2 uses a fixed string by convention).
func New(rep *reporter.Reporter, agentRef string, log logr.Logger, ctxFn func() context.Context) *Bridge {
	if agentRef == "" {
		agentRef = "rca-exporter"
	}
	if ctxFn == nil {
		ctxFn = context.Background
	}
	return &Bridge{
		rep:      rep,
		agentRef: agentRef,
		log:      log.WithName("exporter-bridge"),
		ctxFn:    ctxFn,
	}
}

// Handle satisfies aggregator.SpikeHandler. It must not block on long
// operations beyond a single Kubernetes API roundtrip — the aggregator calls
// it inline from Observe so any latency here back-pressures the OTLP
// receiver's gRPC handlers.
func (b *Bridge) Handle(evt events.LogErrorSpikeEvent) {
	ctx := b.ctxFn()
	summary := buildSummary(evt)
	pod := evt.Pod
	if pod == "" {
		// Reporter.EnsureIncident expects a non-empty pod name for its
		// affected-resources slice. Service-scoped spikes that haven't yet
		// observed a concrete pod use the service name as a sentinel so the
		// resulting CR is still creatable and the dashboard renders
		// something meaningful.
		pod = evt.Service
	}

	if err := b.rep.EnsureIncident(
		ctx,
		evt.Namespace,
		pod,
		b.agentRef,
		IncidentTypeLogErrorSpike,
		SeverityLogErrorSpike,
		summary,
		evt.DedupKey(),
		evt.OccurredAt(),
	); err != nil {
		b.log.Error(err, "Failed to ensure LogErrorSpike incident",
			"namespace", evt.Namespace,
			"service", evt.Service,
			"errorCount", evt.ErrorCount,
		)
		return
	}

	b.log.Info("LogErrorSpike incident ensured",
		"namespace", evt.Namespace,
		"service", evt.Service,
		"errorCount", evt.ErrorCount,
		"window", evt.WindowSeconds,
	)
}

// Compile-time assertion that Bridge.Handle satisfies the SpikeHandler
// signature. If aggregator.SpikeHandler ever changes shape, this line will
// break the build at the bridge layer (closest to the wiring) instead of
// surfacing as a confusing error in cmd/rca-exporter/main.go.
var _ aggregator.SpikeHandler = (*Bridge)(nil).Handle

// buildSummary renders a single-line, dashboard-friendly description of the
// spike. The format is intentionally stable so that downstream notification
// templates (Slack, PagerDuty) can rely on it.
func buildSummary(evt events.LogErrorSpikeEvent) string {
	base := fmt.Sprintf(
		"%d errors in %ds for %s/%s (threshold %d)",
		evt.ErrorCount,
		evt.WindowSeconds,
		evt.Namespace,
		evt.Service,
		evt.Threshold,
	)
	if len(evt.SampleMessages) == 0 {
		return base
	}
	// Truncate each sample to keep the summary readable in kubectl describe
	// output (which line-wraps badly past ~120 chars). Operators wanting the
	// full message body can still grep the original logs in their backend of
	// choice; the IncidentReport is a triage entrypoint, not a log archive.
	samples := make([]string, 0, len(evt.SampleMessages))
	for _, m := range evt.SampleMessages {
		samples = append(samples, truncate(m, 80))
	}
	return base + ": " + strings.Join(samples, " | ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
