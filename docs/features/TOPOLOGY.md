# Service Topology Visualization

## How Topology Works

The RCA Operator builds an interactive service dependency graph by querying external observability backends for distributed trace data. It does **not** run the observability stack itself — it queries existing deployments of SigNoz, Jaeger, or Prometheus.

### Data Flow

```
Applications ──OTLP──> OTel Collector ──> Jaeger / SigNoz (stores traces)
                                                 │
                                         RCA Operator queries
                                                 │
                                    ┌────────────┴─────────────┐
                                    │  internal/telemetry/      │
                                    │  TelemetryQuerier iface   │
                                    │  (SigNoz / Jaeger / Prom) │
                                    └────────────┬─────────────┘
                                                 │
                                    ┌────────────┴─────────────┐
                                    │  internal/topology/       │
                                    │  Builder → ServiceGraph   │
                                    │  Cache (30s TTL)          │
                                    │  BlastRadius (BFS)        │
                                    └────────────┬─────────────┘
                                                 │
                                    ┌────────────┴─────────────┐
                                    │  Dashboard REST API       │
                                    │  GET /api/topology        │
                                    │  GET /api/topology/blast  │
                                    │  GET /api/services        │
                                    └────────────┬─────────────┘
                                                 │
                                    ┌────────────┴─────────────┐
                                    │  Dashboard UI (index.html)│
                                    │  Interactive SVG graph    │
                                    │  Pan / Zoom / Click       │
                                    │  Side panel + blast radius│
                                    └──────────────────────────┘
```

### Graph Construction Steps

1. **Query dependencies**: Call `TelemetryQuerier.GetDependencies(ctx, window)` which calls:
   - Jaeger: `GET {endpoint}/api/dependencies?endTs={nowMs}&lookback={windowMs}`
   - SigNoz: `GET /api/v1/services/dependencies`
   - Returns `[]DependencyEdge{Parent, Child, CallCount, ErrorRate, AvgLatency}`

2. **Build graph skeleton**: Each dependency edge creates two `ServiceNode`s (if not exist) and a `ServiceEdge` between them.

3. **Enrich with metrics**: For each node, call `GetServiceMetrics(ctx, serviceName, window)` to get RED metrics (Request rate, Error rate, Duration) plus CPU/Memory.

4. **Overlay incidents**: Fetch all non-Resolved `IncidentReport` CRs from the Kubernetes API and match them to service nodes by name prefix. This sets node status:
   - **Critical**: Active incident with P1 or P2 severity
   - **Warning**: Active or Detecting phase incident
   - **Healthy**: No incidents

5. **Infer icons**: Service names are matched against known patterns (gateway, postgres, redis, kafka, auth, payment, frontend, etc.) to assign UI icons.

6. **Cache**: The graph is cached in memory with a configurable TTL (default 30s). A background goroutine refreshes it periodically.

### Blast Radius

When you click a service node in the topology view, the operator computes blast radius via BFS:
- **Upstream**: All services that call the affected service (their requests will fail)
- **Downstream**: All services that the affected service calls (may have orphaned connections)
- Affected services are highlighted with red dashed rings in the SVG

### Edge Status Classification

| Condition | Status | Visual |
|-----------|--------|--------|
| Error rate > 10% | Critical | Red dashed line |
| Error rate > 1% or latency > 1000ms | Warning | Amber dashed line |
| Otherwise | Active | Solid grey line |

## Dashboard Features

### Topology Tab

- **Interactive SVG**: Click nodes to select, scroll to zoom, drag to pan
- **Status-colored nodes**: Green (healthy), amber (warning), red (critical), grey (unknown)
- **Edge labels**: Show call count between services
- **Legend**: Health status reference at bottom-left
- **Refresh button**: Manually trigger cache refresh

### Side Panel (on node click)

