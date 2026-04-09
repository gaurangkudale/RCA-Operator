# Prometheus Integration

RCA Operator integrates with Prometheus to display RED metrics (request rate, error rate,
latency) and resource metrics (CPU, memory) in the topology side panel. Prometheus
provides metrics only — it does not produce topology graphs. To use Prometheus you must
pair it with either Jaeger (composite mode) or SigNoz.

## What Prometheus Provides

| Signal | Used For |
|--------|----------|
| Request rate | Topology side panel: requests per second |
| Error rate | Topology side panel: percentage of 5xx responses |
| P99 latency | Topology side panel: 99th-percentile response time |
| CPU usage | Topology side panel: CPU cores consumed |
| Memory usage | Topology side panel: memory bytes consumed |

Prometheus cannot be used as the sole `telemetry.backend` because it has no concept of
service dependencies. It must be combined with a trace backend (Jaeger or SigNoz) that
provides the topology graph. SigNoz users get metrics automatically — this guide is
primarily for Jaeger + Prometheus (composite mode) or for operators already running a
standalone Prometheus stack.

## Backend Modes

| Mode | Topology Source | Metrics Source |
|------|-----------------|----------------|
| `signoz` | SigNoz | SigNoz |
| `jaeger` | Jaeger | None |
| `composite` | Jaeger | Prometheus |

Prometheus is only active when `backend=composite`.

## Prerequisites

- Prometheus v2.x deployed in the cluster (kube-prometheus-stack, VictoriaMetrics with
  PromQL compatibility, or standalone Prometheus)
- Your applications must expose metrics in Prometheus format and be scraped
- Standard HTTP metric labels (`service`, `namespace`) must be present for the PromQL
  queries to match

## Helm Installation

### Composite mode (Jaeger topology + Prometheus metrics)

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=composite \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686/jaeger/ui \
  --set telemetry.prometheus.endpoint=http://prometheus-server.monitoring:9090
```

### Values file approach

```yaml
# helm/values-composite.yaml
telemetry:
  enabled: true
  backend: composite
  jaeger:
    endpoint: "http://jaeger-query.otel-demo.svc:16686/jaeger/ui"
  prometheus:
    endpoint: "http://prometheus-server.monitoring:9090"

topology:
  refreshInterval: 30s
  dependencyWindow: 15m
```

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-composite.yaml
```

## Local Development

```bash
# Forward Jaeger (pod-level if headless service)
JAEGER_POD=$(kubectl get pod -n otel-demo \
  -l app.kubernetes.io/name=jaeger \
  -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686 &

# Forward Prometheus (use a different local port to avoid conflicts)
kubectl port-forward -n monitoring svc/prometheus-server 9091:9090 &

# Run operator with composite backend
make run ARGS="--telemetry-backend=composite \
  --jaeger-endpoint=http://localhost:16686/jaeger/ui \
  --prometheus-endpoint=http://localhost:9091"
```

## PromQL Queries

The operator executes the following PromQL queries when populating the topology side
panel for a selected service. The `<svc>` placeholder is replaced with the service name
as it appears in the Jaeger topology graph.

### Request Rate

```promql
rate(http_server_requests_total{service="<svc>"}[5m])
```

Returns requests per second averaged over the last 5 minutes.

### Error Rate

```promql
rate(http_server_requests_total{service="<svc>",status=~"5.."}[5m])
  /
rate(http_server_requests_total{service="<svc>"}[5m])
```

Returns the fraction of requests that resulted in a 5xx status code.

### P99 Latency

```promql
histogram_quantile(
  0.99,
  rate(http_server_request_duration_seconds_bucket{service="<svc>"}[5m])
)
```

Returns the 99th-percentile request duration in seconds (displayed as milliseconds in
the UI).

### CPU Usage

```promql
rate(container_cpu_usage_seconds_total{service="<svc>"}[5m])
```

Returns CPU cores consumed averaged over 5 minutes.

### Memory Usage

```promql
container_memory_working_set_bytes{service="<svc>"}
```

Returns current working set memory in bytes.

## Metric Label Requirements

The PromQL queries rely on a `service` label that matches the service name as reported
by Jaeger. If your Prometheus metrics use different label names (e.g., `app`,
`job`, `kubernetes_service_name`), the queries will return empty results.

Check what labels your metrics have:

```bash
curl -s "http://localhost:9091/api/v1/labels" | jq '.data[]' | grep -i service
```

If the label name differs, you have two options:

1. Add a relabeling rule in your Prometheus scrape config or ServiceMonitor to rename
   the label to `service`
2. Use a recording rule to create a new metric series with the correct label

## ServiceMonitor Example

If you use the Prometheus Operator (kube-prometheus-stack), add a ServiceMonitor for
your application with a relabeling rule to ensure the `service` label is present:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: my-app
  namespace: my-namespace
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: my-app
  endpoints:
    - port: http
      path: /metrics
      relabelings:
        - sourceLabels: [__meta_kubernetes_service_name]
          targetLabel: service
```

This ensures the `service` label on all scraped metrics matches the Kubernetes service
name, which in turn should match the service name visible in Jaeger traces.

## Verification

### Prometheus reachable

```bash
curl "http://localhost:9091/api/v1/query?query=up" | jq '.status'
```

Expected: `"success"`.

### Metrics exist for a service

Replace `frontend` with an actual service name from your topology:

```bash
curl -s "http://localhost:9091/api/v1/query" \
  --data-urlencode 'query=rate(http_server_requests_total{service="frontend"}[5m])' \
  | jq '.data.result'
```

A non-empty result array means the metrics exist and the label matches.

### Topology side panel shows metrics

1. Open `http://localhost:9090` (RCA Operator dashboard)
2. Navigate to the **Topology** tab
3. Click any service node
4. The side panel should show Request Rate, Error Rate, and P99 Latency values

If values show as "N/A", check the troubleshooting section below.

### Topology API with metrics

```bash
curl -s "http://localhost:9090/api/topology" | jq '.nodes[0].metrics'
```

Each node object should contain a `metrics` field with rate, error, and latency values.

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| Side panel shows "No metrics available" | Prometheus not reachable | Verify `--prometheus-endpoint` value; run `curl` against it from operator pod |
| Side panel shows "N/A" for all metrics | PromQL returns empty results | Check that `service` label exists on metrics; see label requirements section |
| Empty PromQL results for known services | Label name mismatch | Add relabeling in ServiceMonitor to produce `service` label matching Jaeger service name |
| Request rate is 0 but traffic is flowing | Wrong metric name | Your app may use `http_requests_total` instead of `http_server_requests_total`; verify with `curl .../metrics` |
| P99 latency always shows 0 | No histogram metric | App must emit `_bucket`, `_count`, `_sum` histogram metrics; summary metrics are not supported |
| Prometheus returns 401 | Auth enabled on Prometheus | Add bearer token via environment variable `PROMETHEUS_TOKEN` (if supported by your operator version) |
| Topology graph empty despite composite mode | Jaeger endpoint wrong | Topology still comes from Jaeger in composite mode; fix the Jaeger endpoint first |
| CPU/memory missing | cAdvisor not scraped | Ensure node-exporter and kube-state-metrics are part of your Prometheus stack |

## Reference

- Prometheus HTTP API: https://prometheus.io/docs/prometheus/latest/querying/api/
- PromQL documentation: https://prometheus.io/docs/prometheus/latest/querying/basics/
- Jaeger integration (composite mode): `docs/integrations/jaeger.md`
- SigNoz integration (unified backend): `docs/integrations/signoz.md`
- Operator Helm values: `helm/values.yaml` — `telemetry.prometheus` section
