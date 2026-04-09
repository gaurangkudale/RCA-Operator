# SigNoz Integration

RCA Operator integrates with SigNoz to query traces, metrics, logs, and service topology.
SigNoz is the recommended backend because it provides all four signal types through a single
endpoint, which gives RCA Operator the richest context for incident investigation.

## What SigNoz Provides

| Signal | Used For |
|--------|----------|
| Traces | Service topology graph (call graph from span data) |
| Metrics | RED metrics in topology side panel (request rate, error rate, latency) |
| Logs | Drill-down context in incident detail view |
| Dependencies | Blast radius calculation when a node fails |

The operator does **not** store or forward any of this data. It queries SigNoz read-only
at topology refresh time and on-demand during AI investigations.

## Prerequisites

- SigNoz v0.27 or later deployed in the cluster
- RCA Operator v0.0.15 or later (Phase 2 telemetry support)
- The SigNoz query service must be reachable from the RCA Operator pod

Verify the query service is accessible:

```bash
kubectl exec -n rca-system deploy/rca-operator-controller-manager -- \
  wget -qO- http://signoz-query-service.platform:8080/api/v1/health
```

Expected response: `{"status":"ok"}` or similar health payload.

## Helm Installation

### Telemetry only (query SigNoz for topology and metrics)

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=signoz \
  --set telemetry.signoz.endpoint=http://signoz-query-service.platform:8080
```

### Combined: query SigNoz + emit operator traces to SigNoz

This is the recommended production configuration. Application traces flow into SigNoz,
the operator queries SigNoz for topology, and the operator emits its own reconcile spans
back to SigNoz through the OTel collector.

```yaml
# helm/values-signoz.yaml
telemetry:
  enabled: true
  backend: signoz
  signoz:
    endpoint: "http://signoz-query-service.platform:8080"

otel:
  enabled: true
  endpoint: "signoz-otel-collector.platform:4317"  # gRPC, no http://
  serviceName: "rca-operator"
  samplingRate: "1.0"
  insecure: true
```

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-signoz.yaml
```

### With AI investigation

```bash
helm install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=signoz \
  --set telemetry.signoz.endpoint=http://signoz-query-service.platform:8080 \
  --set ai.enabled=true \
  --set ai.endpoint=https://api.openai.com/v1 \
  --set ai.model=gpt-4o \
  --set ai.secretRef=openai-api-key \
  --set ai.autoInvestigate=true
```

The AI investigation tool uses SigNoz logs and traces as context when calling the LLM.

## Topology Refresh Configuration

By default the topology graph refreshes every 30 seconds and uses a 15-minute lookback
window for dependency edges:

```bash
helm upgrade rca-operator ./helm \
  --set topology.refreshInterval=60s \
  --set topology.dependencyWindow=30m
```

Or via CLI flags when running locally:

```bash
make run ARGS="--telemetry-backend=signoz \
  --signoz-endpoint=http://localhost:8080 \
  --topology-refresh-interval=60s \
  --topology-dependency-window=30m"
```

## Local Development

Port-forward the SigNoz query service and run the operator outside the cluster:

```bash
# Terminal 1 — forward SigNoz query service
kubectl port-forward -n platform svc/signoz-query-service 8080:8080

# Terminal 2 — run operator locally
make run ARGS="--telemetry-backend=signoz --signoz-endpoint=http://localhost:8080"
```

If OTel self-instrumentation is also needed locally:

```bash
# Terminal 3 — forward the OTel collector gRPC port
kubectl port-forward -n platform svc/signoz-otel-collector 4317:4317

# Then set the environment variable before running
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
make run ARGS="--telemetry-backend=signoz --signoz-endpoint=http://localhost:8080"
```

## Verification

### Check the topology API

```bash
curl -s http://localhost:9090/api/topology | jq '.nodes | length'
```

A non-zero count confirms the operator is successfully fetching topology from SigNoz.

### Check the services list

```bash
curl -s http://localhost:9090/api/services | jq '.[].name'
```

### Dashboard

Open the RCA Operator dashboard at `http://localhost:9090` and navigate to the
**Topology** tab. Services discovered from SigNoz appear as nodes with edges representing
call relationships. Click any node to open the side panel showing:

- Request rate (req/s)
- Error rate (%)
- P99 latency (ms)
- Recent log entries

If the Topology tab shows an empty graph, see the troubleshooting section below.

### Confirm topology data in SigNoz directly

```bash
NOW=$(python3 -c "import time; print(int(time.time() * 1000))")
curl -s "http://localhost:8080/api/v1/dependencies?endTs=${NOW}&lookback=900000" \
  | jq '.data[:5]'
```

A non-empty `data` array means SigNoz has dependency data and the operator should be
able to build a graph.

## What Data Appears in RCA Incidents

When SigNoz is configured, IncidentReport resources gain additional context:

- **Topology nodes**: affected service and its direct upstream/downstream dependencies
  are listed in the incident summary
- **RED metrics**: displayed in the topology side panel for the pod's parent service
- **Log snippets**: error-level logs from SigNoz are included as signals in the incident
  timeline when log correlation is triggered
- **AI context**: the AI investigation endpoint (`POST /api/investigate`) passes recent
  SigNoz logs and trace spans as tool context to the LLM, enabling more accurate root
  cause hypotheses

Trace IDs produced by the operator's OTel instrumentation are written to
`IncidentReport` annotations under `rca.operator/trace-id`. You can paste this ID
directly into the SigNoz trace explorer to jump to the exact reconcile span that created
the incident.

## Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| Topology tab empty, `/api/topology` returns `{"nodes":[]}` | No traces in SigNoz yet, or lookback window too short | Deploy a load generator; increase `topology.dependencyWindow` |
| `connection refused` in operator logs for SigNoz endpoint | Query service not reachable from operator pod | Check `kubectl get svc -n platform signoz-query-service` and network policies |
| Metrics missing in side panel despite SigNoz backend | SigNoz metrics pipeline not enabled | Enable the SigNoz metrics receiver in your SigNoz Helm values |
| 401 Unauthorized from SigNoz API | SigNoz deployed with auth enabled | Pass the API token via `telemetry.signoz.token` (if supported by your version) or disable auth for internal cluster traffic |
| Topology shows services but no edges | Dependency data not available (no inter-service calls recorded) | Generate cross-service traffic; verify span `kind=CLIENT` is being emitted by your apps |
| AI investigation returns generic answers | SigNoz logs not indexed | Ensure the log pipeline is enabled in SigNoz and that apps emit logs to the OTel collector |
| OTel spans not appearing in SigNoz under `rca-operator` | `otel.enabled=false` or wrong collector endpoint | Set `otel.enabled=true` and verify `otel.endpoint` points to the SigNoz OTel collector gRPC port (4317) |

## Reference

- SigNoz documentation: https://signoz.io/docs
- Operator Helm values: `helm/values.yaml` — `telemetry` and `otel` sections
- Topology feature overview: `docs/features/TOPOLOGY.md`
- OTel self-instrumentation: `docs/integrations/opentelemetry.md`
