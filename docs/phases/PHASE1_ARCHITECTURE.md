# RCA Operator Phase 1 Production Architecture

## Goal

Phase 1 should ship a production-ready incident detection operator that uses only native Kubernetes APIs to detect cluster issues, persist normalized incidents, and power a web dashboard.

This phase should answer one question reliably:

> What is broken in the cluster right now, when did it become real, what resources are affected, and is this the same incident or a new one?

## Phase 1 Scope

Phase 1 must support:

- Node issues
- Workload issues across Pods, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, and CronJobs
- Kubernetes `Event` correlation
- Incident activation only after the issue remains present for 5 continuous minutes
- A web dashboard backed entirely by `IncidentReport` data
- Single active incident per unique problem fingerprint

Phase 1 must not depend on:

- LLMs
- logs collection
- metrics-server
- Prometheus
- external databases

## Architecture Decision

The best Phase 1 architecture for this repo is:

1. A single operator deployment running controller-runtime manager with leader election enabled.
2. Read-only signal collectors built on informer/cache for core workload and event resources.
3. A dedicated incident engine that owns deduplication, stabilization, activation, resolution, and persistence.
4. `IncidentReport` as the single source of truth for dashboard and notifications.
5. A dashboard server that reads only `IncidentReport` and `RCAAgent`, never raw cluster resources.

This is intentionally different from a loose watcher-to-correlator pipeline. In production, the incident engine must be a first-class lifecycle component, not an implicit side effect of event handling.

## Target Runtime Topology

```text
Kubernetes API Server
        |
        v
+-----------------------------+
| controller-runtime Manager  |
|  - leader election          |
|  - shared cache/informers   |
+-------------+---------------+
              |
              +-------------------------------+
              |                               |
              v                               v
+---------------------------+      +---------------------------+
| Signal Collectors         |      | Dashboard API Server      |
|                           |      |                           |
| - Node collector          |      | Reads IncidentReport CRs  |
| - Workload collector      |      | Reads RCAAgent CRs        |
| - Pod collector           |      | No direct raw K8s reads   |
| - Event collector         |      +---------------------------+
| - Ownership enricher      |
+-------------+-------------+
              |
              v
+-----------------------------+
| Incident Engine             |
|                             |
| - fingerprinting            |
| - 5 min stabilization       |
| - active incident cache     |
| - dedup / merge             |
| - resolve after quiet time  |
| - write IncidentReport      |
+-------------+---------------+
              |
              v
+-----------------------------+
| IncidentReport CRD          |
| single source of truth      |
+-----------------------------+
```

## Pure Kubernetes APIs To Use

Phase 1 should rely on these APIs from Kubernetes v1.35:

- `core/v1` `Node`
- `core/v1` `Pod`
- `core/v1` `Event`
- `apps/v1` `Deployment`
- `apps/v1` `StatefulSet`
- `apps/v1` `DaemonSet`
- `apps/v1` `ReplicaSet`
- `batch/v1` `Job`
- `batch/v1` `CronJob`

Why these:

- `Pod.status` and `containerStatuses` provide the most accurate workload failure state.
- `Node.status.conditions` is the authoritative node health signal.
- `Event` gives cluster-native reasons and timeline context.
- controller resources provide rollout and owner context for grouping pod failures into a higher-level incident.

Useful Kubernetes API references:

- https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/
- https://kubernetes.io/docs/reference/using-api/api-concepts/

The API reference also documents list/watch behavior and `sendInitialEvents=true` with `resourceVersionMatch=NotOlderThan`, which matters for safe startup replay and avoiding missed signals during watch handoff.

## Core Design Principles

### 1. Incident-first, not event-first

Events are transient. Incidents are durable. The operator should treat Kubernetes objects and Events as evidence streams that update a stable incident record.

### 2. One fingerprint, one active incident

The system should never open multiple active incidents for the same problem. Every signal must resolve to a deterministic incident fingerprint.

### 3. Workload-aware grouping

Pods are often symptoms, not the real blast radius. The operator must map each pod to its top-level owner so the incident is attached to the right workload when possible.

