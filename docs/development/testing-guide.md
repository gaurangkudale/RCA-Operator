# RCA Operator — Local Testing Guide

> **Goal**: Test the three end-to-end paths that are hard to cover in unit tests:
> 1. Topology UI (Workload + Services modes, plus the inline trace detail modal)
> 2. Jaeger trace enrichment (real Jaeger instance + traces)
> 3. OTel ingest pipeline (real OTLP collector + span injection)

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.22+ | `brew install go` |
| Kind | latest | `brew install kind` |
| kubectl | latest | `brew install kubectl` |
| Helm | 3.x | `brew install helm` |
| Docker | latest | [docker.com/get-started](https://www.docker.com/get-started/) |
| curl | built-in | macOS built-in |

Check everything is installed:
```bash
go version && kind version && kubectl version --client && helm version && docker info | grep "Server Version"
```

---

## Step 1 — Bootstrap the Cluster

```bash
# 1. Create a local Kind cluster
kind create cluster --name rca-dev

# 2. Verify cluster is up
kubectl cluster-info --context kind-rca-dev

# 3. Set context
kubectl config use-context kind-rca-dev
```

---

## Step 2 — Install CRDs & Run Operator Locally

Running locally (no Docker build needed) is the fastest iteration loop.

```bash
# 1. From the repo root
cd /Users/gaurangkudale/gk-github/RCA-Operator

# 2. Install CRDs into cluster
make install

# 3. Apply default correlation rules
kubectl apply -f config/rules/

# 4. Apply sample RCAAgents
kubectl apply -f config/samples/rca_v1alpha1_rcaagent.yaml

# Verify agents exist
kubectl get rcaagent -A
```

---

## Step 3 — Install Jaeger (for topology enrichment)

The Helm chart bundles Jaeger. We'll deploy it to the cluster.

```bash
# 1. Add the Jaeger Helm repo
helm repo add jaegertracing https://jaegertracing.github.io/helm-charts
helm repo update

# 2. Create namespace
kubectl create namespace rca-system

# 3. Install Jaeger (in-memory storage, single pod)
helm install jaeger jaegertracing/jaeger \
  --namespace rca-system \
  --set allInOne.enabled=true \
  --set collector.replicaCount=0 \
  --set query.replicaCount=0 \
  --set agent.enabled=false \
  --set storage.type=memory \
  --set allInOne.image.tag="1.58.0"

# 4. Wait for Jaeger to be ready
kubectl wait --for=condition=available deployment/jaeger-all-in-one \
  -n rca-system --timeout=120s

# 5. Verify Jaeger is up
kubectl get pods -n rca-system
# Expected: jaeger-all-in-one-xxxx   1/1   Running
```

### Port-forward Jaeger services
Open **two separate terminals** and keep them running:

```bash
# Terminal A — Jaeger Query API (used by RCA operator graph builder)
kubectl port-forward -n rca-system svc/jaeger-all-in-one 16686:16686

# Terminal B — Jaeger OTLP HTTP (used by your test apps to send spans)
kubectl port-forward -n rca-system svc/jaeger-all-in-one 4318:4318
```

Verify Jaeger UI is accessible:
```bash
open http://localhost:16686
# Should open Jaeger UI in browser
```

---

## Step 4 — Run Operator with Jaeger URL + OTel Ingest Enabled

Now run the operator with all the flags needed for full testing:

```bash
# From repo root — run operator locally with:
# - Jaeger query URL (for graph builder)
# - OTel ingest server (for span injection)
# - Leader election disabled (simpler local dev)

make run ARGS="--leader-elect=false \
  --jaeger-query-url=http://localhost:16686 \
  --otel-ingest-bind-address=:4319 \
  --otel-ingest-filter-error-status=true \
  --otel-ingest-filter-http-status-gte=500 \
  --otel-ingest-filter-latency-ms=2000 \
  --otel-ingest-filter-min-log-severity=WARN \
  --dashboard-bind-address=:9090"
```

Expected startup output:
```
INFO  Starting manager
INFO  OTLP ingest server registered  bindAddress=:4319 ...
INFO  Jaeger query enrichment enabled  url=http://localhost:16686
INFO  Starting dashboard server  addr=:9090
INFO  Starting workers ...
```

Keep this running in its own terminal.

---

## Step 5 — Test A: Topology UI (No OTel Required)

This tests the basic topology panel using only K8s signals — no Jaeger or OTel needed.

### 5.1 Create a CrashLoop incident

```bash
# In a new terminal
kubectl apply -f test/fixtures/pods/crashloop.yaml

# Watch the incident appear (takes ~30 seconds after pod starts crashing)
kubectl get incidentreports -n default -w
```

Expected output after ~30s:
```
NAME                        PHASE       SEVERITY   TYPE          AGE
crashloop-demo-xxxx         Detecting   P3         CrashLoop     10s
crashloop-demo-xxxx         Active      P2         OOM           28s
```

### 5.2 Check graph was built in the CR

```bash
# Get the incident name
INC=$(kubectl get incidentreports -n default -o name | head -1 | cut -d/ -f2)
echo "Incident: $INC"

# Check if status.incidentGraph is set (non-empty = graph was built)
kubectl get incidentreport $INC -n default \
  -o jsonpath='{.status.incidentGraph.raw}' | base64 --decode | python3 -m json.tool
```

Expected output (JSON):
```json
{
  "nodes": [
    {"id": "incident:default/crashloop-demo-xxxx", "kind": "Incident", "name": "crashloop-demo-xxxx"},
    {"id": "pod:default/crashloop-demo", "kind": "Pod", "name": "crashloop-demo"}
  ],
  "edges": [
    {"from": "incident:default/crashloop-demo-xxxx", "to": "pod:default/crashloop-demo", "kind": "affects"}
  ],
  "truncated": false
}
```

### 5.3 Verify graph API endpoint

```bash
# Replace with your actual incident name
curl -s http://localhost:9090/api/incidents/default/$INC/graph | python3 -m json.tool
# Expected: JSON with nodes + edges
# If graph not yet built: HTTP 204 (No Content)
```

### 5.4 View Topology UI in browser

```bash
open http://localhost:9090
```

1. Open the **Incidents** tab and click an incident — its blast-radius graph
   loads in the right pane.
2. Open the **Topology** tab. It has two modes:
   - **Workload** — Deployments / Pods / Nodes laid out as a hierarchical DAG.
   - **Services** — Jaeger-style service dependency graph; edges carry call counts.
3. Click any node — the side panel shows resource status, open incidents,
   recent events, and (for services) inbound/outbound/peers traffic plus
   observed trace IDs.
4. Click any trace ID anywhere in the UI — the inline trace detail modal
   opens with summary tiles, the span waterfall, errors, and per-service
   breakdown. Click a pod chip on a span to deep-link into the Logs tab
   prefilled with that namespace + pod.
5. Drag a node to reposition it (positions are remembered for the session;
   layout is static — no physics simulation).

**Topology UI is working ✅** if you can navigate between modes, open node
panels, and open a trace modal that renders a waterfall.

---

## Step 6 — Test B: OTel Ingest Pipeline

This tests that OTel spans sent to the operator create incidents.

### 6.1 Send a test span with error status

The operator listens on `:4319` for OTLP/HTTP. Let's send a span with error status:

```bash
# Send a span with STATUS_CODE_ERROR to the operator ingest endpoint
curl -X POST http://localhost:4319/v1/traces \
  -H "Content-Type: application/json" \
  -d '{
    "resourceSpans": [{
      "resource": {
        "attributes": [
          {"key": "service.name", "value": {"stringValue": "payment-service"}},
          {"key": "k8s.pod.name", "value": {"stringValue": "payment-service-abc123"}},
          {"key": "k8s.namespace.name", "value": {"stringValue": "default"}}
        ]
      },
      "scopeSpans": [{
        "spans": [{
          "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
          "spanId": "00f067aa0ba902b7",
          "name": "POST /checkout",
          "startTimeUnixNano": "1713360000000000000",
          "endTimeUnixNano": "1713360001000000000",
          "status": {
            "code": 2,
            "message": "Payment processor timeout"
          },
          "attributes": [
            {"key": "http.method", "value": {"stringValue": "POST"}},
            {"key": "http.status_code", "value": {"intValue": "503"}},
            {"key": "http.url", "value": {"stringValue": "https://api.internal/checkout"}}
          ]
        }]
      }]
    }]
  }'
```

Expected response: `HTTP 200` (empty body)

### 6.2 Verify the signal was ingested

In the operator terminal you should see:
```
INFO  OTelSpanError signal received  service=payment-service traceID=4bf92f3577b34da6a3ce929d0e0e4736
```

### 6.3 Check for incident creation

```bash
# Watch for new incidents
kubectl get incidentreports -n default -w

# Or list all
kubectl get incidentreports -A
```

Expected: A new IncidentReport should appear for the payment-service pod.

### 6.4 Verify TraceID annotation

```bash
INC=$(kubectl get incidentreports -n default -o name | grep payment | head -1 | cut -d/ -f2)

# Check trace-id annotation
kubectl get incidentreport $INC -n default \
  -o jsonpath='{.metadata.annotations.rca\.rca-operator\.tech/trace-id}'
# Expected: 4bf92f3577b34da6a3ce929d0e0e4736

# Check fired-rule annotation
kubectl get incidentreport $INC -n default \
  -o jsonpath='{.metadata.annotations.rca\.rca-operator\.tech/fired-rule}'
# Expected: Rule name (e.g. "OTelSpanError" or custom rule name)
```

### 6.5 Verify Trace & Rule in Dashboard

```bash
open http://localhost:9090
```

1. Find the payment-service incident in the **Incidents** tab.
2. Click it → detail panel opens on the right.
3. The header chips show the fired rule (e.g. `rule: auto-otellogmatch-otelspanerror-samepod` or `OTelSpanError` for a single-signal incident).
4. The **Trace IDs** section lists the trace IDs collected on the incident; click one to open the inline trace detail modal.

**OTel ingest is working ✅** if the incident was created with at least one trace ID and the modal renders the span waterfall.

### 6.6 Send a latency spike span (bonus test)

```bash
# Span with duration = 8000ms (exceeds 2000ms threshold we set)
curl -X POST http://localhost:4319/v1/traces \
  -H "Content-Type: application/json" \
  -d '{
    "resourceSpans": [{
      "resource": {
        "attributes": [
          {"key": "service.name", "value": {"stringValue": "checkout-service"}},
          {"key": "k8s.namespace.name", "value": {"stringValue": "default"}}
        ]
      },
      "scopeSpans": [{
        "spans": [{
          "traceId": "5cf92f3577b34da6a3ce929d0e0e1234",
          "spanId": "11f067aa0ba902c8",
          "name": "GET /cart",
          "startTimeUnixNano": "1713360000000000000",
          "endTimeUnixNano": "1713360008000000000",
          "status": {"code": 1}
        }]
      }]
    }]
  }'
```

Expected: New incident of type `OTelLatencySpike` (if correlator rules match).

---

## Step 7 — Test C: Jaeger Trace Enrichment (Topology with Service Calls)

This tests that the graph builder fetches traces from Jaeger and adds service-to-service edges.

### 7.1 Push a trace to Jaeger (simulate an instrumented app)

```bash
# Send a multi-span trace to Jaeger's OTLP HTTP endpoint (port 4318)
# This simulates: frontend -> payment-service -> database

TRACE_ID="7bf92f3577b34da6a3ce929d0e0e9999"

curl -X POST http://localhost:4318/v1/traces \
  -H "Content-Type: application/json" \
  -d "{
    \"resourceSpans\": [
      {
        \"resource\": {
          \"attributes\": [
            {\"key\": \"service.name\", \"value\": {\"stringValue\": \"frontend\"}}
          ]
        },
        \"scopeSpans\": [{
          \"spans\": [{
            \"traceId\": \"${TRACE_ID}\",
            \"spanId\": \"aaaa000000000001\",
            \"name\": \"POST /order\",
            \"startTimeUnixNano\": \"1713360000000000000\",
            \"endTimeUnixNano\": \"1713360006000000000\",
            \"status\": {\"code\": 2, \"message\": \"Order failed\"}
          }]
        }]
      },
      {
        \"resource\": {
          \"attributes\": [
            {\"key\": \"service.name\", \"value\": {\"stringValue\": \"payment-service\"}},
            {\"key\": \"k8s.pod.name\", \"value\": {\"stringValue\": \"payment-svc-xyz\"}},
            {\"key\": \"k8s.namespace.name\", \"value\": {\"stringValue\": \"default\"}}
          ]
        },
        \"scopeSpans\": [{
          \"spans\": [{
            \"traceId\": \"${TRACE_ID}\",
            \"spanId\": \"bbbb000000000002\",
            \"parentSpanId\": \"aaaa000000000001\",
            \"name\": \"ProcessPayment\",
            \"startTimeUnixNano\": \"1713360001000000000\",
            \"endTimeUnixNano\": \"1713360005000000000\",
            \"status\": {\"code\": 2, \"message\": \"Payment timeout\"},
            \"references\": [{
              \"refType\": \"CHILD_OF\",
              \"traceID\": \"${TRACE_ID}\",
              \"spanID\": \"aaaa000000000001\"
            }]
          }]
        }]
      }
    ]
  }"
```

### 7.2 Verify trace is in Jaeger

```bash
# Check via Jaeger Query API
curl -s "http://localhost:16686/api/traces/${TRACE_ID}" | python3 -m json.tool | head -30
# Expected: JSON with 2 spans (frontend, payment-service)
```

Also verify in Jaeger UI:
```bash
open "http://localhost:16686/trace/${TRACE_ID}"
# Should show a trace with 2 spans and parent-child relationship
```

### 7.3 Send the same trace error to the RCA operator

Now push the same span (with error) to the operator's ingest so it creates an incident with the trace-id:

```bash
TRACE_ID="7bf92f3577b34da6a3ce929d0e0e9999"

curl -X POST http://localhost:4319/v1/traces \
  -H "Content-Type: application/json" \
  -d "{
    \"resourceSpans\": [{
      \"resource\": {
        \"attributes\": [
          {\"key\": \"service.name\", \"value\": {\"stringValue\": \"payment-service\"}},
          {\"key\": \"k8s.pod.name\", \"value\": {\"stringValue\": \"payment-svc-xyz\"}},
          {\"key\": \"k8s.namespace.name\", \"value\": {\"stringValue\": \"default\"}}
        ]
      },
      \"scopeSpans\": [{
        \"spans\": [{
          \"traceId\": \"${TRACE_ID}\",
          \"spanId\": \"bbbb000000000002\",
          \"name\": \"ProcessPayment\",
          \"startTimeUnixNano\": \"1713360001000000000\",
          \"endTimeUnixNano\": \"1713360005000000000\",
          \"status\": {\"code\": 2, \"message\": \"Payment timeout\"},
          \"attributes\": [
            {\"key\": \"http.status_code\", \"value\": {\"intValue\": \"503\"}}
          ]
        }]
      }]
    }]
  }"
```

### 7.4 Wait for incident to go Active and check graph

```bash
# Wait for incident (up to 60s)
kubectl get incidentreports -n default -w

# Get incident name
INC=$(kubectl get incidentreports -n default -o name | head -1 | cut -d/ -f2)

# Check graph (once incident is Active, graph is built)
curl -s http://localhost:9090/api/incidents/default/$INC/graph | python3 -m json.tool
```

Expected graph with Jaeger enrichment:
```json
{
  "nodes": [
    {"id": "incident:default/...", "kind": "Incident"},
    {"id": "pod:default/payment-svc-xyz", "kind": "Pod"},
    {"id": "svc:frontend", "kind": "Service"},
    {"id": "svc:payment-service", "kind": "Service"}
  ],
  "edges": [
    {"from": "incident:...", "to": "pod:...", "kind": "affects"},
    {"from": "svc:frontend", "to": "svc:payment-service", "kind": "calls"}
  ]
}
```

The `calls` edge from `frontend → payment-service` comes from Jaeger span references.

### 7.5 View the enriched topology in dashboard

```bash
open http://localhost:9090
```

1. Click the payment-service incident
2. Scroll to **"Incident Topology"** section
3. Expected to see:
   - **Red star**: Incident root node
   - **Blue dot**: Pod (payment-svc-xyz)
   - **Purple dots**: Service nodes (frontend, payment-service)
   - **Arrows**: affects edge (Incident → Pod), calls edge (frontend → payment-service)
   - Status: `4 nodes · 3 edges`

**Jaeger enrichment is working ✅** if you see service nodes and `calls` edges.

---

## Step 8 — Cleanup

```bash
# Delete test pods
kubectl delete pod crashloop-demo -n default --ignore-not-found
kubectl delete pod payment-service-abc123 -n default --ignore-not-found

# Delete incidents
kubectl delete incidentreports --all -n default

# Delete agents
kubectl delete -f config/samples/rca_v1alpha1_rcaagent.yaml

# Stop the operator (Ctrl+C in operator terminal)

# Stop port-forwards (Ctrl+C in each terminal)

# Delete the Kind cluster
kind delete cluster --name rca-dev
```

---

## Troubleshooting

### Topology panel shows "No topology graph available"
- Incident may still be in **Detecting** phase — wait until it transitions to **Active**
- Graph is built on the Active transition; check operator logs for `buildIncidentGraph`
- Verify: `kubectl get incidentreport $INC -o jsonpath='{.status.phase}'`

### Topology canvas is blank or nodes have no labels
- Check browser console (`F12 → Console`) for JavaScript errors.
- The dashboard loads Tailwind, Lucide, and Inter from `cdn.tailwindcss.com` /
  `unpkg.com` / `fonts.googleapis.com`. If your environment blocks those CDNs,
  the layout will render with bare HTML — verify network access:
  `curl -I https://unpkg.com/lucide@latest`.
- If only labels are missing, `lucide.createIcons()` is failing — usually a
  follow-on of the above CDN check.

### OTel span sends 200 but no incident created
- Check operator logs for ingest errors
- Verify the span has error status code (`"code": 2` = STATUS_CODE_ERROR)
- Check filters: `--otel-ingest-filter-error-status=true` must be set
- The pod name in span attributes must match a pod in a watched namespace

### Jaeger graph has no `calls` edges
- Verify traces exist in Jaeger: `curl http://localhost:16686/api/traces/{traceId}`
- Check spans have `references` with `CHILD_OF` relationship
- Operator logs show: `INFO Fetching trace from Jaeger traceID=...`
- If Jaeger returns 404: trace wasn't pushed OR trace-id in operator != trace-id in Jaeger

### Graph API returns 204 (No Content)
- Graph hasn't been built yet (incident not Active) OR was pruned
- Check `incidentRetention` on RCAAgent — if it's very short (e.g., `5m`), graphs may prune after `5m/4 = 75s`
- For testing, set: `incidentRetention: 24h`

### make run fails with "no such file or directory"
```bash
# Regenerate manifests and binaries
make manifests generate
make install
```

---

## Quick Reference

```bash
# Run operator with all features
make run ARGS="--leader-elect=false --jaeger-query-url=http://localhost:16686 --otel-ingest-bind-address=:4319 --otel-ingest-filter-latency-ms=2000 --dashboard-bind-address=:9090"

# Port-forwards (run in separate terminals)
kubectl port-forward -n rca-system svc/jaeger-all-in-one 16686:16686
kubectl port-forward -n rca-system svc/jaeger-all-in-one 4318:4318

# Dashboard
open http://localhost:9090

# Jaeger UI
open http://localhost:16686

# Trigger crashloop
kubectl apply -f test/fixtures/pods/crashloop.yaml

# Watch incidents
kubectl get incidentreports -n default -w

# Check graph
INC=$(kubectl get incidentreports -n default -o name | head -1 | cut -d/ -f2)
curl -s http://localhost:9090/api/incidents/default/$INC/graph | python3 -m json.tool
```
