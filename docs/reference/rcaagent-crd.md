# RCAAgent CRD Reference

`RCAAgent` is the primary configuration resource for the operator. One agent can watch one or more namespaces, validate notification secrets, start signal collection for that scope, and apply incident retention policy.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

## Minimal Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
  incidentRetention: 30d
```

## Example With Notifications

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
    - staging
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
    pagerduty:
      secretRef: pagerduty-key
      severity: P2
  incidentRetention: 30d
```

## Full Phase 2 Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
  incidentRetention: 30d
  otel:
    endpoint: signoz-otel-collector.platform:4317
    serviceName: rca-operator
    samplingRate: "1.0"
    insecure: true
  telemetry:
    backend: signoz
    signoz:
      endpoint: http://signoz-query-service.platform:8080
  ai:
    enabled: true
    endpoint: https://api.openai.com/v1
    model: gpt-4o
    secretRef: openai-api-key
    autoInvestigate: true
```

## Full Field Reference

### spec.watchNamespaces

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `watchNamespaces` | `[]string` | Yes | `["default"]` | Namespaces the operator monitors for Kubernetes-native incident signals |

If a namespace does not exist at reconcile time the operator logs a warning and continues. The agent becomes fully active once those namespaces exist.

### spec.notifications

Optional. Remove the whole block if you do not want outbound alerts.

#### spec.notifications.slack

| Field | Type | Required | Description |
|---|---|---|---|
| `webhookSecretRef` | `string` | Yes | Name of a Secret with key `webhookURL` |
| `channel` | `string` | Yes | Slack channel, for example `#incidents` |
| `mentionOnP1` | `string` | No | Slack user or group to mention on P1 incidents |

#### spec.notifications.pagerduty

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `secretRef` | `string` | Yes | — | Name of a Secret with key `apiKey` |
| `severity` | `string` | No | `P2` | Minimum severity to page. One of `P1`, `P2`, `P3`, `P4` |

The controller validates any referenced notification secrets in the same namespace as the `RCAAgent`.

### spec.incidentRetention

| Field | Type | Required | Default | Pattern |
|---|---|---|---|---|
| `incidentRetention` | `string` | No | `30d` | `^[1-9][0-9]*(m\|h\|d)$` |

How long to keep `Resolved` `IncidentReport` resources before the operator prunes them.

Examples: `5m`, `12h`, `30d`

### spec.incidentRetentionDays

Deprecated compatibility field retained for older manifests. Prefer `incidentRetention`.

### spec.otel

Optional OpenTelemetry configuration for exporting traces and metrics.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `endpoint` | `string` | Yes | — | OTLP gRPC collector address (e.g. `signoz-collector:4317`). No `http://` prefix. |
| `serviceName` | `string` | No | `rca-operator` | `service.name` resource attribute |
| `samplingRate` | `string` | No | `1.0` | Trace sampling ratio |
| `insecure` | `bool` | No | `false` | Disable TLS on the gRPC connection (typical for in-cluster collectors) |

The operator exports both traces **and** metrics via this endpoint. Metrics use a 30-second periodic reader.

### spec.telemetry

Optional. Configures connections to external observability backends for cross-signal correlation (traces, metrics, logs, topology).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `backend` | `string` | No | `composite` | Backend type. One of `signoz`, `jaeger`, `composite`, `full-composite`. |
| `signoz` | `SigNozConfig` | No | — | SigNoz query service connection. Required when backend is `signoz` or `full-composite`. |
| `jaeger` | `JaegerConfig` | No | — | Jaeger query API connection. Required when backend is `jaeger`, `composite`, or `full-composite`. |
| `prometheus` | `PrometheusConfig` | No | — | Prometheus HTTP API connection. Required when backend is `composite` or `full-composite`. |

**Backend capability matrix:**

| Backend | Traces | Metrics | Logs | Topology |
|---|---|---|---|---|
| `signoz` | Yes | Yes | Yes | Yes |
| `jaeger` | Yes | No | No | Yes |
| `composite` | Yes (Jaeger) | Yes (Prometheus) | No | Yes (Jaeger) |
| `full-composite` | Yes (Jaeger) | Yes (Prometheus) | Yes (SigNoz) | Yes (Jaeger) |

#### spec.telemetry.signoz

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | `string` | Yes | SigNoz query service URL (e.g. `http://signoz-query-service:8080`) |

#### spec.telemetry.jaeger

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | `string` | Yes | Jaeger query HTTP API URL (e.g. `http://jaeger-query:16686`). Supports base path for OTel Demo: `http://jaeger-query:16686/jaeger/ui` |
| `grpcEndpoint` | `string` | No | Jaeger gRPC query endpoint (e.g. `jaeger-query:16685`) |

#### spec.telemetry.prometheus

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | `string` | Yes | Prometheus HTTP API URL (e.g. `http://prometheus:9090`) |

### spec.ai

Optional. Configures AI/LLM-driven root cause analysis. When enabled, the operator calls an OpenAI-compatible chat completions API to generate remediation playbooks and root cause summaries.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `enabled` | `bool` | No | `false` | Enable AI-driven investigation |
| `endpoint` | `string` | No | — | OpenAI-compatible API URL (e.g. `https://api.openai.com/v1`). Works with OpenAI, Azure OpenAI, local Ollama, LiteLLM proxy. |
| `model` | `string` | No | `gpt-4o` | LLM model name (e.g. `gpt-4o`, `llama3`, `mistral`) |
| `secretRef` | `string` | No | — | Name of a Kubernetes Secret with key `apiKey` containing the API key |
| `autoInvestigate` | `bool` | No | `false` | Automatically trigger investigation when an incident transitions to `Active` |

When `autoInvestigate` is false, investigations are triggered manually via `POST /api/investigate/{ns}/{name}`.

### spec.signalMappings

Optional overrides for the default event-type to incident-type mapping.

| Field | Type | Required | Description |
|---|---|---|---|
| `eventType` | `string` | Yes | Watcher event type to override (e.g. `CrashLoopBackOff`) |
| `incidentType` | `string` | Yes | Override incident type |
| `severity` | `string` | No | Override severity (`P1`, `P2`, `P3`, `P4`) |
| `scope` | `string` | No | Override scope level (`Pod`, `Workload`, `Namespace`, `Cluster`) |

## Status Conditions

The operator sets standard Kubernetes conditions on `status.conditions`:

| Type | Meaning |
|---|---|
| `Available` | `True` when the agent is configured and collection is running |
| `Degraded` | `True` when a referenced secret is missing or another validation error blocks operation |
| `Progressing` | Reserved for future controller-managed transitions |

```bash
kubectl get rcaagent sre-agent -n default -o jsonpath='{.status.conditions}' | jq .
```

## kubectl Cheatsheet

```bash
# List all agents
kubectl get rcaagent -A

# Describe a specific agent
kubectl describe rcaagent sre-agent -n default

# Edit live
kubectl edit rcaagent sre-agent -n default

# Delete and stop collection for that agent
kubectl delete rcaagent sre-agent -n default
```

## Related

- [IncidentReport CRD reference](incidentreport-crd.md)
- [RCACorrelationRule CRD reference](rcacorrelationrule-crd.md)
- [Dashboard API reference](dashboard-api.md)
- [CLI Flags reference](cli-flags.md)
- [Helm Values reference](helm-values.md)
- [Architecture](../concepts/Architecture.md)
- [RBAC permissions](rbac.md)
- [Quick Start](../getting-started/quickstart.md)
