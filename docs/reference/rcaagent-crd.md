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
| `endpoint` | `string` | Yes | — | OTLP gRPC collector address (e.g. `signoz-collector:4317`) |
| `serviceName` | `string` | No | `rca-operator` | `service.name` resource attribute |
| `samplingRate` | `string` | No | `1.0` | Trace sampling ratio |
| `insecure` | `bool` | No | `false` | Disable TLS on the gRPC connection (typical for in-cluster collectors) |

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
- [Architecture](../concepts/Architecture.md)
- [RBAC permissions](rbac.md)
- [Quick Start](../getting-started/quickstart.md)
