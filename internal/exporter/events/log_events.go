// Package events defines CorrelatorEvent implementations sourced by the
// rca-exporter (Phase 2). These types are intentionally kept in a separate
// package from internal/watcher so the watcher package remains pure to
// in-cluster Kubernetes signals (Pods, Nodes, Events, Deployments, ...).
//
// Exporter-sourced events implement the same watcher.CorrelatorEvent interface
// (Type / OccurredAt / DedupKey) so they can flow through the same correlator
// rule engine and be persisted by the same reporter.Reporter as Phase 1
// signals — no fork of the correlator pipeline is required.
package events

import (
	"time"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// EventTypeLogErrorSpike marks a sustained burst of ERROR-severity log
// records observed for a single (namespace, service) pair within the
// configured detection window. It is the first signal class produced by the
// rca-exporter and is delivered through OTLP logs ingestion (Fluent Bit ->
// OpenTelemetry Collector -> rca-exporter).
const EventTypeLogErrorSpike watcher.EventType = "LogErrorSpike"

// LogErrorSpikeEvent is emitted when the error-rate aggregator observes that
// the count of ERROR (or higher severity) log records for a single service
// has crossed its configured threshold inside the rolling detection window.
//
// The event is service-scoped, not pod-scoped: a spike on one of three
// payment-service replicas should still produce a single incident attributed
// to "payment-service". Pod is recorded for diagnostic context only and is
// the most recently seen pod that contributed an error.
type LogErrorSpikeEvent struct {
	// At is the wall-clock time at which the threshold was crossed.
	At time.Time

	// Namespace is the Kubernetes namespace of the affected service.
	// Sourced from the OTLP resource attribute `k8s.namespace.name` (set by
	// the OTel Collector's k8sattributes processor).
	Namespace string

	// Service is the logical service name. Sourced from `service.name`,
	// falling back to `k8s.deployment.name` and finally `k8s.pod.name`.
	Service string

	// Pod is the most recent pod that contributed an error to the window.
	// Used as the IncidentReport's primary affected resource so the existing
	// dashboard's pod-centric views still work.
	Pod string

	// Container is the offending container name when known.
	Container string

	// ErrorCount is the number of ERROR-or-higher records observed in the
	// window at the time the threshold was crossed.
	ErrorCount int

	// WindowSeconds is the size of the rolling detection window in seconds,
	// recorded so the incident summary self-describes its sensitivity.
	WindowSeconds int

	// Threshold is the configured fire-threshold the window crossed.
	Threshold int

	// SampleMessages holds the most recent N error messages from the window,
	// included verbatim in the incident summary so on-call engineers can
	// triage without leaving kubectl describe.
	SampleMessages []string
}

// Type returns the EventType discriminator. Implements watcher.CorrelatorEvent.
func (e LogErrorSpikeEvent) Type() watcher.EventType { return EventTypeLogErrorSpike }

// OccurredAt returns the spike timestamp. Implements watcher.CorrelatorEvent.
func (e LogErrorSpikeEvent) OccurredAt() time.Time { return e.At }

// DedupKey returns the canonical identity for incident deduplication.
// Service-scoped on purpose: a spike across replicas of the same service is
// the same operational incident, not three separate ones.
func (e LogErrorSpikeEvent) DedupKey() string {
	return string(EventTypeLogErrorSpike) + ":" + e.Namespace + ":" + e.Service
}

// Compile-time assertion that LogErrorSpikeEvent satisfies CorrelatorEvent.
var _ watcher.CorrelatorEvent = LogErrorSpikeEvent{}
