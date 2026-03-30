# ADR-0001: Adopt a Signal-first Incident Pipeline with OTLP Observability

## Status
Accepted

## Context

Phase 1 established three important foundations for RCA Operator:

- Kubernetes API resources are the authoritative operational inputs
- `IncidentReport` is the durable incident record
- the operator lifecycle already models `Detecting -> Active -> Resolved`

The next architecture step needs to stay fully aligned with Kubernetes and the wider CNCF ecosystem while improving the path from noisy raw cluster changes to actionable incidents.

The earlier architecture descriptions focused on a watcher-to-correlator flow and Prometheus-oriented operator metrics. That was enough for Phase 1, but it does not clearly model:

- normalization and enrichment before correlation
- a separate policy gate between analysis and remediation
- vendor-neutral telemetry export for traces, metrics, and logs

We also want SigNoz to be a first-class deployment target without making the operator API or runtime dependent on a single backend product.

## Decision

RCA Operator will evolve toward this high-level flow:

`kube-apiserver -> watchers -> signal processing -> incident intelligence -> IncidentReport lifecycle -> evidence collection -> RCA analysis -> decision policy -> report or remediate -> OpenTelemetry export`

The design principles are:

1. `kube-apiserver` remains the operational source of truth.
2. `IncidentReport` remains the durable incident source of truth for dashboards, notifications, and recovery after operator restarts.
3. The operator persists candidate incidents early in `Detecting` instead of waiting until an incident is already considered meaningful.
4. Signal processing is a distinct deterministic layer responsible for normalization, enrichment, and deduplication before correlation.
5. RCA analysis and remediation policy are separate concerns. Analysis explains what is happening; the decision layer decides whether action is allowed.
6. Observability is exported through backend-agnostic OTLP. SigNoz is a recommended deployment target, not a hardcoded API dependency.

## Consequences

### Positive

- Preserves the Kubernetes-native control model already present in Phase 1
- Makes the incident pipeline easier to reason about and test in isolation
- Supports autonomy levels without coupling them to the analysis engine
- Keeps the CRD portable across SigNoz and other OTLP-compatible backends
- Improves restart safety because the operator can reconstruct state from `IncidentReport` resources in `Detecting` or `Active`

### Trade-offs

- The API surface grows before all runtime features are implemented
- Some new `RCAAgent` fields will initially be stored but not enforced
- The repo will temporarily contain both Prometheus-era metrics endpoints and OTLP-oriented design language during migration

## API Shape

The `RCAAgent` CRD gains optional Phase 2-oriented schema for:

- `spec.signalProcessing`
- `spec.decision`
- `spec.observability.otlp`

These fields are intentionally:

- optional
- backend-agnostic
- safe to introduce before the full runtime implementation exists

## Rejected Alternatives

### Wait to change the CRD until all runtime features are implemented

Rejected because the repo needs a stable design target for docs, samples, and future controller work. Adding optional fields now gives contributors a clear contract without breaking current behavior.

### Add SigNoz-specific configuration directly to the API

Rejected because it would make the operator less portable and less CNCF-friendly. OTLP is the correct interoperability boundary.

### Create incident records only after the "meaningful incident" threshold

Rejected because it weakens lifecycle visibility and restart recovery. Persisting `Detecting` incidents is more consistent with the current Phase 1 model.

## Follow-up Work

- Introduce an internal normalized `Signal` contract between watchers and correlator
- Teach the runtime to honor `spec.signalProcessing` windows and enrichment toggles
- Implement namespace-aware autonomy evaluation from `spec.decision`
- Add OTLP instrumentation and collector/export wiring
- Keep existing Prometheus scraping support during the migration window
