# Beginner Guide

This guide is for contributors who are new to Go, new to Kubernetes operators, or new to RCA Operator.

Its goal is simple:

> help you understand how the current implementation works end to end, so you can read the code with confidence and make your first change safely.

This is not a full API reference. For field-by-field CRD details, see the documents under `docs/reference/`.

---

## What RCA Operator Does

RCA Operator watches Kubernetes resources, detects failure signals, correlates related signals, and stores the result as durable `IncidentReport` custom resources.

In one sentence:

`RCAAgent` configures what to watch, collectors emit signals, the incident engine processes them, and `IncidentReport` stores the final incident state.

The durable source of truth is the `IncidentReport` CRD, not an in-memory map and not the dashboard.

---

## The Three Main CRDs

### `RCAAgent`

This is the main configuration object users create.

It tells the operator:

- which namespaces to watch
- how long resolved incidents should be retained
- whether Slack or PagerDuty notifications are configured
- optional OpenTelemetry settings
- optional signal mapping overrides

Read:

- `api/v1alpha1/rcaagent_types.go`
- `config/samples/rca_v1alpha1_rcaagent.yaml`

### `RCACorrelationRule`

This is the rule definition CRD.

It lets users describe multi-signal logic without changing Go code. For example:

- if `NodeNotReady` happens
- and `PodEvicted` also happened on the same node
- then fire a P1 cluster-scoped incident

Read:

- `api/v1alpha1/rcacorrelationrule_types.go`
- `config/rules/`

### `IncidentReport`

This is the output CRD created and maintained by the operator.

It stores:

- incident identity (`fingerprint`)
- lifecycle phase (`Detecting`, `Active`, `Resolved`)
- severity and summary
- affected resources
- correlated signals
- timeline
- notification state

Read:

- `api/v1alpha1/incidentreport_types.go`

---

## The Runtime Architecture

At runtime, the manager in `cmd/main.go` wires together several subsystems:

1. Kubernetes controllers
2. watcher-based signal collectors
3. the incident engine
4. the CRD-backed rule engine
5. the dashboard server
6. notification dispatch
7. optional webhooks and auto-detection

The main entrypoint is:

- `cmd/main.go`

Important things `main.go` does:

- creates the controller-runtime manager
- registers CRDs into the scheme
- creates the shared signal channel
- creates the incident engine
- registers the dashboard server
- registers the `RCAAgent`, `IncidentReport`, and `RCACorrelationRule` controllers
- optionally enables webhooks
- optionally enables auto-detection

---

## End-to-End Workflow

This is the most important section for a newcomer.

### Step 1: User creates an `RCAAgent`

The `RCAAgentReconciler` reacts to that CR.

Its job is not to create incidents directly. Its main responsibilities are:

- validate the agent spec
- validate referenced notification secrets
- add/remove the finalizer
- start and stop watchers
- clean up old resolved incidents
- resolve orphaned incidents when resources disappear

Read:

- `internal/controller/rcaagent_controller.go`

### Step 2: Watchers start collecting signals

The operator currently has watchers for:

- Pods
- Kubernetes Events
- Deployments
- Nodes
- StatefulSets
- DaemonSets
- Jobs
- CronJobs

These watchers are read-only. They do not create `IncidentReport` objects themselves.

Their job is to observe Kubernetes state and emit typed Go events such as:

- `CrashLoopBackOffEvent`
- `OOMKilledEvent`
- `ImagePullBackOffEvent`
- `NodeNotReadyEvent`
- `PodEvictedEvent`
- `StalledRolloutEvent`

Read:

- `internal/watcher/events.go`
- `internal/watcher/pod_watcher.go`
- `internal/watcher/event_watcher.go`
- `internal/watcher/deployment_watcher.go`
- `internal/watcher/node_watcher.go`
- `internal/watcher/statefulset_watcher.go`
- `internal/watcher/daemonset_watcher.go`
- `internal/watcher/job_watcher.go`
- `internal/watcher/cronjob_watcher.go`

### Step 3: Watchers emit events into a shared channel

The collectors use a shared channel emitter.

That emitter is intentionally non-blocking. If the channel is full, the event is dropped and a log line is written.

Read:

- `internal/watcher/emitter.go`
- `internal/collectors/collectors.go`

### Step 4: The incident engine consumes events

The incident engine is created in `cmd/main.go` and implemented by:

- `internal/engine/engine.go`
- `internal/correlator/consumer.go`