### 4. State-based confirmation

Activation must depend on continuous observation for 5 minutes, not one noisy event.

### 5. Dashboard reads normalized data only

The UI should not reconstruct incidents from raw events. It should render the already-correlated `IncidentReport`.

## Recommended Internal Architecture

Keep the Kubebuilder single-group layout. Do not move scaffolded API/controller locations.

Add or reshape internals around these packages:

```text
internal/
  collectors/
    node/
    pod/
    workload/
    event/
    ownership/
  engine/
    incident/
    fingerprint/
    lifecycle/
    resolver/
  reporter/
    incidentreport/
  dashboard/
  controller/
```

Recommended responsibility split:

- `collectors/*`: convert Kubernetes resources into normalized signals
- `engine/fingerprint`: compute canonical identity for an issue
- `engine/lifecycle`: maintain `Detecting -> Active -> Resolved`
- `engine/resolver`: decide when an active incident is cleared
- `reporter`: persist only the final normalized incident state into `IncidentReport`

## Signal Model

Every collector should emit the same normalized signal envelope:

```go
type Signal struct {
    ID                string
    ObservedAt        time.Time
    Source            string
    Category          string
    Reason            string
    Message           string
    SeverityHint      string
    ClusterScoped     bool
    Namespace         string
    ResourceKind      string
    ResourceName      string
    ResourceUID       string
    TopLevelKind      string
    TopLevelName      string
    TopLevelNamespace string
    NodeName          string
    FingerprintParts  map[string]string
    RawEventUID       string
}
```

This is the most important design improvement for the current codebase. Right now the implementation mixes watcher-specific event types with correlation behavior. A normalized signal contract will make Phase 1 easier to reason about and extend.

## What To Detect In Phase 1

### Node incidents

Signals:

- `NodeReady=False`
- `NodeReady=Unknown`
- `NodeDiskPressure=True`
- `NodeMemoryPressure=True`
- `NodePIDPressure=True`
- supporting `Event` reasons such as `NodeNotReady`, eviction-related events, kubelet warnings

Canonical incident types:

- `NodeFailure`
- `NodePressure`

Fingerprint:

- `cluster + incidentType + nodeName`

### Pod-level incidents

Signals:

- CrashLoopBackOff
- OOMKilled
- ImagePullBackOff / ErrImagePull
- Pending too long
- probe failures from `Event`
- forced termination / grace period breach

Canonical incident types:

- `WorkloadCrashLoop`
- `WorkloadResourceFailure`
- `ImagePullFailure`
- `SchedulingFailure`
- `ProbeFailure`
- `TerminationFailure`

Fingerprint:

- Prefer top-level owner:
  `cluster + incidentType + namespace + topLevelKind + topLevelName`
- Fallback when owner cannot be resolved:
  `cluster + incidentType + namespace + podName`

### Workload controller incidents

Signals:

- `Deployment.status.conditions` stalled or unavailable
- `StatefulSet.status` not progressing
- `DaemonSet.status` unavailable/misscheduled pressure
- `Job` repeated failures or backoff exhaustion
- `CronJob` creating failing jobs repeatedly

Canonical incident types:

- `DeploymentRolloutFailure`
- `StatefulSetRolloutFailure`
- `DaemonSetRolloutFailure`
- `JobFailure`
- `CronJobFailure`

Fingerprint:

- `cluster + incidentType + namespace + kind + name`

## Ownership Resolution

This is mandatory for production readiness.

For each Pod signal:

1. Read `ownerReferences`
2. Resolve upward until a stable top-level workload is found
3. Prefer these grouping targets:
   - Deployment
   - StatefulSet
   - DaemonSet
   - Job
   - CronJob
   - ReplicaSet only if no Deployment owner exists
4. Store both affected pod and owning workload in the incident

Without this, the operator will create incident noise for every failing pod replica.

## Incident Lifecycle

Use these lifecycle states:

- `Detecting`
- `Active`
- `Resolved`

### Detecting

Create or update a candidate incident as soon as the first matching signal arrives.

Required fields:

