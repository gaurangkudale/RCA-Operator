# IncidentReport CRD Reference

`IncidentReport` is created automatically by the operator for each detected incident. Users do not create these directly — they are managed by the incident engine.

```bash
kubectl get incidentreport -A
kubectl describe incidentreport <name> -n <namespace>
```

## Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: IncidentReport
metadata:
  name: crashloopbackoff-payment-abc123
  namespace: production
spec:
  agentRef: sre-agent
  fingerprint: "Workload|production|deployment|payment-service"
  incidentType: CrashLoopBackOff
  scope:
    level: Workload
    namespace: production
    workloadRef:
      apiVersion: apps/v1
      kind: Deployment
      namespace: production
      name: payment-service
status:
  phase: Active
  severity: P2
  incidentType: CrashLoopBackOff
  summary: "CrashLoopBackOff: container app in pod payment-abc123 (restarts: 8)"
  firstObservedAt: "2026-04-01T10:00:00Z"
  activeAt: "2026-04-01T10:05:00Z"
  lastObservedAt: "2026-04-01T10:15:00Z"
  signalCount: 5
  notified: true
  affectedResources:
    - apiVersion: apps/v1
      kind: Deployment
      namespace: production
      name: payment-service
  correlatedSignals:
    - "CrashLoopBackOff (restarts: 8)"
  timeline:
    - time: "2026-04-01T10:00:00Z"
      event: "Incident detected: CrashLoopBackOff"
    - time: "2026-04-01T10:05:00Z"
      event: "Phase transition: Detecting → Active"
```

## Spec Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `agentRef` | `string` | Yes | Name of the RCAAgent that created this report |
| `fingerprint` | `string` | Yes | Canonical identity for deduplication (stable across repeated signals) |
| `incidentType` | `string` | Yes | Durable incident category (e.g. `CrashLoopBackOff`, `OOMKilled`) |
| `scope` | `IncidentScope` | No | Primary object or workload the incident belongs to |

### spec.scope

| Field | Type | Description |
|---|---|---|
| `level` | `string` | One of `Cluster`, `Namespace`, `Workload`, `Pod` |
| `namespace` | `string` | Populated for namespace-, workload-, and pod-scoped incidents |
| `workloadRef` | `IncidentObjectRef` | Top-level workload (e.g. Deployment) when applicable |
| `resourceRef` | `IncidentObjectRef` | Primary affected object (e.g. Node for cluster-scoped) |

## Status Fields

| Field | Type | Description |
|---|---|---|
| `phase` | `string` | Current lifecycle phase: `Detecting`, `Active`, or `Resolved` |
| `severity` | `string` | Incident severity: `P1`, `P2`, `P3`, or `P4` |
| `incidentType` | `string` | Self-describing incident type from the raw event |
| `summary` | `string` | Human-readable summary for dashboard display |
| `reason` | `string` | Machine-oriented Kubernetes reason when available |
| `message` | `string` | Detailed message for the most recent signal |
| `firstObservedAt` | `Time` | When the incident fingerprint was first seen |
| `activeAt` | `Time` | When the incident crossed the stabilization window |
| `lastObservedAt` | `Time` | When the most recent confirming signal was received |
| `resolvedAt` | `Time` | When the incident was resolved (empty while active) |
| `startTime` | `Time` | Deprecated alias for `firstObservedAt`; retained for backward compatibility |
| `resolvedTime` | `Time` | Deprecated alias for `resolvedAt`; retained for backward compatibility |
| `signalCount` | `int64` | Number of confirming signals in the current lifecycle |
| `stabilizationWindowSeconds` | `int64` | Stabilization window applied to this incident, in seconds |
| `notified` | `bool` | Whether Slack/PagerDuty notifications have been sent |
| `affectedResources` | `[]AffectedResource` | Kubernetes resources involved in this incident |
| `correlatedSignals` | `[]string` | Raw signals that triggered this incident |
| `timeline` | `[]TimelineEvent` | Ordered sequence of incident events |
| `conditions` | `[]metav1.Condition` | Standard Kubernetes status conditions (Active, Resolved, etc.) |
| `incidentGraph` | `runtime.RawExtension` | Serialized blast-radius topology graph — see [Topology Graph](../features/topology-graph.md) |

### Lifecycle Phases

```text
Detecting ──(stabilization window)──> Active ──(pod healthy/deleted)──> Resolved
    ^                                                                      |
    └──────────────────(signal recurrence)─────────────────────────────────┘
```

- **Detecting**: Initial signal received; waiting for stabilization window confirmation
- **Active**: Incident confirmed; notifications sent
- **Resolved**: Underlying issue cleared; auto-resolved when affected pod becomes healthy or is deleted

### Severity Levels

| Level | Scope | Description |
|---|---|---|
| P1 | Cluster-wide | Node failures, mass evictions |
| P2 | Namespace / Workload | Correlated multi-signal incidents |
| P3 | Single service | Single-signal incidents (CrashLoopBackOff, ImagePullBackOff) |
| P4 | Warning | Informational, low-urgency events |

## Print Columns

`kubectl get incidentreport` shows:

| Column | Description |
|---|---|
| Severity | P1–P4 |
| Phase | Detecting, Active, Resolved |
| Type | Incident type |
| Notified | Whether notifications were sent |
| FirstSeen | When first observed |
| Age | Resource age |

## kubectl Cheatsheet

The reporter writes incident metadata to labels prefixed with `rca.rca-operator.tech/` (see `internal/reporter/cr_reporter.go`). Phase is **not** mirrored to a label — filter by phase with the `STATUS` print column or jq.

```bash
# List all incidents (with the operator's print columns)
kubectl get incidentreport -A

# Filter by severity (label) — works with selectors
kubectl get incidentreport -A -l rca.rca-operator.tech/severity=P1

# Filter by incident type (label)
kubectl get incidentreport -A -l rca.rca-operator.tech/incident-type=CrashLoopBackOff

# Active incidents only — phase lives in status, not a label, so use jq
kubectl get incidentreport -A -o json \
  | jq -r '.items[] | select(.status.phase=="Active") | "\(.metadata.namespace)/\(.metadata.name)"'

# Full detail
kubectl describe incidentreport <name> -n <namespace>

# Watch for new incidents
kubectl get incidentreport -A -w
```

## Related

- [RCAAgent CRD reference](rcaagent-crd.md)
- [RCACorrelationRule CRD reference](rcacorrelationrule-crd.md)
- [RBAC permissions](rbac.md)
