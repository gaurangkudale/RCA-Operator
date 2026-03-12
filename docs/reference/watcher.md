# Watcher Layer Reference

The watcher layer is the **observation engine** of the RCA Operator. It registers informers against the Kubernetes API server, detects pod failure signals in real time, and emits strongly-typed events to the correlator pipeline.

One `PodWatcher` instance is started per `RCAAgent` CR. It shuts down automatically when the CR is deleted.

---

## Architecture

```
RCAAgent CR
    │  spec.watchNamespaces
    ▼
PodWatcher  ──────────────────────────────────────────────────────────────────►
    │  controller-runtime informer (Add / Update / Delete)
    │  periodic scans (Pending, Ready, GracePeriod)
    │
    ▼
EventEmitter (ChannelEventEmitter)
    │  non-blocking send — drops and logs on full channel
    │
    ▼
chan CorrelatorEvent  ──►  Correlator
```

The channel is shared across all `RCAAgent` instances managed by the same operator process.

---

## Event Types

The watcher emits eight discrete event types. Each type carries a `BaseEvent` (shared context) plus event-specific fields.

### BaseEvent — Fields Carried by All Events

| Field       | Type        | Description                                              |
|-------------|-------------|----------------------------------------------------------|
| `At`        | `time.Time` | Timestamp the event was detected                         |
| `AgentName` | `string`    | Name of the `RCAAgent` CR that owns this watcher         |
| `Namespace` | `string`    | Namespace of the affected pod                            |
| `PodName`   | `string`    | Name of the affected pod                                 |
| `PodUID`    | `string`    | UID of the affected pod                                  |
| `NodeName`  | `string`    | Node the pod is (or was) scheduled on                    |

---

### CrashLoopBackOff

**Trigger:** A container enters `CrashLoopBackOff` waiting state AND its restart count meets or exceeds `CrashLoopRestartThreshold`.  
**Re-emission:** Emitted on every reconcile where the restart count increases while the container remains in `CrashLoopBackOff`.

| Field           | Type     | Description                                        |
|-----------------|----------|----------------------------------------------------|
| `ContainerName` | `string` | Name of the crashing container                     |
| `RestartCount`  | `int32`  | Current container restart count                    |
| `Threshold`     | `int32`  | Configured restart threshold that was exceeded     |

**Dedup key:** `CrashLoopBackOff:<namespace>:<pod>:<container>`

---

### OOMKilled

**Trigger:** A container terminates with Kubernetes reason `OOMKilled`.

| Field           | Type     | Description                                      |
|-----------------|----------|--------------------------------------------------|
| `ContainerName` | `string` | Name of the OOM-killed container                 |
| `ExitCode`      | `int32`  | Container exit code (typically `137`)            |
| `Reason`        | `string` | Kubernetes termination reason string             |

**Dedup key:** `OOMKilled:<namespace>:<pod>:<container>:<podUID>`  
The pod UID keeps each new pod incarnation unique — a restarted pod produces a fresh event.

---

### ImagePullBackOff

**Trigger:** A container enters `ImagePullBackOff` or `ErrImagePull` waiting state.

| Field           | Type     | Description                                       |
|-----------------|----------|---------------------------------------------------|
| `ContainerName` | `string` | Name of the affected container                    |
| `Reason`        | `string` | Kubernetes waiting reason (`ImagePullBackOff`, …) |
| `Message`       | `string` | Kubernetes waiting message (registry error detail)|

**Dedup key:** `ImagePullBackOff:<namespace>:<pod>:<container>`

---

### PodPendingTooLong

**Trigger:** A pod remains in `Pending` phase longer than `PendingTimeout` (default `5m`). Evaluated by a periodic scan every `PendingScanInterval`.

| Field        | Type            | Description                                       |
|--------------|-----------------|---------------------------------------------------|
| `PendingFor` | `time.Duration` | How long the pod has been Pending at detection    |
| `Timeout`    | `time.Duration` | Configured pending timeout threshold              |

**Dedup key:** `PodPendingTooLong:<namespace>:<pod>`  
Each pod only triggers this event once per watcher lifetime (tracked in memory).

---

### GracePeriodViolation

**Trigger:** A pod is in Terminating state and still has running containers after `DeletionGracePeriodSeconds` (default `30s`) has elapsed.

| Field                | Type            | Description                                          |
|----------------------|-----------------|------------------------------------------------------|
| `GracePeriodSeconds` | `int64`         | The configured grace period in seconds               |
| `OverdueFor`         | `time.Duration` | How far past the grace period the pod has gone       |

**Dedup key:** `GracePeriodViolation:<namespace>:<pod>:<podUID>`

---

### PodHealthy  *(resolution signal)*

**Trigger:** A pod transitions to `Running` phase with all containers `Ready`, and this stable state persists for `ReadyStabilityWindow` (default `60s`).

This event signals the correlator to resolve any active `IncidentReport` linked to the pod.

No extra fields beyond `BaseEvent`.

**Dedup key:** `PodHealthy:<namespace>:<pod>`

---

### PodDeleted  *(resolution signal)*

**Trigger:** A watched pod is removed from the cluster (informer `DeleteFunc`).

This event triggers immediate resolution of any active `IncidentReport` referencing the pod, regardless of its health state.

