# OpenTelemetry Integration

OTel is relevant to RCA Operator in two distinct ways:

1. **Operator self-instrumentation** — the operator emits its own traces (reconcile
   loops, signal processing, incident creation) to an OTel Collector via OTLP gRPC
2. **Application traces as topology source** — your applications send OTLP traces to a
   collector/backend, and the operator queries that backend (Jaeger or SigNoz) to build
   the service topology graph

Both can be active simultaneously. They are independent configurations.

---

## Part 1: Operator Self-Instrumentation

### What Gets Traced

When OTel is enabled, the RCA Operator emits spans for its core operations:

| Span Name | Attributes |
|-----------|-----------|
| `Reconcile` | `k8s.resource.kind`, `k8s.resource.name`, `k8s.resource.namespace` |
| `ProcessSignal` | `event.type`, `k8s.namespace`, `k8s.pod` |
| `EvaluateRule` | `rule.name`, `rule.priority` |
| `EnsureIncident` | `incident.type`, `incident.fingerprint` |

These spans allow you to trace the full path from an incoming Kubernetes event through
rule evaluation to incident creation. Long reconcile durations or failed rule evaluations
become immediately visible in your trace UI.

### Trace IDs in IncidentReports

When an incident is created, the trace ID of the active `EnsureIncident` span is written
to the `IncidentReport` annotation `rca.operator/trace-id`. You can copy this value
directly into the SigNoz or Jaeger trace explorer to jump to the exact span that created
the incident.

```bash
kubectl get incidentreport -n my-namespace \
  -o jsonpath='{.items[0].metadata.annotations.rca\.operator/trace-id}'
```

### Helm Configuration

OTel self-instrumentation requires an OTel Collector (or SigNoz OTel Collector) running
in the cluster.

```yaml
# helm/values-otel.yaml
otel:
  enabled: true
  # gRPC address — do NOT include http:// for gRPC endpoints
  endpoint: "signoz-otel-collector.platform:4317"
  serviceName: "rca-operator"
  # 1.0 = sample every span; reduce for high-throughput clusters
  samplingRate: "1.0"
  # Set to false if the collector uses TLS
  insecure: true
```

```bash
helm upgrade rca-operator ./helm -f helm/values-otel.yaml
```

### Environment Variable Override

The OTLP endpoint can be overridden at runtime without changing Helm values:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
make run ARGS="--telemetry-backend=signoz --signoz-endpoint=http://localhost:8080"
```

The environment variable takes precedence over the `otel.endpoint` Helm value.

### Viewing Operator Traces

In Jaeger or SigNoz, filter by service name `rca-operator` (or whatever value is in
`otel.serviceName`). To find traces for a specific incident:

1. Get the trace ID from the IncidentReport annotation (see above)
2. In SigNoz: navigate to **Traces** > search by trace ID
3. In Jaeger: navigate to **Search** > paste the trace ID

To find slow reconcile operations, sort by duration and look for `Reconcile` root spans.
Spans named `EnsureIncident` contain the `incident.fingerprint` attribute which matches
the `rca.operator/dedup-key` annotation on the IncidentReport.

### Sampling Rate

In clusters with many pods and frequent events, setting `samplingRate` to `"1.0"` may
produce a high volume of spans. Consider a lower rate for production:

```yaml
otel:
  samplingRate: "0.1"  # sample 10% of spans
```

Incident creation spans (`EnsureIncident`) are always worth sampling at a higher rate
than routine reconcile spans. If your collector supports tail-based sampling, configure
a rule to always keep spans that contain `incident.type` attributes.

### Local OTel Collector Quick-Start

For local development without a cluster collector, run a minimal OTel Collector with a
logging exporter:

```yaml
# otel-collector-local.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

exporters:
  logging:
    verbosity: detailed
  # Add jaeger or otlp exporter here to forward to a backend

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [logging]
```

```bash
# Run the collector locally (requires otelcol binary)
otelcol --config otel-collector-local.yaml &

export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
make run ARGS="--telemetry-backend=jaeger --jaeger-endpoint=http://localhost:16686"
```

Spans will appear in the collector's stdout output.

---

## Part 2: Application Traces as Topology Source

### How It Works

Your applications instrument their code with OTel SDKs and export spans via OTLP to an
OTel Collector, which forwards them to a backend (Jaeger or SigNoz). The RCA Operator
queries that backend's dependency API to construct the service topology graph. The
operator never touches the OTel Collector or receives trace data directly — it queries
the storage backend.

### Data Flow

```
Applications
    |
    | OTLP (gRPC or HTTP)
    v