- **Metrics**: Request rate, error rate, P99 latency, CPU, memory
- **Active Incidents**: List with severity badges
- **Blast Radius**: Tagged list of affected services
- **Quick Actions**: View recent traces, view recent logs, trigger AI investigation

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/topology` | Full ServiceGraph JSON (nodes + edges) |
| GET | `/api/topology/blast?service=X` | Blast radius for service X |
| GET | `/api/services` | List services with health status |
| GET | `/api/services/{name}` | Service node details |
| GET | `/api/services/{name}/metrics` | RED metrics for a service |
| GET | `/api/services/{name}/traces` | Recent traces for a service |
| GET | `/api/services/{name}/logs` | Recent logs for a service |

---

## Testing Topology with the OTel Demo Application

The [OpenTelemetry Demo](https://opentelemetry.io/docs/demo/) is a realistic microservices e-commerce application with 18 services that continuously generates distributed traces. It includes Jaeger and Prometheus out of the box — perfect for testing the topology visualization.

> **Verified with**: OTel Demo from `main` branch (`opentelemetry-demo.yaml`), Jaeger v1.53.0, kind cluster.

### Prerequisites

- A Kubernetes cluster (kind, minikube, or any cloud cluster)
- `kubectl` and `helm` configured
- At least **6 GB memory** available in the cluster

### Step 1: Deploy the OTel Demo

```bash
# Create a dedicated namespace
kubectl create namespace otel-demo

# Deploy the full OTel Demo stack
kubectl apply -n otel-demo \
  -f https://raw.githubusercontent.com/open-telemetry/opentelemetry-demo/main/kubernetes/opentelemetry-demo.yaml

# Wait for all pods to become ready (takes 3–8 minutes on first pull)
kubectl wait --for=condition=ready pod --all -n otel-demo --timeout=480s

# Confirm all 25 pods are Running 1/1
kubectl get pods -n otel-demo
```

The demo deploys these 18 observable services:

| Service | Role | Icon |
|---------|------|------|
| `frontend` | Web storefront | 🖥 |
| `frontend-proxy` | Envoy edge proxy / gateway | 🌐 |
| `cart` | Shopping cart (Valkey-backed) | ⚙ |
| `checkout` | Order processing | ⚙ |
| `payment` | Payment handling | 💳 |
| `product-catalog` | Product listing | ⚙ |
| `recommendation` | ML recommendations | ⚙ |
| `shipping` | Shipping quotes | ⚙ |
| `email` | Email notifications | ⚙ |
| `ad` | Advertising service | ⚙ |
| `currency` | Currency conversion | ⚙ |
| `quote` | Shipping quote service | ⚙ |
| `accounting` | Finance (Kafka consumer) | ⚙ |
| `fraud-detection` | Fraud analysis | ⚙ |
| `kafka` | Event streaming | 📨 |
| `valkey-cart` | Redis-compatible cache | 🗄 |
| `postgresql` | Database | 🗄 |
| `load-generator` | Synthetic traffic (Locust) | ⚙ |

It also deploys `jaeger`, `prometheus`, `grafana`, `opensearch`, and `otel-collector`.

### Step 2: Verify Jaeger Has Trace Data

> **Important**: The OTel Demo configures Jaeger with `--query.base-path=/jaeger/ui`.  
> The service name is `jaeger-query` (not `jaeger`), and it is **headless** — port-forward to the pod.

```bash
# Get the Jaeger pod name
JAEGER_POD=$(kubectl get pod -n otel-demo -l app.kubernetes.io/name=jaeger \
  -o jsonpath='{.items[0].metadata.name}')
echo "Jaeger pod: $JAEGER_POD"

# Port-forward directly to the pod (headless service requires this)
kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686

# Open the Jaeger UI
open http://localhost:16686/jaeger/ui
# (or navigate there in your browser)
```

Verify the topology builder's dependency endpoint returns data:

```bash
# The OTel Demo Jaeger has base-path=/jaeger/ui — API is at /jaeger/ui/api/
curl -s "http://localhost:16686/jaeger/ui/api/services" | jq '.data'
# Expected: ["cart","checkout","frontend","payment","shipping",...]

