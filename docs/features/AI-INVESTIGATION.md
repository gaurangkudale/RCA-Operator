# AI Investigation

RCA Operator can perform AI-powered root cause analysis on active incidents using any OpenAI-compatible LLM API.

## How It Works

1. **Trigger** — An incident transitions to `Active` (with `autoInvestigate: true`) or a user calls `POST /api/investigate/{ns}/{name}`.
2. **Context Assembly** — The investigator assembles:
   - Incident metadata (type, severity, summary, timeline, affected resources)
   - Related Kubernetes events
   - Error traces from the telemetry backend (trace IDs from `status.relatedTraces`)
   - Metric anomalies in the incident time window
   - Error logs correlated by trace ID or service name
3. **PII Redaction** — Before sending to the LLM, all context is scanned for:
   - Email addresses
   - IPv4/IPv6 addresses
   - JWT tokens
   - Database connection strings
   - AWS/GCP/Azure credential patterns
4. **LLM Call** — The context is submitted to the configured chat completions endpoint with tool definitions. The LLM can call tools to fetch additional evidence:
   - `query_metrics(promql, start, end)` — execute a PromQL query
   - `search_logs(service, severity, keyword, window)` — search logs
   - `get_trace(trace_id)` — fetch a full trace with spans
   - `get_dependencies(service)` — fetch topology for a service
5. **Result Storage** — The response is parsed and written to `status.rca` on the `IncidentReport` CR.

## Configuration

### Via RCAAgent CRD

```yaml
spec:
  ai:
    enabled: true
    endpoint: https://api.openai.com/v1
    model: gpt-4o
    secretRef: openai-api-key   # Secret must have key "apiKey"
    autoInvestigate: true        # Trigger on every Active incident
```

### Via Helm

```yaml
ai:
  enabled: true
  endpoint: "https://api.openai.com/v1"
  model: "gpt-4o"
  secretRef: "openai-api-key"
  autoInvestigate: true
```

### Via CLI Flags (make run)

```bash
make run ARGS="--ai-endpoint=https://api.openai.com/v1 --ai-model=gpt-4o --ai-secret-ref=openai-api-key --ai-auto-investigate"
```

## API Key Secret

Create the secret before deploying the agent:

```bash
kubectl create secret generic openai-api-key \
  --from-literal=apiKey=sk-proj-xxx... \
  -n rca-system
```

The secret must be in the same namespace as the `RCAAgent` resource.

## Compatible LLM Providers

The operator uses the standard OpenAI chat completions API (`POST /v1/chat/completions`). Any compatible endpoint works:

| Provider | Endpoint |
|---|---|
| OpenAI | `https://api.openai.com/v1` |
| Azure OpenAI | `https://<instance>.openai.azure.com/openai/deployments/<deployment>` |
| Local Ollama | `http://localhost:11434/v1` |
| LiteLLM Proxy | `http://litellm-proxy:8000/v1` |
| Anthropic (via proxy) | Any OpenAI-compatible proxy |

## Triggering Investigations

### Automatic

Set `autoInvestigate: true` in `spec.ai`. The operator triggers an investigation for every incident that transitions to `Active` phase.

### Manual (Dashboard)

Open an incident in the dashboard and click the **Investigate** button. The result appears in the AI RCA block within seconds.

### Manual (API)

```bash
curl -X POST http://localhost:9090/api/investigate/production/crashloopbackoff-payment-abc123
```

### Manual (kubectl)

Since results are stored in the `IncidentReport` status, you can read them directly:

```bash
kubectl get incidentreport crashloopbackoff-payment-abc123 -n production \
  -o jsonpath='{.status.rca}' | jq .
```

## Result Schema

Results are stored in `status.rca` on the `IncidentReport` CR:

```yaml
status:
  rca:
    rootCause: "Memory leak in payment-svc caused OOMKilled events"
    confidence: "0.92"
    playbook:
      - "kubectl rollout undo deployment/payment-svc -n production"
      - "kubectl scale deployment/payment-svc --replicas=6"
      - "kubectl set resources deployment/payment-svc --limits=memory=512Mi"
    evidence:
      - "Trace abc123: 500ms latency spike on POST /checkout at 10:15:01"
      - "Log: java.lang.OutOfMemoryError: Java heap space at 10:15:01"
      - "Metric: memory_usage_bytes spiked from 200MB to 490MB between 10:14-10:15"
    investigatedAt: "2026-04-01T10:20:00Z"
```

| Field | Description |
|---|---|
| `rootCause` | Plain-language root cause summary |
| `confidence` | LLM confidence score (0.0–1.0 as string) |
| `playbook` | Ordered list of kubectl/runbook commands to remediate |
| `evidence` | Specific telemetry signals used to reach the conclusion |
| `investigatedAt` | Timestamp of the investigation |

## Prerequisites

AI investigation benefits from — but does not require — a telemetry backend. Without a backend:
- The LLM works with Kubernetes-native signals only (events, pod status, deployment conditions)
- `status.relatedTraces` will be empty
- Tool calls for metrics/logs/traces will return empty results

With a backend configured, the LLM receives cross-signal evidence and produces higher-quality analysis.

## Privacy and Security

- PII is stripped before any data is sent to the LLM
- The operator uses read-only tool calls — it never modifies cluster resources during investigation
- API keys are stored in Kubernetes Secrets and accessed in-memory; they are never logged
- AI investigation is disabled by default (`ai.enabled: false`)

## Related

- [RCAAgent CRD reference](../reference/rcaagent-crd.md) — `spec.ai`
- [IncidentReport CRD reference](../reference/incidentreport-crd.md) — `status.rca`
- [Dashboard API](../reference/dashboard-api.md) — `/api/investigate` endpoints
- [SigNoz integration](../integrations/signoz.md) — recommended telemetry backend for AI investigation