OTel Collector
    |
    |-- Jaeger exporter --> Jaeger (stores spans, builds dependency graph)
    |                              |
    |                      RCA Operator queries /api/dependencies
    |
    |-- OTLP exporter --> SigNoz (stores spans, metrics, logs)
                                 |
                         RCA Operator queries dependency API
```

The OTel Collector can fan out to both backends simultaneously, which is useful during
a migration from Jaeger to SigNoz.

### OTel Collector Fan-Out Configuration

To send traces to both Jaeger and SigNoz at the same time:

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

exporters:
  jaeger:
    endpoint: jaeger-collector.otel-demo:14250
    tls:
      insecure: true

  otlp/signoz:
    endpoint: signoz-otel-collector.platform:4317
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [jaeger, otlp/signoz]
```

When both backends are populated, you can configure the RCA Operator with either backend
independently and both will return topology data.

### RCA Operator Backend Configuration

Once traces are flowing into your chosen backend, configure the operator to query it:

**Query Jaeger for topology:**

```bash
helm upgrade rca-operator ./helm \
  --set telemetry.enabled=true \
  --set telemetry.backend=jaeger \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686
```

**Query SigNoz for topology + metrics + logs:**

```bash
helm upgrade rca-operator ./helm \
  --set telemetry.enabled=true \
  --set telemetry.backend=signoz \
  --set telemetry.signoz.endpoint=http://signoz-query-service.platform:8080
```

### Verifying Trace Flow

Check that spans are arriving at the backend before debugging the operator:

```bash
# Jaeger: list services that have sent spans
curl -s http://localhost:16686/api/services | jq '.data'

# SigNoz: check health
curl -s http://localhost:8080/api/v1/health | jq '.status'
```

Then confirm the operator can see topology:

```bash
curl -s http://localhost:9090/api/topology | jq '{nodes: (.nodes|length), edges: (.edges|length)}'
```

---

## Combined Configuration (Self-Instrumentation + Topology Queries)

The most complete setup enables both: the operator emits its own traces to SigNoz, and
it queries SigNoz for application topology.

```yaml
# helm/values-full-otel.yaml
telemetry:
  enabled: true
  backend: signoz
  signoz:
    endpoint: "http://signoz-query-service.platform:8080"

otel:
  enabled: true
  endpoint: "signoz-otel-collector.platform:4317"
  serviceName: "rca-operator"
  samplingRate: "1.0"
  insecure: true
```

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-full-otel.yaml
```

After deployment, the operator's own spans appear in SigNoz under the service name
`rca-operator`, alongside your application services. You can correlate an
`EnsureIncident` span with the same time window as a spike in your application's
error rate.

---

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| No spans in SigNoz/Jaeger for `rca-operator` | `otel.enabled=false` | Set `otel.enabled=true` and redeploy |
| `connection refused` to OTel Collector | Wrong endpoint or collector not running | Verify collector pod is running; check endpoint does not include `http://` prefix for gRPC |
| Spans appear but `rca-operator` service not visible | Wrong service name | Check `otel.serviceName` matches what you search for in the UI |
| Topology graph empty despite traces in backend | Dependency processor not enabled | Jaeger requires `--processor.jaeger-metrics.storage-type` or the `dependencies` storage; SigNoz requires the dependency pipeline to be active |
| `OTEL_EXPORTER_OTLP_ENDPOINT` not taking effect | Environment variable not exported | Ensure `export` is used, not just assignment; verify with `env | grep OTEL` |
| Sampling rate too high for production | 1.0 sampling overwhelms collector | Set `otel.samplingRate` to `0.1` or `0.01`; configure tail-based sampling in collector |
| Application spans not creating topology edges | Missing `span.kind=CLIENT` | Outbound calls must emit CLIENT-kind spans for Jaeger/SigNoz to produce dependency edges |
| OTel Collector crashing | Exporter pipeline misconfigured | Check collector logs: `kubectl logs -n otel-system deploy/otel-collector` |

## Reference

- OpenTelemetry Collector documentation: https://opentelemetry.io/docs/collector/
- OTLP exporter specification: https://opentelemetry.io/docs/specs/otel/protocol/
- SigNoz integration: `docs/integrations/signoz.md`
- Jaeger integration: `docs/integrations/jaeger.md`
- Operator Helm values: `helm/values.yaml` — `otel` section
