# Service Topology Visualization

## How Topology Works

The RCA Operator builds an interactive service dependency graph by querying external observability backends for distributed trace data. It does **not** run the observability stack itself -- it queries existing deployments of SigNoz, Jaeger, or Prometheus.

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
   - Jaeger: `GET /api/dependencies?endTs={now}&lookback={window}`
   - SigNoz: `GET /api/v1/services/dependencies`
   - Returns `[]DependencyEdge{Parent, Child, CallCount, ErrorRate, AvgLatency}`

2. **Build graph skeleton**: Each dependency edge creates two `ServiceNode`s (if not exist) and a `ServiceEdge` between them.

3. **Enrich with metrics**: For each node, call `GetServiceMetrics(ctx, serviceName, window)` to get RED metrics (Request rate, Error rate, Duration) plus CPU/Memory.

4. **Overlay incidents**: Fetch all non-Resolved `IncidentReport` CRs and match them to service nodes by name. This sets node status:
   - **Critical**: Active incident with P1 or P2 severity
   - **Warning**: Active or Detecting phase incident
   - **Healthy**: No incidents

5. **Infer icons**: Service names are matched against known patterns (gateway, postgres, redis, kafka, auth, payment, frontend, etc.) to assign UI icons.

6. **Cache**: The graph is cached in memory with a configurable TTL (default 30s). A background goroutine refreshes it periodically.

### Blast Radius

When you click a service node in the topology view, the operator computes blast radius via BFS:
- **Upstream**: All services that call the affected service (their requests will fail)
- **Downstream**: All services that the affected service calls (may have orphaned connections)
- These are highlighted with red dashed rings in the SVG

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
- **Side panel**: Shows metrics, active incidents, blast radius, and quick actions when a node is selected

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

