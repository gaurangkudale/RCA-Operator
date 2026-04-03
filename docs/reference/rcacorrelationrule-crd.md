# RCACorrelationRule CRD Reference

`RCACorrelationRule` is a cluster-scoped CRD that defines declarative correlation rules for the RCA Operator rule engine. Rules are loaded dynamically — no operator restart needed when rules are created, updated, or deleted.

```bash
kubectl get rcacorrelationrules
# or using the short name:
kubectl get rcr
```

## How It Works

1. The operator starts and loads all `RCACorrelationRule` CRDs from the cluster
2. A dedicated controller watches for rule changes and reloads the rule engine automatically
3. When a signal arrives, the engine evaluates rules in priority order (highest first)
4. The first rule whose trigger and conditions match wins — its `fires` section defines the incident properties

## Example: Node Failure Detection

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: node-plus-eviction
spec:
  priority: 500
  trigger:
    eventType: NodeNotReady
  conditions:
    - eventType: PodEvicted
      scope: sameNode
  fires:
    incidentType: NodeNotReady
    severity: P1
    summary: "NodeNotReady with pod evictions on node {{.NodeName}}"
    resource: node
    scope: Cluster
```

## Example: OOM Detection

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: crashloop-plus-oom
spec:
  priority: 400
  trigger:
    eventType: CrashLoopBackOff
  conditions:
    - eventType: OOMKilled
      scope: samePod
  fires:
    incidentType: OOMKilled
    severity: P2
    summary: "OOMKilled: CrashLoopBackOff with OOMKilled on pod {{.PodName}} in {{.Namespace}}"
```

## Example: Negated Condition

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: imagepull-no-history
spec:
  priority: 200
  trigger:
    eventType: ImagePullBackOff
  conditions:
    - eventType: PodHealthy
      scope: samePod
      negate: true   # fires only if PodHealthy is NOT in the buffer
  fires:
    incidentType: ImagePullBackOff
    severity: P2
    summary: "ImagePullBackOff: no prior healthy state for pod {{.PodName}} in {{.Namespace}}"
```

## Full Field Reference

### spec.priority

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `priority` | `int` | Yes | `100` | Evaluation order. Higher values are evaluated first. First match wins. |

### spec.agentSelector

| Field | Type | Required | Description |
|---|---|---|---|
| `agentSelector` | `LabelSelector` | No | Restricts which RCAAgents this rule applies to. Nil matches all agents. |

### spec.trigger

| Field | Type | Required | Description |
|---|---|---|---|
| `trigger.eventType` | `string` | Yes | Watcher event type that starts evaluation |

Available event types: `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, `PodPendingTooLong`, `GracePeriodViolation`, `NodeNotReady`, `PodEvicted`, `ProbeFailure`, `StalledRollout`, `NodePressure`, `PodHealthy`, `PodDeleted`.

### spec.conditions

All conditions must match for the rule to fire (AND logic).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `eventType` | `string` | Yes | — | Signal type that must be present in the buffer |
| `scope` | `string` | Yes | `samePod` | Relationship to trigger: `samePod`, `sameNode`, `sameNamespace`, `any` |
| `negate` | `bool` | No | `false` | When true, fires only if this signal is NOT present |

### spec.fires

| Field | Type | Required | Description |
|---|---|---|---|
| `incidentType` | `string` | Yes | Canonical incident category |
| `severity` | `string` | Yes | `P1`, `P2`, `P3`, or `P4` |
| `summary` | `string` | Yes | Go `text/template` rendered with event context |
| `resource` | `string` | No | Override dedup resource: `node` for node-scoped, `deployment` for deployment-scoped |
| `scope` | `string` | No | Override incident scope: `Pod`, `Workload`, `Namespace`, `Cluster` |

### Summary Template Variables

The `summary` field is a Go `text/template` with these variables:

| Variable | Description |
|---|---|
| `{{.PodName}}` | Pod name from the trigger event |
| `{{.Namespace}}` | Namespace from the trigger event |
| `{{.NodeName}}` | Node name from the trigger event |
| `{{.EventType}}` | Event type string (e.g. `CrashLoopBackOff`) |

## Default Rules

The Helm chart ships 4 default rules (enabled via `defaultRules.enabled: true`):

| Name | Priority | Trigger | Condition | Fires | Severity |
|---|---|---|---|---|---|
| `node-plus-eviction` | 500 | NodeNotReady | PodEvicted (sameNode) | NodeNotReady | P1 |
| `crashloop-plus-oom` | 400 | CrashLoopBackOff | OOMKilled (samePod) | OOMKilled | P2 |
| `crashloop-plus-deploy` | 300 | CrashLoopBackOff | StalledRollout (sameNamespace) | StalledRollout | P2 |
| `imagepull-no-history` | 200 | ImagePullBackOff | !PodHealthy (samePod) | ImagePullBackOff | P2 |

## Print Columns

`kubectl get rcacorrelationrules` shows:

| Column | Description |
|---|---|
| Priority | Evaluation priority |
| Trigger | Trigger event type |
| Fires | Incident type produced |
| Severity | Incident severity |
| Age | Resource age |

## kubectl Cheatsheet

```bash
# List all rules
kubectl get rcacorrelationrules
kubectl get rcr

# Describe a rule
kubectl describe rcacorrelationrule node-plus-eviction

# Apply a custom rule
kubectl apply -f my-rule.yaml

# Delete a rule (engine reloads automatically)
kubectl delete rcacorrelationrule my-rule

# Apply default rules from config
kubectl apply -f config/rules/
```

## Auto-Generated Rules

The operator can automatically create `RCACorrelationRule` CRDs from observed signal patterns when auto-detection is enabled (`--enable-autodetect`). Auto-generated rules:

- Use priority 10-50 (below user-created rules at 100+)
- Are labeled `rca.rca-operator.tech/auto-generated: "true"`
- Carry confidence, occurrence, and timestamp annotations
- Are automatically expired and deleted if the pattern is not observed within the configured expiry window

See [Auto-Detection](../features/auto-detection.md) for full documentation.

## Related

- [RCAAgent CRD reference](rcaagent-crd.md)
- [IncidentReport CRD reference](incidentreport-crd.md)
- [Auto-Detection](../features/auto-detection.md)
- [RBAC permissions](rbac.md)
