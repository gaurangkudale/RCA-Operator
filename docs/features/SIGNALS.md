# Signal Pipeline

The RCA Operator detects Kubernetes incidents through a three-stage signal pipeline: **Normalize → Enrich → Deduplicate**.

## Architecture

```
Watcher Events
    │
    ▼
┌───────────┐     ┌─────────────┐     ┌─────────────────┐
│ Normalize │────▶│    Enrich   │────▶│ Rule Engine /   │
│           │     │             │     │ Incident Writer  │
└───────────┘     └─────────────┘     └─────────────────┘
     │                   │
     │                   └── Phase 2: CrossSignalEnricher
     │                       queries telemetry backend for
     │                       related traces, metrics, logs
     │
     └── event type → incident type mapping
         (configurable per-agent via spec.signalMappings)
```

### Stage 1: Normalize

The `Normalizer` maps raw watcher `CorrelatorEvent` values to a `NormalizedSignal` using a configurable `SignalMapping` table. Each mapping specifies:

- `eventType` — the raw event type emitted by a watcher
- `incidentType` — the incident category to create (mirrors the event type by default, making incident types self-describing)
- `severity` — default severity (`P1`–`P4`)
- `scopeLevel` — incident scope (`Pod`, `Workload`, `Namespace`, `Cluster`)

Overrides are applied at agent level via `spec.signalMappings`.

### Stage 2: Enrich

The `Enricher` adds context to normalized signals:

- Kubernetes resource metadata (owner references, labels, annotations)
- **Phase 2:** `CrossSignalEnricher` queries the configured telemetry backend for error traces, metric anomalies, and correlated logs in the incident time window. Results populate `status.relatedTraces` and trigger blast radius computation.

Enrichment is non-blocking. If the telemetry backend is unavailable, the incident still fires with Kubernetes-only context.

### Stage 3: Rule Engine / Incident Writer

The rule engine evaluates multi-signal correlation rules against the enriched signal and the sliding-window buffer. When a rule fires, the incident type and severity are overridden by the rule's output.

The `Reporter` then persists the `IncidentReport` CR via the Kubernetes API.

---

## Default Signal Mappings

14 event types are recognized out of the box:

| Event Type | Incident Type | Default Severity | Scope | Source |
|---|---|---|---|---|
| `CrashLoopBackOff` | `CrashLoopBackOff` | P3 | Pod | PodWatcher |
| `OOMKilled` | `OOMKilled` | P2 | Pod | PodWatcher |
| `ImagePullBackOff` | `ImagePullBackOff` | P3 | Workload | PodWatcher |
| `PodPendingTooLong` | `PodPendingTooLong` | P3 | Pod | PodWatcher |
| `GracePeriodViolation` | `GracePeriodViolation` | P2 | Pod | PodWatcher |
| `NodeNotReady` | `NodeNotReady` | P1 | Cluster | EventWatcher / NodeWatcher |
| `PodEvicted` | `PodEvicted` | P2 | Pod | EventWatcher |
| `ProbeFailure` | `ProbeFailure` | P3 | Pod | EventWatcher |
| `StalledRollout` | `StalledRollout` | P2 | Workload | DeploymentWatcher |
| `NodePressure` | `NodePressure` | P2 | Cluster | NodeWatcher |
| `StalledStatefulSet` | `StalledStatefulSet` | P2 | Workload | StatefulSetWatcher |
| `StalledDaemonSet` | `StalledDaemonSet` | P2 | Workload | DaemonSetWatcher |
| `JobFailed` | `JobFailed` | P3 | Workload | JobWatcher |
| `CronJobFailed` | `CronJobFailed` | P3 | Workload | CronJobWatcher |

> `NodePressure` severity is `P2` for `DiskPressure` and `MemoryPressure`, `P3` for `PIDPressure`.

### Lifecycle Events (not mapped to incidents)

Two event types trigger incident resolution rather than creation:

| Event Type | Effect |
|---|---|
| `PodHealthy` | Emitted when a pod transitions to Running+Ready. Triggers resolution of any Active incidents for that pod. |
| `PodDeleted` | Emitted when a watched pod is removed. Triggers immediate resolution of any Active incidents for that pod. |

---

## Watchers

Each watcher monitors specific Kubernetes resources via the controller-runtime informer cache:

| Watcher | Watched Resources | Emitted Events |
|---|---|---|
| `PodWatcher` | `core/v1 Pod` | `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, `PodPendingTooLong`, `GracePeriodViolation`, `PodHealthy`, `PodDeleted` |
| `EventWatcher` | `core/v1 Event` | `NodeNotReady`, `PodEvicted`, `ProbeFailure` |
| `DeploymentWatcher` | `apps/v1 Deployment` | `StalledRollout` |
| `NodeWatcher` | `core/v1 Node` | `NodeNotReady`, `NodePressure` |
| `StatefulSetWatcher` | `apps/v1 StatefulSet` | `StalledStatefulSet` |
| `DaemonSetWatcher` | `apps/v1 DaemonSet` | `StalledDaemonSet` |
| `JobWatcher` | `batch/v1 Job` | `JobFailed` |
| `CronJobWatcher` | `batch/v1 CronJob` | `CronJobFailed` |

---

## Deduplication

The signal pipeline prevents duplicate incidents through several mechanisms:

### Dedup Key

Each event type carries a stable `DedupKey()` used to look up existing open `IncidentReport` CRs. Key format varies by scope:

- Pod-scoped: `EventType:namespace:podName:containerName` (or `:podUID` for OOMKilled)
- Workload-scoped: `EventType:namespace:deploymentName`
- Node-scoped: `EventType:namespace:nodeName`

### Reopen Window

If a resolved incident's `DedupKey` is seen again within the reopen window (5 minutes), the existing CR is reopened rather than creating a new one.

### ExitCode Suppression

When multiple consecutive crashes occur with the same exit code, `ExitCodePattern` (P3) incidents are suppressed once the `ConsecutiveExitCode` threshold (3 crashes) is reached. Only the `ConsecutiveExitCode` P2 incident is created to avoid duplicate incidents for the same root cause.

---

## Overriding Default Mappings

Use `spec.signalMappings` on an `RCAAgent` to change the default behavior for specific event types:

```yaml
spec:
  signalMappings:
    - eventType: CrashLoopBackOff
      incidentType: CrashLoopBackOff
      severity: P2          # Promote to P2 in production
      scope: Pod
    - eventType: ProbeFailure
      incidentType: ProbeFailure
      severity: P2          # Treat probe failures as higher severity
```

Valid severity values: `P1`, `P2`, `P3`, `P4`
Valid scope values: `Pod`, `Workload`, `Namespace`, `Cluster`

---

## Related

- [RCAAgent CRD reference](../reference/rcaagent-crd.md) — `spec.signalMappings`
- [Correlation Rules](CORRELATION-RULES.md) — Multi-signal rule evaluation
- [AI Investigation](AI-INVESTIGATION.md) — LLM-powered root cause analysis