# Query dependencies (replace NOW with current epoch milliseconds)
NOW=$(python3 -c "import time; print(int(time.time() * 1000))")
curl -s "http://localhost:16686/jaeger/ui/api/dependencies?endTs=${NOW}&lookback=3600000" | jq '.data[:5]'
```

You should see output like:
```json
[
  {"parent": "frontend",  "child": "cart",     "callCount": 130},
  {"parent": "checkout",  "child": "payment",  "callCount": 15},
  {"parent": "checkout",  "child": "email",    "callCount": 15},
  {"parent": "load-generator", "child": "flagd", "callCount": 87},
  {"parent": "frontend",  "child": "product-catalog", "callCount": 92}
]
```

> If `callCount` values are zero or the list is empty, wait 2–3 more minutes for the load generator to produce trace data, then retry.

### Step 3: Deploy RCA Operator with Jaeger Backend

> **Key**: Pass `--jaeger-endpoint` with the `/jaeger/ui` path suffix so the operator appends `/api/dependencies` correctly.

**With Helm (in-cluster):**

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=jaeger \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686/jaeger/ui

kubectl get pods -n rca-system
```

**With `make run` (local development against the cluster):**

```bash
# Port-forward Jaeger to localhost first
JAEGER_POD=$(kubectl get pod -n otel-demo -l app.kubernetes.io/name=jaeger \
  -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n otel-demo pod/$JAEGER_POD 16686:16686 &

# Run the operator locally
make run ARGS="--telemetry-backend=jaeger --jaeger-endpoint=http://localhost:16686/jaeger/ui"
```

### Step 4: Access the Topology Dashboard

```bash
# If running via Helm, port-forward the dashboard service
kubectl port-forward -n rca-system svc/rca-operator-controller-manager-dashboard 9090:9090 &

# Open the dashboard
open http://localhost:9090
```

Click the **Topology** tab. You should see:

- **18 service nodes** arranged in a grid
- **21+ edges** with directional arrows showing call flow
- **Call counts** on edges (e.g., "130 calls")
- **Status-colored nodes**: all green initially (no incidents)
- **Icons**: 🌐 for `frontend-proxy`, 💳 for `payment`, 🗄 for `valkey-cart` / `postgresql`, 📨 for `kafka`

### Step 5: Create an RCAAgent and Simulate an Incident

```bash
# Create an RCAAgent watching the otel-demo namespace
cat <<'EOF' | kubectl apply -f -
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: otel-demo-agent
  namespace: otel-demo
spec:
  watchNamespaces:
    - otel-demo
  signalCooldown: 2m
  stabilizationWindow: 30s
EOF

# Simulate a cart failure by scaling it to 0
kubectl scale deployment cart -n otel-demo --replicas=0

# Watch for an IncidentReport to appear (30s–2 minutes)
kubectl get incidentreport -n otel-demo -w
```

After the incident is created, **refresh the Topology tab**. You should see:

- `cart` node turns **red** (Critical)
- `checkout` and `frontend` nodes may turn **amber** (Warning — they call `cart`)
- Incident badge appears under the `cart` node label

### Step 6: Test Blast Radius

1. **Click the `cart` node** in the topology graph
2. The **side panel** opens on the right showing:
   - Status: critical (red)
   - Active incident with severity badge
   - **Blast Radius** section: `checkout`, `frontend` (services that call `cart`)
3. Affected services in the blast radius are highlighted with **red dashed rings**
4. Use the API directly to inspect:

```bash
curl -s "http://localhost:9090/api/topology/blast?service=cart" | jq .
# Expected: ["checkout", "frontend", ...]
```

### Step 7: Test with Composite Mode (Jaeger + Prometheus)

The OTel Demo also includes Prometheus. Use composite mode to get per-service metrics in the topology side panel:

```bash
helm upgrade rca-operator ./helm \
  --namespace rca-system \
  --set telemetry.enabled=true \
  --set telemetry.backend=composite \
  --set telemetry.jaeger.endpoint=http://jaeger-query.otel-demo.svc:16686/jaeger/ui \
  --set telemetry.prometheus.endpoint=http://prometheus.otel-demo.svc:9090
```

Or locally:

```bash
# Port-forward Prometheus too
kubectl port-forward -n otel-demo svc/prometheus 9091:9090 &

make run ARGS="--telemetry-backend=composite \
  --jaeger-endpoint=http://localhost:16686/jaeger/ui \
  --prometheus-endpoint=http://localhost:9091"
```

