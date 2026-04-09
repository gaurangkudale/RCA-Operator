# Correlation Rules

The RCA Operator uses a rule engine to detect multi-signal incidents — situations where multiple independent signals arriving close together indicate a common root cause. Rules are defined as `RCACorrelationRule` cluster-scoped CRDs and loaded dynamically (no operator redeploy needed).

## How It Works

1. Every signal received by the operator is added to a sliding-window buffer (default: 5-minute window).
2. When a new signal arrives, all loaded `RCACorrelationRule` CRs are evaluated in **priority order** (higher priority first).
3. The first rule whose trigger + all conditions match **wins** — its `fires.*` fields override the incident type, severity, and summary.
4. If no rule fires, the incident is created using the default signal mapping (see [Signal Pipeline](SIGNALS.md)).

## Rule Anatomy

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: crashloop-oom-correlation
spec:
  priority: 200               # Higher = evaluated first
  agentSelector:              # Optional: restrict to specific agents
    matchLabels:
      env: production
  trigger:
    eventType: CrashLoopBackOff   # Signal that triggers evaluation
  conditions:
    - eventType: OOMKilled        # Additional signal that must be present
      scope: samePod              # On the same pod as the trigger
  fires:
    incidentType: OOMCorrelation  # Incident type to create
    severity: P1                  # Override severity
    summary: "OOM + CrashLoop on {{.PodName}} in {{.Namespace}}"
    scope: Pod
```

## Field Reference

### spec.priority

Integer controlling evaluation order. Rules with higher priority numbers are evaluated first. When two rules have the same priority, they are sorted alphabetically by name.

**Default:** `100`

### spec.agentSelector

Optional `LabelSelector` that restricts which `RCAAgent` instances this rule applies to. A nil selector matches all agents.

```yaml
agentSelector:
  matchLabels:
    env: production
    team: payments
```

### spec.trigger

The event type that initiates rule evaluation. Must be one of the [14 standard event types](SIGNALS.md#default-signal-mappings) or a custom type used by a signal mapping override.

```yaml
trigger:
  eventType: CrashLoopBackOff
```

### spec.conditions

Additional signals that must be present in the sliding-window buffer for the rule to fire. All conditions use AND logic — every condition must match.

| Field | Type | Default | Description |
|---|---|---|---|
| `eventType` | string | — | Signal type that must be present |
| `scope` | string | `samePod` | Relationship to trigger: `samePod`, `sameNode`, `sameNamespace`, `any` |
| `negate` | bool | `false` | When `true`, the rule fires only if this signal is **absent** |

**Scope values:**

| Scope | Meaning |
|---|---|
| `samePod` | Condition signal must be from the same pod as the trigger |
| `sameNode` | Condition signal must be from the same node as the trigger |
| `sameNamespace` | Condition signal must be from the same namespace as the trigger |
| `any` | Condition signal can be from any pod/node in the buffer |

### spec.fires

Defines the output when the rule matches.

| Field | Type | Required | Description |
|---|---|---|---|
| `incidentType` | string | Yes | Incident type to create (e.g. `OOMCorrelation`, `NodeFailure`) |
| `severity` | string | Yes | Severity: `P1`, `P2`, `P3`, `P4` |
| `summary` | string | Yes | Go template. Variables: `{{.PodName}}`, `{{.Namespace}}`, `{{.NodeName}}`, `{{.EventType}}` |
| `resource` | string | No | Resource scope override: `node` for node-scoped, `deployment` for workload-scoped |
| `scope` | string | No | Incident scope: `Pod`, `Workload`, `Namespace`, `Cluster` |

## Example Rules

### CrashLoop + OOMKilled on Same Pod → P1

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: oom-crashloop
spec:
  priority: 500
  trigger:
    eventType: CrashLoopBackOff
  conditions:
    - eventType: OOMKilled
      scope: samePod
  fires:
    incidentType: OOMKilled
    severity: P1
    summary: "OOM-induced crash loop on pod {{.PodName}} in {{.Namespace}}"
    scope: Pod
```

### NodeNotReady + PodEvicted from Same Node → P1

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: node-failure
spec:
  priority: 600
  trigger:
    eventType: NodeNotReady
  conditions:
    - eventType: PodEvicted
      scope: sameNode
  fires:
    incidentType: NodeFailure
    severity: P1
    summary: "Node {{.NodeName}} failure with evictions"
    resource: node
    scope: Cluster
```

### ImagePullBackOff Without Registry Recovery → P2

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: registry-outage
spec:
  priority: 300
  trigger:
    eventType: ImagePullBackOff
  conditions:
    - eventType: PodHealthy
      scope: sameNamespace
      negate: true   # No healthy pods = registry still failing
  fires:
    incidentType: RegistryOutage
    severity: P2
    summary: "Image registry unreachable — no pods recovered in {{.Namespace}}"
    scope: Namespace
```

### CrashLoop + StalledRollout in Same Namespace → P2

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: bad-deploy
spec:
  priority: 400
  trigger:
    eventType: CrashLoopBackOff
  conditions:
    - eventType: StalledRollout
      scope: sameNamespace
  fires:
    incidentType: BadDeployment
    severity: P2
    summary: "Deployment rollout caused crash loop in {{.Namespace}}"
    scope: Workload
```

## Auto-Detection

The operator can automatically create `RCACorrelationRule` CRs when it detects recurring signal patterns in the buffer. This is controlled by the `autoDetect.*` Helm values and CLI flags.

Auto-generated rules:
- Have `autoGenerated: true` in their annotations
- Include a `confidence` field based on observation frequency
- Expire after the configured expiry duration without new observations
- Are visible in the **RCA Rules** dashboard tab

Enable auto-detection:

```yaml
autoDetect:
  enabled: true
  minOccurrences: 5      # Pattern must be seen at least 5 times
  maxRules: 20           # Cap on auto-generated rules
  interval: 60s          # How often to analyze buffer
  expiry: 1h             # Rule expires if pattern stops repeating
```

## Rule Evaluation Order

1. All `RCACorrelationRule` CRs are loaded and sorted by `spec.priority` (descending).
2. For each signal, rules are evaluated in order. **First match wins.**
3. Built-in signal mappings (from `internal/signals/normalizer.go`) serve as the fallback when no rule fires.

## Inspecting Rules

```bash
# List all rules
kubectl get rcacorrelationrule -A

# Describe a specific rule
kubectl describe rcacorrelationrule node-failure

# Short alias
kubectl get rcr -A
```

## Related

- [RCACorrelationRule CRD reference](../reference/rcacorrelationrule-crd.md) — Full field reference
- [Signal Pipeline](SIGNALS.md) — How signals flow through the system
- [RCAAgent CRD reference](../reference/rcaagent-crd.md) — `spec.signalMappings` for single-signal overrides
