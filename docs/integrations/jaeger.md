# Jaeger Integration

RCA Operator integrates with Jaeger to build service topology graphs from distributed
trace data. Jaeger provides traces and service dependency information but does not provide
metrics or logs. For metrics alongside topology, use composite mode (Jaeger + Prometheus).

## What Jaeger Provides

| Signal | Used For |
|--------|----------|
| Traces | Service topology graph via `/api/dependencies` |
| Service list | Node labels in the topology graph |
| Call counts | Edge weights (number of calls between services) |

Jaeger does **not** provide metrics (request rates, latencies, CPU, memory). The topology
side panel will show dependency edges but no RED metric values when Jaeger is the sole
backend. To add metrics, see composite mode below or `docs/integrations/prometheus.md`.

## Prerequisites

- Jaeger v1.45 or later (v2 supported)
- The Jaeger query HTTP API must be reachable from the RCA Operator pod
- At least some inter-service traces must exist in Jaeger for dependency edges to appear

## Endpoint Formats

The Jaeger endpoint value must match how Jaeger is deployed:

| Deployment | Endpoint Value |
|------------|---------------|
| Default (no base path) | `http://jaeger-query:16686` |
| OTel Demo (`--query.base-path=/jaeger/ui`) | `http://jaeger-query.otel-demo.svc:16686/jaeger/ui` |
| Behind an ingress at `/jaeger` | `https://observability.example.com/jaeger` |

The operator appends `/api/dependencies` and `/api/services` to the endpoint value, so
the value must include any base path prefix and must **not** have a trailing slash.

### OTel Demo Base Path Note

The OpenTelemetry Demo Helm chart configures Jaeger with
`--query.base-path=/jaeger/ui`. This shifts all API paths:

- Without base path: `GET /api/dependencies`
- With base path: `GET /jaeger/ui/api/dependencies`

If you set the endpoint to `http://jaeger-query:16686` (without the base path), the
operator's API calls will receive HTML back (Jaeger's SPA index page) instead of JSON.
Always include the base path in the endpoint value when it is configured.

### Headless Service Warning

The OTel Demo's `jaeger-query` service may be deployed as headless (ClusterIP: None).
A headless service cannot be used with `kubectl port-forward svc/...` because there is
no stable cluster IP to forward to. Port-forward to the pod directly:

```bash
JAEGER_POD=$(kubectl get pod -n otel-demo \
  -l app.kubernetes.io/name=jaeger \
  -o jsonpath='{.items[0].metadata.name}')

kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686
```

## Helm Installation

### Jaeger only

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=jaeger \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686/jaeger/ui
```

### Jaeger + Prometheus (composite mode, recommended)

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=composite \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686/jaeger/ui \
  --set telemetry.prometheus.endpoint=http://prometheus-server.monitoring:9090
```

See `docs/integrations/prometheus.md` for full composite mode documentation.

## Topology Refresh Configuration

```bash
helm upgrade rca-operator ./helm \
  --set topology.refreshInterval=30s \
  --set topology.dependencyWindow=15m
```

The dependency window controls how far back in time the operator looks when calling
`/api/dependencies`. Longer windows produce more complete graphs but may surface stale
edges from services that are no longer communicating.

## Local Development

```bash
# Step 1 — forward Jaeger query port to localhost
# If jaeger-query is a headless service, use pod-level forwarding:
JAEGER_POD=$(kubectl get pod -n otel-demo \
  -l app.kubernetes.io/name=jaeger \
  -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686

# Step 2 — run operator with Jaeger backend
make run ARGS="--telemetry-backend=jaeger \
  --jaeger-endpoint=http://localhost:16686/jaeger/ui"
```

For composite mode locally:

```bash
kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686 &
kubectl port-forward -n monitoring svc/prometheus-server 9091:9090 &

make run ARGS="--telemetry-backend=composite \
  --jaeger-endpoint=http://localhost:16686/jaeger/ui \
  --prometheus-endpoint=http://localhost:9091"
```

## Verification

### Confirm Jaeger has dependency data

```bash
NOW=$(python3 -c "import time; print(int(time.time() * 1000))")
curl -s "http://localhost:16686/jaeger/ui/api/dependencies?endTs=${NOW}&lookback=3600000" \
  | jq '.data[:5]'
```

A non-empty array indicates Jaeger has inter-service call data. If the array is empty:

1. The load generator may not be running — start it and wait a few minutes
2. The lookback value (milliseconds) may be too short — increase it
3. Services may not be instrumented with distributed tracing

### Check the services list

```bash
curl -s "http://localhost:16686/jaeger/ui/api/services" | jq '.data'
```

### Confirm operator is building topology

```bash
curl -s http://localhost:9090/api/topology | jq '{nodes: (.nodes | length), edges: (.edges | length)}'
```

### Dashboard

Open `http://localhost:9090` and navigate to the **Topology** tab. You should see
service nodes connected by directed edges. Edge labels show call counts from the Jaeger
dependency graph.

When Jaeger is the sole backend, clicking a node opens the side panel showing topology
position (upstream/downstream services) but **no metric values**. Metric rows display
"N/A" or are hidden. Switch to composite mode to populate them.

## What Data RCA Gets from Jaeger

- **Service topology graph**: derived from span parent-child relationships aggregated
  by Jaeger's dependency processor
- **Call counts on edges**: the `callCount` field from `/api/dependencies` is used as
  edge weight in the topology visualization
- **Blast radius**: when an incident targets a service, the operator traverses topology
  edges to identify dependent upstream services that may be impacted

No log or metric data is sourced from Jaeger.

## Limitation: No Metrics

When `backend=jaeger`, the topology side panel will not show RED metrics (request rate,
error rate, latency) or resource metrics (CPU, memory). These require a Prometheus
data source.

To enable metrics alongside Jaeger topology:

```bash
helm upgrade rca-operator ./helm \
  --set telemetry.backend=composite \
  --set telemetry.prometheus.endpoint=http://prometheus-server.monitoring:9090
```

The Jaeger endpoint setting is preserved when upgrading to composite mode.

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `/api/topology` returns empty nodes | No dependency data in Jaeger | Run the load generator; verify `/api/dependencies` returns data directly |
| Jaeger API returns HTML instead of JSON | Wrong base path in endpoint | Include the base path: `http://jaeger-query:16686/jaeger/ui` |
| `connection refused` port-forwarding via `svc/jaeger-query` | Service is headless (ClusterIP: None) | Use pod-level port-forward: `kubectl port-forward pod/$JAEGER_POD 16686:16686` |
| Empty dependency graph despite traffic | Lookback window too short | Increase `topology.dependencyWindow`; OTel Demo default is low traffic, wait longer |
| Services list is empty | Jaeger has no spans | Verify your apps are sending traces to the Jaeger collector |
| 404 on `/api/dependencies` | Jaeger version too old or wrong path | Upgrade to Jaeger v1.45+; check the exact path with `curl -v` |
| Topology graph nodes but no edges | No CLIENT-kind spans or no inter-service calls | Ensure apps emit `span.kind=CLIENT` spans for outbound calls |

## Reference

- Jaeger HTTP API: https://www.jaegertracing.io/docs/latest/apis/
- Composite mode (Jaeger + Prometheus): `docs/integrations/prometheus.md`
- Operator Helm values: `helm/values.yaml` — `telemetry.jaeger` section
- Topology feature overview: `docs/features/TOPOLOGY.md`