The [OpenTelemetry Demo](https://opentelemetry.io/docs/demo/) is a microservices application with 10+ services that generates realistic distributed traces. It's ideal for testing the RCA Operator's topology visualization.

### Prerequisites

- A Kubernetes cluster (kind, minikube, or cloud)
- `kubectl` and `helm` configured
- At least 6 GB memory available for the cluster

### Step 1: Deploy the OTel Demo

```bash
# Create a namespace for the demo
kubectl create namespace otel-demo

# Deploy the OTel Demo (includes Jaeger, Prometheus, and 10+ microservices)
kubectl apply -n otel-demo -f https://raw.githubusercontent.com/open-telemetry/opentelemetry-demo/main/kubernetes/opentelemetry-demo.yaml

# Wait for all pods to be ready (takes 3-5 minutes)
kubectl wait --for=condition=ready pod --all -n otel-demo --timeout=300s
```

The demo deploys these services (among others):
- `frontend` - Web UI
- `cartservice` - Shopping cart (Redis-backed)
- `checkoutservice` - Order processing
- `paymentservice` - Payment handling
- `productcatalogservice` - Product listing
- `recommendationservice` - Product recommendations
- `shippingservice` - Shipping quotes
- `emailservice` - Email notifications
- `adservice` - Ad serving
- `currencyservice` - Currency conversion

It also deploys:
- **Jaeger** - Distributed tracing backend at `jaeger:16686`
- **Prometheus** - Metrics at `prometheus:9090`
- **OTel Collector** - Receives OTLP from all services

### Step 2: Verify Jaeger Has Trace Data

```bash
# Port-forward Jaeger UI
kubectl port-forward -n otel-demo svc/jaeger 16686:16686

# Open http://localhost:16686 in your browser
# You should see services in the dropdown and traces between them
```

Check the `/api/dependencies` endpoint that the topology builder uses:

```bash
# Query Jaeger dependencies API
curl -s "http://localhost:16686/api/dependencies?endTs=$(date +%s)000&lookback=900000" | jq .
```

You should see output like:
```json
{
  "data": [
    {"parent": "frontend", "child": "cartservice", "callCount": 42},
    {"parent": "frontend", "child": "productcatalogservice", "callCount": 128},
    {"parent": "checkoutservice", "child": "paymentservice", "callCount": 15},
    ...
  ]
}
```

### Step 3: Deploy RCA Operator with Jaeger Backend

```bash
# Install RCA Operator with Jaeger telemetry
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=jaeger \
  --set telemetry.jaeger.endpoint=http://jaeger.otel-demo.svc:16686

# Verify the operator pod is running
kubectl get pods -n rca-system
```

Or if running locally with `make run`:

```bash
make run ARGS="--telemetry-backend=jaeger --jaeger-endpoint=http://localhost:16686"
```

### Step 4: Access the Topology Dashboard

```bash
# Port-forward the dashboard
kubectl port-forward -n rca-system svc/rca-operator-dashboard 9090:9090

# Open http://localhost:9090 in your browser
# Click the "Topology" tab
```

You should see:
- All OTel Demo services as nodes in the graph
- Edges showing call relationships (e.g., frontend -> cartservice)
- Call counts on edges
- Service icons (globe for frontend, database for redis, etc.)
- Health status coloring (green if no incidents)

### Step 5: Create an Incident and See It on Topology

```bash
# Create a sample RCAAgent to watch the otel-demo namespace
cat <<EOF | kubectl apply -f -
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: otel-demo-agent
  namespace: otel-demo
spec:
  watchNamespaces:
    - otel-demo
  signalCooldown: 5m
  stabilizationWindow: 1m
EOF

# Simulate a failure by scaling down a service
kubectl scale deployment cartservice -n otel-demo --replicas=0

# Wait 1-2 minutes for the operator to detect the failure
# The topology should now show:
# - cartservice node turns RED (critical)
# - frontend node may turn AMBER (warning) due to blast radius
# - Clicking cartservice shows the incident in the side panel
```

### Step 6: Test Blast Radius

1. Click on the `cartservice` node in the topology
2. The side panel shows the active incident
3. The **Blast Radius** section shows affected upstream services (e.g., `frontend`, `checkoutservice`)
4. Affected services are highlighted with red dashed rings on the graph

### Step 7: Test with Composite Mode (Jaeger + Prometheus)

```bash
# If Prometheus is also available, use composite mode
helm upgrade rca-operator ./helm \
  --namespace rca-system \
  --set telemetry.enabled=true \
  --set telemetry.backend=composite \
  --set telemetry.jaeger.endpoint=http://jaeger.otel-demo.svc:16686 \
  --set telemetry.prometheus.endpoint=http://prometheus.otel-demo.svc:9090
```

With composite mode, the topology nodes also show per-service metrics (request rate, error rate, P99 latency, CPU, memory) in the side panel.

### Step 8: Restore and Verify Resolution

```bash
# Scale cartservice back up
kubectl scale deployment cartservice -n otel-demo --replicas=1

# Wait for the operator to resolve the incident
# The topology should return to all-green
```

### Troubleshooting

| Issue | Cause | Fix |
|-------|-------|-----|
| "No topology data available" | Telemetry backend not configured or unreachable | Check `--telemetry-backend` flag and endpoint connectivity |
| Empty graph (no nodes/edges) | Jaeger has no trace data yet | Wait for the OTel Demo to generate traffic (2-3 minutes), or check Jaeger UI |
| Nodes all show "unknown" | Incident overlay callback not working | Verify IncidentReport CRs exist: `kubectl get incidentreport -A` |
| Side panel shows no metrics | Using Jaeger-only mode (no metrics support) | Switch to composite mode or SigNoz for metrics |
| Topology doesn't refresh | Cache TTL not expired | Click the refresh button (circular arrow) in the topology controls |

### API Verification Commands

```bash
# Check topology data directly
curl -s http://localhost:9090/api/topology | jq '.nodes | keys'

# Check blast radius
curl -s "http://localhost:9090/api/topology/blast?service=cartservice" | jq .

# Check services list
curl -s http://localhost:9090/api/services | jq .

# Check service metrics (requires Prometheus or SigNoz)
curl -s http://localhost:9090/api/services/frontend/metrics | jq .

# Check service traces
curl -s http://localhost:9090/api/services/frontend/traces | jq .
```

### Cleanup

```bash
# Remove the OTel Demo
kubectl delete namespace otel-demo

# Remove the RCA Operator
helm uninstall rca-operator -n rca-system
kubectl delete namespace rca-system
```