The engine owns the runtime loop that receives watcher events and turns them into incident updates.

### Step 5: Signal pipeline runs

The current pipeline is:

1. Normalize
2. Enrich
3. Evaluate correlation rules
4. Write or update `IncidentReport`

#### Normalize

The normalizer converts a raw watcher event into a common `incident.Input` shape.

Example:

- raw event: `watcher.CrashLoopBackOffEvent`
- normalized output: namespace, agent name, incident type, severity, summary, dedup key, initial scope

Read:

- `internal/signals/normalizer.go`

#### Enrich

The enricher adds Kubernetes metadata that may not be present in the raw event.

For pod-originated signals it tries to resolve:

- the top-level workload owner
- the proper incident scope
- the affected resource list

This is how a pod-level signal can become a workload-level incident.

Read:

- `internal/signals/enricher.go`
- `internal/incident/resolver.go`

#### Correlate

The rule engine evaluates whether the incoming event should override the default classification.

Important detail:

- correlation rules are loaded from `RCACorrelationRule` CRs
- the first highest-priority matching rule wins
- rules are not hardcoded in Go

Read:

- `internal/rulengine/crd_engine.go`
- `internal/controller/rcacorrelationrule_controller.go`

### Step 6: Reporter creates or updates `IncidentReport`

The reporter is the single place that writes incident CRs.

It decides whether to:

- create a new incident
- update an existing open incident
- reopen a recently resolved incident

It also maintains:

- labels
- annotations
- phase state
- timeline
- signal count
- affected resources

Read:

- `internal/reporter/cr_reporter.go`
- `internal/incident/model.go`

### Step 7: `IncidentReport` controller manages lifecycle

After a report exists, the `IncidentReportReconciler` handles lifecycle transitions.

The main lifecycle is:

- `Detecting`
- `Active`
- `Resolved`

This controller is responsible for:

- promoting `Detecting` to `Active` after the stabilization window
- resolving incidents when signals stop and resource state is healthy again
- sending notifications for open and resolved incidents
- recording lifecycle metrics

Read:

- `internal/controller/incidentreport_controller.go`
- `internal/incidentstatus/status.go`

### Step 8: Dashboard and notifications consume durable state

The dashboard and notification system do not inspect raw pod/node state to decide what happened.

They read the normalized durable incident state from CRs.

Read:

- `internal/dashboard/server.go`
- `internal/notify/dispatcher.go`

---

## A Concrete Example

Here is a simple path to trace in code:

### CrashLoopBackOff on a Pod

1. `PodWatcher` sees a pod container waiting with reason `CrashLoopBackOff`
2. it emits `CrashLoopBackOffEvent`
3. `Consumer.handleEvent` receives it
4. `Normalizer` maps it to an `incident.Input`
5. `Enricher` resolves workload ownership if possible
6. rule engine checks whether a correlation rule changes severity or summary
7. `Reporter.EnsureSignal` creates or updates an `IncidentReport`
8. the report starts in `Detecting`
9. `IncidentReportReconciler` later promotes it to `Active` if the issue remains
10. if the pod becomes healthy or disappears, the incident is resolved

Useful files for this trace:

- `internal/watcher/pod_watcher.go`
- `internal/correlator/consumer.go`
- `internal/signals/normalizer.go`
- `internal/signals/enricher.go`
- `internal/reporter/cr_reporter.go`
- `internal/controller/incidentreport_controller.go`

---

## Key Go Concepts You Will See In This Repo

### Structs

Go uses `struct` as the main data type for stateful objects.

Examples:

- `RCAAgentReconciler`
- `IncidentReportReconciler`
- `Consumer`
- `Reporter`
- `CRDRuleEngine`

### Methods With Receivers

This:

```go
func (r *RCAAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
```

means:

- this function belongs to `RCAAgentReconciler`
- `r` is the receiver, like `self` in Python or `this` in other languages

### Interfaces

Interfaces define behavior instead of concrete implementation.

Examples in this repo:

- watcher events implement `CorrelatorEvent`
- rule engines implement shared rule engine contracts
- emitters hide how signals are delivered

### Goroutines

Watchers and background loops often run in goroutines.

Examples:

- periodic scans in watchers
- dashboard server shutdown handling
- auto-detection loop

### Channels

Channels are how different goroutines pass messages safely.

RCA Operator uses a shared signal channel to move watcher events into the incident engine.

### Context

`context.Context` is used to control request lifetime and shutdown.