- `firstObservedAt`
- `lastObservedAt`
- `stabilizationDeadline = firstObservedAt + 5m`
- `signalCount`
- `timeline`

### Active

Promote the incident to `Active` only when:

- the same fingerprint continues to receive confirming signals, or
- the underlying broken state is still present,
- for a continuous 5 minute window

Recommended rule:

- transition to `Active` when `now - firstObservedAt >= 5m` and the issue still evaluates as present

### Resolved

Resolve when:

- the broken state is no longer present, and
- no confirming signal has been seen for 5 minutes

This should be recorded as:

- `resolvedAt`
- resolution reason such as `SignalQuietPeriodElapsed`

## The Deduplication Model

Production-ready dedup requires three layers:

### 1. Raw event dedup

Prevent the same Kubernetes `Event` update from generating repeated signals.

Suggested key:

- `involvedObject.uid + reason + reportingController + message hash`

### 2. Signal dedup

Suppress repeated collector emissions for the same unchanged resource condition.

Suggested key:

- `fingerprint + condition/reason + generation/resourceVersion window`

### 3. Incident dedup

Only one open incident may exist for a fingerprint.

Suggested rule:

- unique open incident constraint by `spec.fingerprint` plus status phase in memory
- if a matching incident exists in `Detecting` or `Active`, append timeline and update timestamps instead of creating a new CR

## Recommended IncidentReport Shape

For this Phase 1 architecture, `IncidentReport` should become the durable incident record, not just an alert snapshot.

Recommended fields:

```yaml
spec:
  agentRef: rcaagent-sample
  fingerprint: cluster-a|NodeFailure|gke-node-1
  incidentType: NodeFailure
  scope:
    level: Cluster | Namespace | Workload | Pod
    namespace: default
    workloadRef:
      apiVersion: apps/v1
      kind: Deployment
      name: payments
    resourceRef:
      apiVersion: v1
      kind: Node
      name: gke-node-1

status:
  phase: Detecting | Active | Resolved
  severity: P1 | P2 | P3 | P4
  firstObservedAt: <time>
  activeAt: <time>
  lastObservedAt: <time>
  resolvedAt: <time>
  stabilizationWindow: 5m
  signalCount: 9
  summary: Node not ready
  reason: NodeStatusUnknown
  message: Kubelet stopped posting node status
  affectedResources: []
  evidence: []
  timeline: []
  conditions: []
```

Most importantly, add:

- `fingerprint`
- `firstObservedAt`
- `activeAt`
- `lastObservedAt`
- `signalCount`
- structured scope fields

Those fields are what make dedup, dashboard filtering, and lifecycle correctness easy.

## Severity Model

Recommended Phase 1 severity rules:

- `P1`: cluster-critical or node failure affecting shared workloads
- `P2`: workload unavailable or rollout failure affecting a service
- `P3`: single pod or limited degradation
- `P4`: warning or pre-incident signal

Examples:

- `NodeReady=False/Unknown` for 5 min: `P1`
- Deployment rollout stuck with unavailable replicas: `P2`
- Single pod CrashLoop with replicas still serving: `P3`
- DiskPressure with no workload impact yet: `P3` or `P4`

## Dashboard Architecture

The dashboard should read from operator-owned data only:

- `IncidentReport`
- `RCAAgent`

Recommended API endpoints:

- `GET /api/incidents`
- `GET /api/incidents/:namespace/:name`
- `GET /api/stats`
- `GET /api/timeline?fingerprint=...`

The dashboard should show:

- phase and severity
- summary, reason, message
- first seen, active at, last seen, resolved at
- duration
- affected node/workload/pods
- correlated evidence and event timeline
- generated kubectl commands

This aligns well with the example incident UX you shared.

## How The Example Incident Maps

For the sample node incident:

- `incidentType`: `NodeFailure`
- `phase`: `Resolved`
- `severity`: `P1`
- `fingerprint`: `cluster|NodeFailure|gke-ql-controller-n-ql-controller-n-w-18664a61-5n6c`
- `firstObservedAt`: `16:53:24 UTC`
- `activeAt`: `16:58:24 UTC` only if the node was still unhealthy after 5 minutes
- `summary`: `Node not ready`
- `reason`: `NodeStatusUnknown`
- `message`: `Kubelet stopped posting node status`

