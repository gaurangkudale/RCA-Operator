# Phase 2 Release Notes

Phase 2 turns RCA Operator from Kubernetes-only incident detection into a
trace-aware incident correlation platform. It still keeps the core contract
simple: durable `IncidentReport` CRs are the source of truth, and the operator
does not require an external database.

## What's New

### OTLP ingest

The operator can run an in-process OTLP/HTTP receiver on port `4319`. A bundled
OpenTelemetry Collector DaemonSet forwards filtered error spans and warn/error
logs to this endpoint, where they become normal correlator signals:

- `OTelSpanError`
- `OTelSpanLatencySpike`
- `OTelLogMatch`
- `OTelSpanEvent`

See [OTLP Ingest](../features/otlp-ingest.md).

### Trace correlation

`RCACorrelationRule` conditions now support `sameTrace` scope, allowing rules
to correlate Kubernetes failures and telemetry failures that share a W3C trace
ID. OTel attribute predicates can match semantic-convention fields such as
`http.status_code`, `service.name`, and `db.system`.

### Topology graph

`IncidentReport.status.incidentGraph` stores a graph of affected Kubernetes
resources, service nodes, trace spans, ownership edges, scheduling edges, and
optional Jaeger service-dependency edges. The dashboard reads this payload
directly from the CR.

See [Topology Graph](../features/topology-graph.md).

### Dashboard upgrades

The dashboard now includes:

- incident details backed by `IncidentReport`
- workload topology and service topology modes
- inline trace detail modal through `/api/traces/{traceID}`
- local static CSS/icons for air-gapped clusters
- RCAAgent, RCACorrelationRule, logs, and timeline views

See [Dashboard](../features/DASHBOARD.md).

### Auto-detection

The auto-detector can mine the correlation buffer for recurring signal patterns
and create low-priority auto-generated `RCACorrelationRule` CRs. This is useful
for discovering environment-specific incidents after enough baseline traffic is
available.

See [Auto-Detection](../features/auto-detection.md).

### Metrics

Phase 2 exposes RCA-specific Prometheus metrics for signal ingestion,
deduplication, incident lifecycle transitions, active incidents, and transition
durations, alongside the standard controller-runtime metrics.

See [Metrics Reference](../reference/metrics.md).

## Install Profiles

| Profile | Values file | Intended use |
|---|---|---|
| Full | `helm/values-full.yaml` or chart defaults | Demos, evaluation, clusters without tracing |
| Minimal | `helm/values-minimal.yaml` | Operator-only Kubernetes incident detection |
| External observability | `helm/values-external-observability.yaml` | Existing Collector and Jaeger deployments |

See [Installation](../getting-started/installation.md).

## Upgrade Notes

- Apply RCA CRDs before `helm upgrade` when upgrading an existing release.
- Use `--wait --timeout 10m` for the full profile because OTel CRs are Helm
  hooks that depend on OTel Operator webhooks being Ready.
- Review Jaeger storage before production use. The bundled all-in-one Jaeger
  defaults are suitable for evaluation, not long-retention production tracing.

See [Helm Upgrade Guide](../HELM_UPGRADE.md).
