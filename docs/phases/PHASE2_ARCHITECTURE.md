# Phase 2 Architecture: Cross-Signal Observability and AI-Powered RCA

## Overview

Phase 2 extends the RCA Operator from single-signal Kubernetes event detection to cross-signal observability by integrating external telemetry backends (traces, logs, metrics), adding interactive topology visualization, and enabling AI-powered root cause analysis.

### Design Principle: Query, Don't Ingest

The operator does NOT run the observability stack. It **queries** external systems (SigNoz, Jaeger, Prometheus) for telemetry data and correlates it with K8s-native signals. The external stack is deployed separately by the platform team.

## New Packages

### `internal/telemetry/` - Telemetry Query Layer

Central abstraction for querying external observability backends via the `TelemetryQuerier` interface.

```
types.go            -- Data types: Trace, Span, MetricSeries, LogEntry, DependencyEdge
querier.go          -- TelemetryQuerier interface + NoopQuerier
signoz_client.go    -- SigNoz Query Service REST API client
jaeger_client.go    -- Jaeger v2 HTTP Query API client
prometheus_client.go -- Prometheus PromQL HTTP API client
composite.go        -- Composite querier (delegates to Jaeger+Prometheus or full-composite)
```

**TelemetryQuerier Interface:**

| Method | Signal | Description |
|---|---|---|
| `FindTracesByService` | Traces | Find traces by service name and time window |
| `GetTrace` | Traces | Get full trace detail by trace ID |
| `FindErrorTraces` | Traces | Find error traces for a service |
| `QueryMetric` | Metrics | Execute PromQL-style query |
| `GetServiceMetrics` | Metrics | Get RED metrics (Rate, Errors, Duration) for a service |
| `SearchLogs` | Logs | Search logs by service, severity, keyword, trace ID |
| `GetDependencies` | Topology | Get service dependency edges from span relationships |
| `CorrelateByTraceID` | Cross-signal | Get correlated traces + logs + metrics for a trace ID |

**Backend Capability Matrix:**

| Backend | Traces | Metrics | Logs | Topology |
|---|---|---|---|---|
| `signoz` | `/api/v3/traces` | `/api/v3/query_range` | `/api/v3/logs` | `/api/v1/services/dependencies` |
| `jaeger` | `/api/traces/{id}` | N/A | N/A | `/api/dependencies` |
| `prometheus` | N/A | `/api/v1/query_range` | N/A | N/A |
| `composite` | Jaeger | Prometheus | No | Jaeger |
| `full-composite` | Jaeger | Prometheus | SigNoz | Jaeger |

### `internal/topology/` - Service Dependency Graph

Builds and caches a service topology DAG from OTel span parent-child relationships.

```
graph.go            -- ServiceGraph, ServiceNode, ServiceEdge data structures
builder.go          -- Builds graph from TelemetryQuerier + enriches with metrics/incidents
blast_radius.go     -- BFS blast radius calculation (upstream + downstream)
cache.go            -- TTL-based in-memory cache with background refresh
```

**How the DAG is built:**

1. Query telemetry backend for dependency edges (Jaeger `/api/dependencies` or SigNoz service deps)
2. Build graph skeleton from caller-callee edges
3. Enrich nodes with per-service metrics (CPU, MEM, request rate, error rate, latency)
4. Overlay incident data (map active IncidentReport CRs to service nodes)
5. Infer UI icons from service names (gateway, database, queue, etc.)

**Blast Radius Algorithm:**

When an incident fires on service X:
- BFS upstream: find all callers that depend on X (their requests will fail)
- BFS downstream: find all callees that X calls (may have orphaned connections)
- Union = full blast radius
- `ComputeUpstreamBlastRadius` returns only upstream (narrower, used in incident reports)

## CRD Changes

All changes are backward-compatible optional additions to `v1alpha1`.

### RCAAgent - New Fields

```yaml
spec:
  telemetry:
    backend: signoz              # signoz | jaeger | composite
    signoz:
      endpoint: http://signoz-query-service:8080
    jaeger:
      endpoint: http://jaeger-query:16686
      grpcEndpoint: jaeger-query:16685
    prometheus:
      endpoint: http://prometheus:9090
  ai:
    enabled: true
    endpoint: https://api.openai.com/v1
    model: gpt-4o
    secretRef: openai-api-key    # Secret with key "apiKey"
    autoInvestigate: true        # Auto-trigger on Active incidents
```

### IncidentReport - New Status Fields