Timeline entries:

1. First signal observed
2. Stabilization threshold reached and incident promoted to `Active`
3. Quiet period elapsed and incident auto-resolved

## Controller and Collector Responsibilities

### `RCAAgentReconciler`

Should only do:

- validate agent config
- start or stop collectors for that agent scope
- expose agent health and config status

It should not own incident lifecycle logic directly.

### Collectors

Each collector should be read-only and stateless except for local dedup caches.

- `node collector`: node conditions and node-related events
- `pod collector`: container state, restart loops, image pull, readiness recovery
- `workload collector`: rollout health from Deployment/StatefulSet/DaemonSet/Job/CronJob
- `event collector`: attach supporting evidence and early warnings
- `ownership enricher`: resolve pod to workload

### Incident engine

This should be a single writer for incident state.

That means:

- only the engine creates `IncidentReport`
- only the engine transitions lifecycle
- only the engine decides dedup/merge/resolve

This single-writer rule is the cleanest way to avoid multiple incidents for the same issue.

## Persistence Strategy

Use Kubernetes as the database for Phase 1.

Recommended approach:

- `IncidentReport` CR is the durable store
- in-memory cache holds hot state for active fingerprints
- on startup, rebuild in-memory state from non-resolved `IncidentReport` resources

This avoids needing Redis/Postgres in Phase 1 while still surviving operator restarts.

## HA And Production Readiness

Required for Phase 1 production readiness:

- leader election enabled
- single active incident writer
- informer-backed cache, not repeated polling
- startup state rebuild from CRs
- bounded in-memory caches with TTL cleanup
- structured logs with `fingerprint` and `incidentType`
- metrics for signals seen, incidents opened, incidents activated, incidents resolved, dedup hits
- health and readiness probes

## Recommended Metrics

- `rca_signals_received_total`
- `rca_signals_deduplicated_total`
- `rca_incidents_detecting_total`
- `rca_incidents_activated_total`
- `rca_incidents_resolved_total`
- `rca_active_incidents`
- `rca_incident_transition_seconds`

## What Should Change In This Repo

### Keep

- Kubebuilder project layout
- `RCAAgent`
- `IncidentReport`
- embedded dashboard server
- controller-runtime manager

### Change

- replace watcher-specific event fan-out as the primary model with normalized signals
- introduce a dedicated incident engine package
- move lifecycle rules out of ad hoc correlator behavior
- group incidents at top-level workload or node scope
- enrich every incident with stable fingerprint and scope metadata

### Avoid In Phase 1

- direct dashboard reads from Pods/Events/Nodes
- per-pod incident creation when a workload-level incident exists
- metrics-server dependency
- LLM or log scraping

## Phased Implementation Order

1. Introduce normalized `Signal` contract and ownership resolution
2. Build incident fingerprinting and single-writer incident engine
3. Extend `IncidentReport` schema for lifecycle timestamps and fingerprint
4. Rewire node, pod, workload, and event collectors to emit normalized signals
5. Rebuild dashboard API around new incident fields
6. Add startup recovery, metrics, and lifecycle tests

## Recommended Phase 1 Definition Of Done

Phase 1 is done when:

- the operator detects node and workload failures using only Kubernetes APIs
- a repeated issue creates one active incident per fingerprint
- no incident becomes `Active` before 5 continuous minutes
- resolved incidents close automatically after the issue disappears and the quiet period passes
- the dashboard fully renders from `IncidentReport`
- operator restart does not duplicate or lose open incidents

## Bottom Line

The strongest Phase 1 architecture for RCA Operator is:

- native Kubernetes informer-based collectors
- top-level workload and node-aware fingerprinting
- a dedicated incident engine with strict single-writer lifecycle ownership
- `IncidentReport` as the durable incident database
- a dashboard that renders only normalized incident state

That gives you a production-ready Phase 1 with low operational complexity, clean deduplication, and a durable incident model centered on Kubernetes resources.