When the manager stops, contexts are cancelled and background loops should stop too.

---

## File Reading Order For New Contributors

If you want the smoothest path through the codebase, read files in this order:

1. `README.md`
2. `docs/concepts/Architecture.md`
3. `cmd/main.go`
4. `api/v1alpha1/rcaagent_types.go`
5. `api/v1alpha1/rcacorrelationrule_types.go`
6. `api/v1alpha1/incidentreport_types.go`
7. `internal/controller/rcaagent_controller.go`
8. `internal/watcher/events.go`
9. `internal/watcher/pod_watcher.go`
10. `internal/watcher/event_watcher.go`
11. `internal/correlator/consumer.go`
12. `internal/signals/normalizer.go`
13. `internal/signals/enricher.go`
14. `internal/reporter/cr_reporter.go`
15. `internal/controller/incidentreport_controller.go`
16. `internal/rulengine/crd_engine.go`
17. `internal/dashboard/server.go`
18. `internal/notify/dispatcher.go`

If you are short on time, read only:

- `cmd/main.go`
- `internal/controller/rcaagent_controller.go`
- `internal/correlator/consumer.go`
- `internal/reporter/cr_reporter.go`
- `internal/controller/incidentreport_controller.go`

---

## How Incident Identity Works

One of the most important design details is the incident fingerprint.

The fingerprint is a stable identity derived from scope, such as:

- cluster + node
- namespace + workload
- namespace + pod

It intentionally does **not** include `IncidentType`.

Why?

Because different signals affecting the same underlying thing should often collapse into one incident instead of creating duplicates.

Read:

- `internal/incident/model.go`

---

## Cleanup, Retention, and Safety Nets

The operator also handles the less exciting but very important maintenance paths.

### Retention cleanup

Resolved incidents are deleted after the retention window configured on `RCAAgent`.

Read:

- `internal/controller/rcaagent_controller.go`
- `internal/retention/parse.go`

### Orphan resolution

If a pod disappears and the watcher missed the delete event, the agent reconciler can still resolve the incident later.

This keeps incident state from getting stuck forever.

### Startup consolidation

The reporter has logic to merge duplicate open incidents that may already exist in the cluster from previous runs or older behavior.

This helps keep one canonical incident per fingerprint.

---

## Webhooks and Validation

Webhooks are optional at runtime and enabled by flag.

When enabled, they validate and default:

- `RCAAgent`
- `RCACorrelationRule`

Read:

- `internal/webhook/rcaagent_webhook.go`
- `internal/webhook/rcacorrelationrule_webhook.go`

---

## Metrics and Observability

The project exposes Prometheus metrics for:

- signals received
- signals deduplicated
- incidents detecting
- incidents activated
- incidents resolved
- active incident gauge
- lifecycle transition timings
- rule evaluations
- notifications

Read:

- `internal/metrics/incidents.go`
- `docs/reference/metrics.md`

---

## Good First Contribution Areas

If you are new, these areas are usually easier to work in safely:

- documentation updates
- unit tests for watcher logic
- unit tests for normalizer or enricher behavior
- small dashboard API improvements
- new sample `RCACorrelationRule` YAML
- metrics or logging improvements

Harder areas, but good after you are comfortable:

- adding a new watcher
- changing fingerprint logic
- changing incident lifecycle semantics
- modifying rule engine matching behavior

---

## Tips For Debugging

When you are trying to understand behavior:

1. start from `cmd/main.go`
2. find which controller or watcher owns the behavior
3. trace the event into `Consumer.handleEvent`
4. check whether `Reporter.EnsureSignal` created, updated, or reopened an incident
5. inspect the resulting `IncidentReport`

Useful commands:

```bash
kubectl get rcaagent -A
kubectl get rcacorrelationrules
kubectl get incidentreports -A
kubectl describe incidentreport <name> -n <namespace>
make test
```

For local development setup and test commands, see:

- `docs/development/local-setup.md`
- `docs/development/testing.md`

---

## Final Mental Model

If you remember only one thing from this guide, remember this:

RCA Operator is not "a bunch of watchers that directly create alerts."

It is a pipeline:

1. `RCAAgent` configures runtime behavior
2. watchers produce typed signals
3. the incident engine processes and correlates them
4. the reporter writes durable `IncidentReport` state
5. the incident controller manages lifecycle
6. dashboard and notifications read that durable state

Once that model clicks, the rest of the codebase becomes much easier to navigate.