No extra fields beyond `BaseEvent`.

**Dedup key:** `PodDeleted:<namespace>:<pod>`

---

## Configuration

Detection thresholds are set on the `PodWatcher` when the `RCAAgent` controller starts a watcher. In Phase 1, the thresholds below use built-in defaults. Future versions will expose them directly in `RCAAgentSpec`.

| Parameter                   | Default  | Description                                                        | Governed by CRD field         |
|-----------------------------|----------|--------------------------------------------------------------------|-------------------------------|
| `CrashLoopRestartThreshold` | `3`      | Minimum restart count before a `CrashLoopBackOff` event fires      | *(Phase 2: spec field)*       |
| `PendingTimeout`            | `5m`     | How long a pod must be Pending before `PodPendingTooLong` fires    | *(Phase 2: spec field)*       |
| `PendingScanInterval`       | `30s`    | How often the watcher scans for Pending/Ready/GracePeriod states   | *(Phase 2: spec field)*       |
| `ReadyStabilityWindow`      | `60s`    | How long a pod must be Ready before `PodHealthy` fires             | *(Phase 2: spec field)*       |
| `WatchNamespaces`           | —        | Namespaces the watcher monitors; inherits `spec.watchNamespaces`   | `spec.watchNamespaces`        |

### spec.watchNamespaces

```yaml
spec:
  watchNamespaces:
    - production
    - staging
```

Events are only emitted for pods in listed namespaces. Filtering is enforced in the informer handler before any detection logic runs. Duplicate entries are silently de-duplicated by the controller.

> **Note:** If a namespace does not exist at the time the `RCAAgent` reconciles, the operator logs a warning and continues. The watcher will start receiving events for that namespace once it is created.

---

## Exit Code Classification

CrashLoop incidents attach the last non-zero, non-OOM exit code as diagnostic context using the categories below.

| Exit Code | Category            | Description                             |
|-----------|---------------------|-----------------------------------------|
| `1`       | `GeneralError`      | General application error               |
| `2`       | `ShellMisuse`       | Misuse of shell builtins                |
| `126`     | `PermissionDenied`  | Command invoked cannot execute          |
| `127`     | `CommandNotFound`   | Command not found                       |
| `130`     | `Interrupted`       | Script terminated by Control-C (SIGINT) |
| `134`     | `Abort`             | Process aborted (SIGABRT)               |
| `137`     | *handled as OOMKilled* | See [OOMKilled](#oomkilled)          |
| `139`     | `SegmentationFault` | Segmentation fault (SIGSEGV)            |
| `143`     | `Terminated`        | Terminated by SIGTERM                   |
| `255`     | `OutOfRange`        | Exit status out of range                |
| *(other)* | `NonZeroExit`       | Unclassified non-zero exit code         |

---

## Backpressure and Event Dropping

The `ChannelEventEmitter` sends events to the correlator channel **without blocking**. If the channel is full (correlator is slow or stopped), the event is silently dropped and a log line is emitted:

```
Dropped watcher event because correlator channel is full  eventType=<type>  dedupKey=<key>
```

This prevents a lagging correlator from stalling pod informer callbacks. If you see frequent drop messages, increase the correlator channel buffer or tune the scan interval.

---

## Watcher Lifecycle

| Trigger                          | Action                                         |
|----------------------------------|------------------------------------------------|
| `RCAAgent` CR created            | Watcher started; bootstrap scan runs once after cache sync |
| `spec.watchNamespaces` changed   | Old watcher cancelled; new watcher started with updated namespaces |
| `RCAAgent` CR deleted            | Watcher context cancelled; goroutines exit cleanly |
| Operator process restart         | All watchers recreated from existing `RCAAgent` CRs on first reconcile |

In-memory state (pending-alerted set, ready-since timestamps, healthy-alerted set) is **not persisted**. After a restart, the bootstrap scan re-evaluates all existing pods so no failures are missed, but previously suppressed dedup entries will fire once on restart.

---

## Dedup Key Formats

| Event Type             | Dedup Key Format                                                                 |
|------------------------|----------------------------------------------------------------------------------|
| `CrashLoopBackOff`     | `CrashLoopBackOff:<namespace>:<pod>:<container>`                                 |
| `OOMKilled`            | `OOMKilled:<namespace>:<pod>:<container>:<podUID>`                               |
| `ImagePullBackOff`     | `ImagePullBackOff:<namespace>:<pod>:<container>`                                 |
| `PodPendingTooLong`    | `PodPendingTooLong:<namespace>:<pod>`                                            |
| `GracePeriodViolation` | `GracePeriodViolation:<namespace>:<pod>:<podUID>`                                |
| `PodHealthy`           | `PodHealthy:<namespace>:<pod>`                                                   |
| `PodDeleted`           | `PodDeleted:<namespace>:<pod>`                                                   |

Dedup keys are used by the correlator to identify unique active incidents. Events with the same dedup key update the existing incident rather than creating a new one.

---

## Related

- [RCAAgent CRD reference](rcaagent-crd.md) — full `spec` field reference
- [IncidentReport CRD reference](incidentreport-crd.md) — incident lifecycle and status fields
- [Architecture](../concepts/Architecture.md) — how the watcher fits in the full operator pipeline
