# Integrations

RCA Operator integrates with external observability backends to query trace data,
metrics, logs, and service topology. The operator does not run any of these backends
— it queries them read-only to enrich incident context and build the service dependency
graph.

## Quick Reference

| Backend | Topology | Metrics | Logs | Recommended For |
|---------|----------|---------|------|-----------------|
| SigNoz | Yes | Yes | Yes | Production — unified single endpoint |
| Jaeger | Yes | No | No | Existing Jaeger deployments |
| Composite (Jaeger + Prometheus) | Yes | Yes | No | Jaeger with separate Prometheus stack |
| Prometheus (standalone) | No | Yes | No | Not supported alone — use composite |

## Guides

### [SigNoz](./signoz.md)

The recommended backend. Provides traces, metrics, logs, and topology through a single
query service endpoint. Covers Helm installation, combined OTel collector configuration,
local development with port-forwarding, and verification steps.

### [Jaeger](./jaeger.md)

Provides distributed traces and service topology via the `/api/dependencies` endpoint.
Does not provide metrics or logs. Covers standard and base-path endpoint formats, the
headless service port-forwarding workaround for OTel Demo deployments, and composite
mode setup.

### [Prometheus](./prometheus.md)

Provides RED metrics (request rate, error rate, P99 latency) and resource metrics for
the topology side panel. Must be paired with Jaeger or SigNoz for topology data. Covers
composite mode Helm configuration, the PromQL queries the operator executes, and
ServiceMonitor label requirements.

### [OpenTelemetry](./opentelemetry.md)

Covers two separate topics:

1. **Operator self-instrumentation** — emitting traces from the RCA Operator itself
   (reconcile loops, signal processing, incident creation) to an OTel Collector via
   OTLP gRPC
2. **Application traces as topology source** — configuring the OTel Collector to fan
   out traces to Jaeger/SigNoz so the operator can query the dependency graph

## Topology Feature

For an overview of how RCA Operator builds and visualizes the service dependency graph
— including the dashboard Topology tab, blast radius calculation, and topology refresh
configuration — see:

### [Topology Feature](../features/TOPOLOGY.md)

## Helm Values Reference

All backend configuration lives in the `telemetry`, `otel`, and `ai` sections of
`helm/values.yaml`:

```yaml
telemetry:
  enabled: true
  backend: signoz|jaeger|composite

  signoz:
    endpoint: "http://signoz-query-service.platform:8080"

  jaeger:
    endpoint: "http://jaeger-query:16686"
    # or with base path: "http://jaeger-query:16686/jaeger/ui"

  prometheus:
    endpoint: "http://prometheus-server.monitoring:9090"

otel:
  enabled: true
  endpoint: "signoz-otel-collector.platform:4317"  # gRPC, no http://
  serviceName: "rca-operator"
  samplingRate: "1.0"
  insecure: true

ai:
  enabled: true
  endpoint: "https://api.openai.com/v1"
  model: "gpt-4o"
  secretRef: "openai-api-key"
  autoInvestigate: true
```

## CLI Flags

When running the operator outside the cluster (`make run`), the equivalent flags are:

| Flag | Helm Value |
|------|-----------|
| `--telemetry-backend` | `telemetry.backend` |
| `--signoz-endpoint` | `telemetry.signoz.endpoint` |
| `--jaeger-endpoint` | `telemetry.jaeger.endpoint` |
| `--prometheus-endpoint` | `telemetry.prometheus.endpoint` |
| `--topology-refresh-interval` | `topology.refreshInterval` |
| `--topology-dependency-window` | `topology.dependencyWindow` |

## Dashboard Endpoints

The RCA Operator exposes HTTP endpoints on port 9090 (configurable via `--dashboard-bind-address`):

| Endpoint | Description |
|----------|-------------|
| `GET /api/topology` | Current service dependency graph (nodes + edges) |
| `GET /api/services` | List of services discovered from the configured backend |
| `POST /api/investigate` | Trigger AI investigation for an incident |
