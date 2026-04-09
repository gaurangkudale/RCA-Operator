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
  relatedTraces:
    - "abc123def456ghi789"
    - "jkl012mno345pqr678"
  blastRadius:
    - "api-gateway"
    - "frontend"
  rca:
    rootCause: "Memory leak in payment-svc caused OOMKilled events"
    confidence: "0.92"
    playbook:
      - "kubectl rollout undo deployment/payment-svc -n production"
      - "kubectl scale deployment/payment-svc --replicas=6"
    evidence:
      - "Trace abc123: 500ms latency spike on /checkout"
      - "Log: java.lang.OutOfMemoryError at 10:15:01"
    investigatedAt: "2026-04-01T10:20:00Z"
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

### Core Status

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
| `signalCount` | `int64` | Number of confirming signals in the current lifecycle |
| `notified` | `bool` | Whether Slack/PagerDuty notifications have been sent |
| `affectedResources` | `[]AffectedResource` | Kubernetes resources involved in this incident |
| `correlatedSignals` | `[]string` | Raw signals that triggered this incident |
| `timeline` | `[]TimelineEvent` | Ordered sequence of incident events |

### Phase 2 Cross-Signal Status

These fields are populated by the cross-signal enricher when a telemetry backend is configured.

| Field | Type | Description |
|---|---|---|
| `relatedTraces` | `[]string` | W3C trace IDs correlated with this incident from the configured telemetry backend |
| `blastRadius` | `[]string` | Downstream services impacted by this incident, computed by BFS over the service dependency graph |

### status.rca

Populated when an AI investigation completes (either via `autoInvestigate` or a manual `POST /api/investigate/{ns}/{name}` call).

| Field | Type | Description |
|---|---|---|
| `rootCause` | `string` | AI-determined root cause summary |
| `confidence` | `string` | AI confidence score (0.0–1.0 as string, e.g. `"0.92"`) |
| `playbook` | `[]string` | Ordered list of suggested remediation steps |
| `evidence` | `[]string` | Telemetry evidence the AI used (trace IDs, log lines, metric anomalies) |
| `investigatedAt` | `Time` | When the AI investigation was performed |

### Lifecycle Phases

```text
Detecting ──(stabilization window)──> Active ──(pod healthy/deleted)──> Resolved
    ^                                                                      |
    └──────────────────(signal recurrence)─────────────────────────────────┘
```

- **Detecting**: Initial signal received; waiting for stabilization window confirmation
- **Active**: Incident confirmed; notifications sent; AI investigation triggered if `autoInvestigate=true`
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

```bash
# List all incidents
kubectl get incidentreport -A

# Active incidents only
kubectl get incidentreport -A -l phase=Active

# Incidents for a specific severity
kubectl get incidentreport -A -l severity=P1

# Full detail
kubectl describe incidentreport <name> -n <namespace>

# Watch for new incidents
kubectl get incidentreport -A -w

# Get the AI RCA result for an incident
kubectl get incidentreport <name> -n <namespace> -o jsonpath='{.status.rca}' | jq .

# Get blast radius for an incident
kubectl get incidentreport <name> -n <namespace> -o jsonpath='{.status.blastRadius}'
```

## Related

- [RCAAgent CRD reference](rcaagent-crd.md)
- [RCACorrelationRule CRD reference](rcacorrelationrule-crd.md)
- [Dashboard API reference](dashboard-api.md)
- [AI Investigation feature](../features/AI-INVESTIGATION.md)
- [RBAC permissions](rbac.md)
