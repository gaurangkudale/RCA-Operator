# OTLP Ingest

RCA Operator embeds an OTLP/HTTP receiver inside the controller-manager. The
cluster-wide OTel Collector DaemonSet forwards a filtered subset of traces
and logs to this endpoint, and the operator turns them into correlation
signals alongside the native Kubernetes event signals.

---

## Why it exists

Kubernetes events only tell you *that* something broke (CrashLoop, OOMKilled,
NodeNotReady). OTel spans and logs tell you *where* and *why* (HTTP 500,
latency spike, exception). Ingesting both into one correlator lets rules
span both planes — e.g. "P2 NodeFailure if two pods evict AND their upstream
service is returning 5xx".

---

## Data flow

```
App → OTel Collector (DaemonSet)
        │        ├── full pipeline → Jaeger
        │        └── filter/errors pipeline → Operator OTLP ingest (:4319)
        │                                       │
        │                                       └── Correlator
        │                                             │
        │                                             └── IncidentReport
```

The collector fans out *everything*; the operator only sees what matches the
filter. This keeps the correlator buffer small and the chart self-contained.

---

## Endpoints

The ingest server listens on `:4319` inside the pod and is exposed cluster-
internally at `rca-operator-otel-ingest.<release-namespace>.svc:4319`.

| Path | Accepts | Encoding |
|---|---|---|
| `POST /v1/traces` | `ExportTraceServiceRequest` | protobuf or JSON |
| `POST /v1/logs`   | `ExportLogsServiceRequest`  | protobuf or JSON |
| `GET /healthz`    | — | — |

A `NetworkPolicy` restricts ingress on port 4319 to the OTel Collector
DaemonSet's ServiceAccount — arbitrary workloads cannot push directly.

---

## Filter knobs (Helm)

```yaml
# helm/values.yaml (defaults shown)
rcaIngest:
  enabled: true
  bindPort: 4319
  networkPolicy:
    enabled: true

otelIngest:
  filters:
    traces:
      statusCodeERROR: true   # keep spans with Status.Code == ERROR
      httpStatusGte: 500      # keep spans where http.status_code >= 500 (0 disables)
      latencyP99Ms: 5000      # keep spans slower than 5000ms (0 disables)
    logs:
      minSeverity: "WARN"     # drop records below WARN at ingest
```

Filters are applied **twice**: once in the Collector (OTTL processor) so only
relevant telemetry leaves the node, and once in the operator's ingest server
as the hard gate before signals enter the correlator buffer.

---

## Signals produced

| OTel input | Correlator signal |
|---|---|
| Span with `Status.Code == ERROR` | `OTelSpanError` |
| Span with `http.status_code >= 500` | `OTelSpanError` |
| Span with duration >= `latencyP99Ms` | `OTelSpanLatencySpike` |
| Log record at/above `minSeverity` | `OTelLogMatch` |
| Span event such as an exception event | `OTelSpanEvent` |

Write `RCACorrelationRule`s against these signal types exactly as you would
for native Kubernetes signals.

---

## Disabling

```bash
helm upgrade rca-operator rca-operator/rca-operator \
  --set rcaIngest.enabled=false
```

The Service, NetworkPolicy, and collector forwarding pipeline are all gated
on `rcaIngest.enabled`, so disabling cleanly removes the surface area.

---

## Forwarding from an existing Collector

If you already run your own OTel Collector, add an exporter pointing at the
operator's ingest Service:

```yaml
exporters:
  otlphttp/rca-operator:
    endpoint: http://rca-operator-otel-ingest.rca-system:4319
    compression: gzip
    tls:
      insecure: true   # in-cluster traffic

service:
  pipelines:
    traces/rca:
      receivers: [otlp]
      processors: [filter/errors, batch]
      exporters: [otlphttp/rca-operator]
```

See [helm/templates/otel-collector.yaml](../../helm/templates/otel-collector.yaml)
for the full pipeline the chart renders.