Clicking a node in composite mode shows **Request Rate, Error Rate, P99 Latency, CPU, Memory** in the side panel.

### Step 8: Restore and Verify Resolution

```bash
# Scale cart back up
kubectl scale deployment cart -n otel-demo --replicas=1

# The operator detects the pod becoming healthy and resolves the incident
# Topology refreshes to all-green within 30s–2 minutes
kubectl get incidentreport -n otel-demo
```

---

## Troubleshooting

| Issue | Cause | Fix |
|-------|-------|-----|
| `services "jaeger" not found` | Wrong service name | Use `svc/jaeger-query` not `svc/jaeger` |
| Jaeger API returns HTML (SPA) | Wrong base path | The endpoint must include `/jaeger/ui`: `http://....:16686/jaeger/ui` |
| `jq: parse error: Invalid numeric literal` | `$(date +%s)000` shell expansion issue | Use `python3 -c "import time; print(int(time.time()*1000))"` for milliseconds |
| Empty dependency graph | No trace data yet | Wait 3–5 minutes for load generator to produce traces, re-run curl check |
| "No topology data available" in UI | Telemetry backend not configured | Check operator logs: `kubectl logs -n rca-system deploy/... -f` |
| Nodes all green despite incident | `WithIncidentsFn` misconfigured | Verify `kubectl get incidentreport -A` shows the incident |
| Side panel shows "No metrics available" | Jaeger-only mode has no metrics | Switch to composite mode with Prometheus |
| Topology doesn't update after incident | Cache TTL (30s) | Click the **↻** refresh button in the topology toolbar |

---

## API Verification Commands

```bash
# Verify operator topology endpoint
curl -s http://localhost:9090/api/topology | jq '{nodeCount: (.nodes | length), edgeCount: (.edges | length)}'

# List all discovered service names
curl -s http://localhost:9090/api/topology | jq '.nodes | keys'

# Check blast radius for a specific service
curl -s "http://localhost:9090/api/topology/blast?service=cart" | jq .

# Service health overview
curl -s http://localhost:9090/api/services | jq '.[] | {name, status}'

# Service metrics (requires Prometheus or SigNoz backend)
curl -s http://localhost:9090/api/services/frontend/metrics | jq .

# Recent traces for a service
curl -s "http://localhost:9090/api/services/frontend/traces?limit=5" | jq '.[] | {traceID, hasError}'

# Recent logs (requires SigNoz backend)
curl -s "http://localhost:9090/api/services/cart/logs?severity=ERROR&limit=10" | jq '.[] | .body'
```

---

## OTel Demo Service Names Reference

The OTel Demo uses **short service names** without the `-service` suffix. The operator matches incidents to topology nodes by name prefix.

| Kubernetes Deployment | Jaeger Service Name | Icon Inferred |
|-----------------------|--------------------:|--------------|
| `frontend` | `frontend` | 🖥 monitor |
| `frontend-proxy` | `frontend-proxy` | 🌐 globe |
| `cart` | `cart` | ⚙ server |
| `checkout` | `checkout` | ⚙ server |
| `payment` | `payment` | 💳 credit-card |
| `product-catalog` | `product-catalog` | ⚙ server |
| `recommendation` | `recommendation` | ⚙ server |
| `shipping` | `shipping` | ⚙ server |
| `email` | `email` | ⚙ server |
| `ad` | `ad` | ⚙ server |
| `currency` | `currency` | ⚙ server |
| `quote` | `quote` | ⚙ server |
| `accounting` | `accounting` | ⚙ server |
| `fraud-detection` | `fraud-detection` | ⚙ server |
| `kafka` | `kafka` | 📨 mail |
| `valkey-cart` | `valkey-cart` | 🗄 database |
| `postgresql` | `postgresql` | 🗄 database |
| `load-generator` | `load-generator` | ⚙ server |

---

## Cleanup

```bash
# Stop all port-forwards
pkill -f "kubectl port-forward"

# Remove the OTel Demo
kubectl delete namespace otel-demo

# Remove the RCA Operator
helm uninstall rca-operator -n rca-system
kubectl delete namespace rca-system
```