```yaml
status:
  rca:
    rootCause: "Memory leak in payment-svc caused OOMKilled events"
    confidence: "0.92"
    playbook:
      - "kubectl rollout undo deployment/payment-svc -n production"
      - "kubectl scale deployment/payment-svc --replicas=6"
    evidence:
      - "Trace abc123: 500ms latency spike on /checkout"
      - "Log: java.lang.OutOfMemoryError at 10:15:01"
    investigatedAt: "2026-04-08T10:20:00Z"
  relatedTraces:
    - "abc123def456"
    - "789ghi012jkl"
  blastRadius:
    - "api-gateway"
    - "frontend"
```

## Dashboard API - New Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/topology` | ServiceGraph JSON (nodes + edges + metrics) |
| `GET` | `/api/topology/blast?service=X` | Blast radius for service X |
| `GET` | `/api/services` | List discovered services with health status |
| `GET` | `/api/services/{name}` | Single service node detail |
| `GET` | `/api/services/{name}/metrics` | RED + resource metrics for a service |
| `GET` | `/api/services/{name}/traces` | Recent traces for a service |
| `GET` | `/api/services/{name}/logs` | Recent logs for a service |
| `GET` | `/api/agents` | All RCAAgent resources with health, conditions, and configuration summary |
| `GET` | `/api/investigate/{ns}/{name}` | Get existing AI RCA result for an incident |
| `POST` | `/api/investigate/{ns}/{name}` | Trigger AI investigation for an incident |
| `SSE` | `/api/stream/topology` | Live topology graph updates |
| `SSE` | `/api/stream/correlation` | Live correlation signal stream |

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--telemetry-backend` | (empty) | Backend type: `signoz`, `jaeger`, `composite`, or `full-composite`. Empty disables telemetry. |
| `--signoz-endpoint` | (empty) | SigNoz query service URL. Used when backend is `signoz` or `full-composite`. |
| `--jaeger-endpoint` | (empty) | Jaeger query HTTP API URL. Used when backend is `jaeger`, `composite`, or `full-composite`. |
| `--prometheus-endpoint` | (empty) | Prometheus HTTP API URL. Used when backend is `composite` or `full-composite`. |
| `--topology-refresh-interval` | `30s` | How often to refresh the topology graph cache |
| `--topology-dependency-window` | `15m` | Time window for querying service dependencies |
| `--ai-endpoint` | (empty) | OpenAI-compatible API endpoint. Empty disables AI investigation. |
| `--ai-model` | `gpt-4o` | LLM model name for AI investigation |
| `--ai-secret-ref` | (empty) | Kubernetes Secret name containing API key (key: `apiKey`) |
| `--ai-auto-investigate` | `false` | Auto-trigger AI investigation on Active incidents |

See [CLI Flags reference](../reference/cli-flags.md) for the complete flag list.

## Helm Values

```yaml
telemetry:
  enabled: false
  backend: composite
  signoz:
    endpoint: ""
  jaeger:
    endpoint: ""
    grpcEndpoint: ""
  prometheus:
    endpoint: ""

ai:
  enabled: false
  endpoint: ""
  model: "gpt-4o"
  secretRef: ""
  autoInvestigate: false

topology:
  enabled: true
  refreshInterval: 30s
  dependencyWindow: 15m
```

## Implementation Milestones

| Milestone | Status | Description |
|---|---|---|
| M1: Telemetry Query Layer + Topology | **Done** | `internal/telemetry/`, `internal/topology/`, CRD extensions, dashboard API endpoints |
| M2: Dashboard Topology Visualization | **Done** | Interactive SVG topology view, SSE hub, service detail panel with metrics/blast radius |
| M3: Cross-Signal Enrichment | **Done** | `CrossSignalEnricher` in correlator, auto-populates `RelatedTraces` + `BlastRadius` on incidents |
| M4: AI/LLM Investigation | **Done** | `internal/rca/` with OpenAI-compatible LLM client, tool-use agentic pattern, PII redaction, `/api/investigate` endpoint |
| M5: OTel Instrumentation + Helm | **Done** | OTLP metrics export (30s periodic reader), enhanced span instrumentation, Helm chart with telemetry/ai/topology values |

## Recommended Stack

For the simplest deployment, use **SigNoz** as a unified backend:

```
Applications --OTLP--> OTel Collector --OTLP--> SigNoz (ClickHouse)
                                                     |
                                              RCA Operator queries
```

SigNoz stores traces, logs, and metrics in a single ClickHouse cluster, enabling cross-signal correlation via SQL JOINs on `trace_id`. This eliminates the "tri-database problem" of running Jaeger + Prometheus + ELK separately.

For existing clusters with Jaeger and Prometheus already deployed, use **composite mode** to query both backends through the unified `TelemetryQuerier` interface.
