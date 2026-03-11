<div align="center">

# 🔍 RCA Operator — Phase 1 Plan
### Foundation MVP · Scope, Delivery & Definition of Done

[![Phase](https://img.shields.io/badge/Phase-1%20%E2%80%94%20Foundation-blue)](.)
[![Status](https://img.shields.io/badge/Status-Planning-yellow)](.)
[![Timeline](https://img.shields.io/badge/Timeline-2--3%20Weeks-green)](.)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://golang.org)

</div>

---

## What Is This Document?

This is the official scope document for **Phase 1 of the RCA Operator**. It defines exactly what will be built, what won't, how delivery is broken down week by week, and what "done" looks like.

> **Rule:** If it isn't in this document, it isn't Phase 1. Scope creep is the enemy of shipping.

---

## Table of Contents

- [Objective](#objective)
- [What's In Scope](#whats-in-scope)
- [Explicitly Out of Scope](#explicitly-out-of-scope)
- [CRD Definitions](#crd-definitions-phase-1-subset)
- [Delivery Breakdown](#delivery-breakdown)
- [File Deliverables](#file-deliverables)
- [Definition of Done](#definition-of-done)
- [Risks & Mitigations](#risks--mitigations)
- [GitHub Issues](#github-issues)
- [Incident Flow (End-to-End)](#incident-flow-end-to-end)
- [IncidentReport CR Lifecycle](#incidentreport-customresource-lifecycle)

---

## Objective

Phase 1 is about shipping a **real, deployable operator** — not a prototype. The goal is the minimum vertical slice that delivers immediate value:

> A Kubernetes operator that runs in-cluster, watches pods and events, correlates signals into meaningful incidents, and notifies the team via Slack and PagerDuty.

Everything else — LLM analysis, remediation playbooks, autonomy levels, dashboards — comes in later phases. Phase 1 plants the flag.

---

## What's In Scope

### 2.1 Core Operator Scaffolding

- kubebuilder-generated project structure with Go module, Makefile, and CI
- `RCAAgent` CRD — the main configuration resource
- `IncidentReport` CRD — auto-created per detected incident
- Agent controller reconciliation loop
- Helm chart for one-command install
- Namespace-scoped and cluster-scoped watch modes

### 2.2 Watcher Layer (Read-Only)

**Pod Watcher**
- CrashLoopBackOff detection (restart count threshold)
- OOMKilled detection (exit code 137)
- ImagePullBackOff detection
- Pod pending too long
- Exit code intelligence (classify and map all common termination exit codes)
- Grace period violation detection (simplified):
  - Track pod deletions using `DeletionTimestamp`
  - Compare runtime against `DeletionGracePeriodSeconds`
  - If containers remain running after grace period, raise `Grace Period Violation`

> CrashLoopBackOff detection is threshold-based, not fixed-time based.
> Current implementation emits when pod state is `CrashLoopBackOff` and container restart count reaches the configured threshold (default: 3 restarts).
> Detection wall-clock latency depends on the app crash cycle and kubelet backoff behavior.

**Event Watcher**
- Watch `core/v1` Event stream across watched namespaces
- Deduplicate repeated events (ring-buffer + time window)

**Node Watcher**
- Node `NotReady` condition
- Node `DiskPressure` / `MemoryPressure` conditions

**Deployment Watcher**
- Track recent deployments (timestamp + revision)
- Detect stalled rollouts

**Correlation Additions (Phase 1)**
- CPU throttling correlation engine using Kubernetes metrics/events already available to the operator
- Exit-code-aware incident enrichment (not limited to code `137`)

**Estimated Effort for Added Scope:** 3-4 days

### 2.3 Correlator & Triage Engine

- Internal event ring buffer (in-memory, configurable window)
- Rule engine — **8 built-in correlation rules** for revised Phase 1:

| # | Rule | Incident Type |
|---|------|--------------|
| 1 | CrashLoop + OOMKilled | Memory pressure incident |
| 2 | CrashLoop + recent deployment | Bad deploy incident |
| 3 | Multiple pods failing on same node | Node-level incident |
| 4 | ImagePullBackOff + no prior pull success | Registry / credentials incident |
| 5 | Node NotReady + eviction events | Node failure incident |
| 6 | CPU throttling + probe failures/restarts | Resource saturation incident |
| 7 | Non-zero exit code patterns (mapped by category) | Exit-code intelligence incident |
| 8 | Deletion grace period exceeded + forced termination signal | Grace period violation incident |

- Incident deduplication — suppress repeat fires within cool-down window
- Severity scoring: `P1` (cluster) / `P2` (namespace) / `P3` (single service) / `P4` (warning)

### 2.4 Incident Lifecycle Management

- Incident struct with: ID, severity, affectedResources, timeline, correlatedSignals, status
- Status transitions: `Detecting` → `Active` → `Resolved`
- Auto-resolve detection (pod healthy again for N minutes)
- `IncidentReport` CR written to namespace on creation and updated on resolution

CrashLoop resolve semantics in current implementation:

- CrashLoop incidents are resolved from a pod healthy signal, not directly from disappearance of `CrashLoopBackOff` state.
- Healthy signal is emitted when pod is `Running` + `Ready` for a stability window (default: 60s).
- Ready scan runs every 30s, so practical resolve latency is usually about 60-90s after pod becomes healthy.

### 2.5 Notification Layer

**Slack**
- Incident open message: severity badge, affected resources, detected signals
- Incident resolved message with duration
- Configurable channel per agent
- Optional `@mention` on P1

**PagerDuty**
- Trigger alert on P1/P2 incidents
- Resolve alert when incident auto-resolves
- Attach incident summary as alert detail

**Kubernetes Native**
- Emit K8s events on the `IncidentReport` CR for `kubectl` visibility
- Credentials via Kubernetes Secrets (never env vars)

### 2.6 Observability & Health

- Operator health endpoints (`/healthz`, `/readyz`)
- Basic Prometheus metrics:
  - `rca_incidents_detected_total`
  - `rca_incidents_resolved_total`
  - `rca_watcher_events_processed_total`
- Structured JSON logging with incident ID correlation field

---

## Explicitly Out of Scope

The following are planned for later phases. **Do not build them now.**

| Feature | Phase | Reason Deferred |
|---|---|---|
| AI / LLM RCA (OpenAI, Claude, Ollama) | Phase 2 | Adds API cost + complexity before core pipeline is proven |
| NetworkPolicy impact analyzer | Phase 2 | Requires CNI-specific behavior handling and deeper networking integration than Phase 1 allows |
| Rule-based log analysis (pattern matching) | Phase 2 | Needs evidence gatherer first |
| Autonomous remediation (rollback, scale) | Phase 3 | Requires human trust before autonomous action |
| Autonomy level engine (0–3) | Phase 3 | No remediation in Phase 1 |
| Full DSADS (eBPF-driven) | Phase 3-4 | Requires eBPF infrastructure and deeper kernel-level telemetry not suitable for Phase 1 timeline |
| Auto post-mortem generation | Phase 4 | Needs AI + full incident history |
| Grafana dashboard provisioning | Phase 4 | Nice-to-have, not core value |
| Email notifications | Phase 2 | Slack + PagerDuty sufficient for MVP |
| Multi-cluster support | Phase 5 / v1.0 | Significantly increases scope |
| Web UI for incident history | Phase 5 / v1.0 | API-first; UI is a later concern |
| Custom runbook engine (ConfigMap) | Phase 3 | Runbooks have no executor in Phase 1 |
| SLO / error budget tracking | Phase 4 | Requires metric history |

---

## CRD Definitions (Phase 1 Subset)

> Only these fields will be implemented. The full spec in the main README is the target for later phases.

### RCAAgent — Phase 1 Fields

```yaml
apiVersion: rca-operator.io/v1alpha1
kind: RCAAgent
metadata:
  name: rca-agent
  namespace: rca-operator-system
spec:
  watchNamespaces:
    - production
    - staging
  aiProviderConfig:             # stub only in Phase 1 — stored, not used
    type: openai
    model: gpt-4o
    secretRef: ai-api-key
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
    pagerduty:
      secretRef: pd-api-key
      severity: P2         # minimum severity to page
  incidentRetention: 30d
```

| Field | Type | Description |
|---|---|---|
| `spec.watchNamespaces` | `[]string` | Namespaces to watch. Empty = all namespaces. |
| `spec.aiProviderConfig` | `object` | Stub in Phase 1 — type and secretRef stored, not used yet. |
| `spec.notifications.slack.webhookSecretRef` | `string` | Secret name containing the Slack webhook URL. |
| `spec.notifications.slack.channel` | `string` | Target Slack channel (e.g. `#incidents`). |
| `spec.notifications.slack.mentionOnP1` | `string` | User or group to mention on P1 incidents. |
| `spec.notifications.pagerduty.secretRef` | `string` | Secret name with PagerDuty Events API v2 key. |
| `spec.notifications.pagerduty.severity` | `string` | Minimum severity to page (`P1` or `P2`). |
| `spec.incidentRetention` | `string` | How long to keep Resolved IncidentReport CRs. Format: `5m`, `2h`, `30d`. Default: `30d`. |

### IncidentReport — Phase 1 Status Fields

```yaml
status:
  severity: P2
  phase: Active                  # Detecting | Active | Resolved
  incidentType: OOM              # CrashLoop | OOM | BadDeploy | NodeFailure | Registry
  startTime: "2025-01-15T10:32:00Z"
  resolvedTime: ""
  notified: true
  affectedResources:
    - kind: Deployment
      name: payment-service
      namespace: production
  correlatedSignals:
    - "CrashLoopBackOff (restarts: 8)"
    - "OOMKilled (exit code 137)"
    - "Deployment at 10:28 UTC (revision 15)"
  timeline:
    - time: "10:32:00"
      event: "Pod payment-service-xxx entered CrashLoopBackOff"
    - time: "10:33:00"
      event: "OOMKilled event correlated"
  rootCause: ""                  # stub — populated by RCA engine in Phase 2
```

| Field | Type | Validation / Notes |
|---|---|---|
| `status.severity` | `string` | Enum: `P1` `P2` `P3` `P4` |
| `status.phase` | `string` | Enum: `Detecting` \| `Active` \| `Resolved` |
| `status.incidentType` | `string` | Enum: `CrashLoop` \| `OOM` \| `BadDeploy` \| `NodeFailure` \| `Registry` |
| `status.startTime` | `*metav1.Time` | RFC3339 timestamp — set when incident is first detected |
| `status.resolvedTime` | `*metav1.Time` | RFC3339 timestamp — empty while still active |
| `status.notified` | `bool` | Dedup gate — set to `true` after first notification fires; prevents duplicate alerts |
| `status.affectedResources` | `[]AffectedResource` | `kind`, `name`, `namespace` of each impacted resource (`+listType=atomic`) |
| `status.correlatedSignals` | `[]string` | Raw signal strings from the correlator (`+listType=atomic`) |
| `status.timeline` | `[]TimelineEvent` | Ordered `{time, event}` entries (`+listType=atomic`) |
| `status.rootCause` | `string` | Stub in Phase 1 — populated by the RCA engine in Phase 2 |
| `status.conditions` | `[]metav1.Condition` | Standard Kubernetes status conditions (`+listType=map`) |

#### About `status.conditions`

`status.conditions` is the standard Kubernetes pattern for communicating detailed machine-readable state on any CR. It is the same mechanism used by Deployments, Pods, Nodes, etc.

**What a single condition looks like:**

```yaml
status:
  conditions:
    - type: Available               # what aspect of the resource this describes
      status: "True"                # True | False | Unknown
      reason: IncidentActive        # CamelCase machine-readable reason code
      message: "P2 OOM incident is active, notifications dispatched"
      observedGeneration: 3         # matches metadata.generation — staleness check
      lastTransitionTime: "2026-03-05T10:32:00Z"
```

**The three condition types used in this operator:**

| `type` | `status: True` means | `status: False` means |
|---|---|---|
| `Available` | CR is healthy / fully operational | Something is wrong |
| `Progressing` | State is actively changing (e.g. detecting, resolving) | Not currently changing |
| `Degraded` | Something failed (secret missing, notification error) | Everything healthy |

> **Rule:** use `status.phase` (`Detecting|Active|Resolved`) for the business-level lifecycle. Use `status.conditions` for the operator's own health state — notification failures, validation errors, reconcile success. They serve different audiences: `phase` is for humans and dashboards; `conditions` are for automation and `kubectl describe`.

---

## Delivery Breakdown

### Week 1–2 — Scaffolding & CRDs

- [x] `kubebuilder init` + API group `rca-operator.io/v1alpha1`
- [x] Define `RCAAgent` CRD (Phase 1 fields only)
- [x] Define `IncidentReport` CRD
- [x] `make generate` + `make manifests` to produce CRD YAMLs
- [x] Basic `agent_controller.go` reconcile loop
- [x] `kind` cluster + `make install` smoke test
- [x] Helm chart skeleton (`Chart.yaml`, `templates/deployment.yaml`, `values.yaml`)

### Week 3 — Watcher Layer

- [ ] `pod_watcher.go` — CrashLoop / OOM / ImagePull / Pending detection
- [ ] Add exit code intelligence mapping for common termination codes
- [ ] Add simplified grace period violation detector (non-eBPF)
- [ ] `event_watcher.go` — `core/v1` Event stream, dedup buffer
- [ ] `node_watcher.go` — NotReady / DiskPressure / MemoryPressure
- [ ] `deployment_watcher.go` — recent deploy timestamp tracker
- [ ] Add CPU throttling correlation inputs (Kubernetes-native signals only)
- [ ] Shared event emitter interface → feeds Correlator channel
- [ ] Unit tests for each watcher (table-driven, mock K8s client)

### Week 4 — Correlator & Incident Manager

- [ ] `correlator.go` — ring buffer + 5 correlation rules
- [ ] `incident.go` — Incident struct, severity scoring, dedup logic
- [ ] `incident_controller.go` — reconcile loop for `IncidentReport` CRs
- [ ] Auto-resolve logic (health check poll)
- [ ] Unit tests for all 5 correlation rules

### Week 5 — Notifications

- [ ] `slack.go` — webhook client, open/resolve message templates
- [ ] `pagerduty.go` — Events API v2 trigger + resolve
- [ ] `cr_reporter.go` — create/patch `IncidentReport` CR from incident
- [ ] K8s event emission on `IncidentReport`
- [ ] Secret resolution from Kubernetes Secrets
- [ ] Integration test — fire fake incident, verify Slack message

### Week 6 — Helm, RBAC & Observability

- [ ] Full Helm chart — RBAC `ClusterRole`/`Binding`, `ServiceAccount`, `Deployment`, CRDs
- [ ] RBAC scoped to read-only on pods/events/nodes + write on CRDs only
- [ ] Prometheus metrics
- [ ] Structured JSON logger (`zap`) with incident ID field
- [ ] `/healthz` and `/readyz` endpoints

### Week 7–8 — E2E Tests, Docs & Release

- [ ] E2E test suite in `tests/e2e/` using `kind` + `envtest`
- [ ] E2E scenario: deploy broken pod → detect CrashLoop → `IncidentReport` CR created → Slack mock notified → pod fixed → incident resolved
- [ ] README Quick Start verified end-to-end
- [ ] `config/samples/` — working example `RCAAgent` CR
- [ ] GitHub release `v0.1.0` with: `crds.yaml`, `install.yaml`, Helm chart, Docker image
- [ ] `CHANGELOG.md` entry for v0.1

---

## File Deliverables

> ✅ Build = implemented this phase · 🔲 Stub = empty file/dir, implemented later

```
rca-operator/
│
├── cmd/
│   └── main.go                               ✅ Build
│
├── api/
│   └── v1alpha1/
│       ├── rcaagent_types.go                 ✅ Build  (Phase 1 fields only)
│       ├── incidentreport_types.go           ✅ Build  (Phase 1 status fields)
│       └── zz_generated.deepcopy.go          ✅ Build  (generated)
│
├── internal/
│   ├── watcher/
│   │   ├── pod_watcher.go                    ✅ Build
│   │   ├── event_watcher.go                  ✅ Build
│   │   ├── node_watcher.go                   ✅ Build
│   │   ├── deployment_watcher.go             ✅ Build
│   │   ├── metrics_watcher.go                🔲 Stub   (Phase 2)
│   │   └── log_watcher.go                    🔲 Stub   (Phase 2)
│   │
│   ├── correlator/
│   │   ├── correlator.go                     ✅ Build
│   │   ├── rules.go                          ✅ Build  (5 rules)
│   │   └── incident.go                       ✅ Build
│   │
│   ├── rca/
│   │   ├── engine.go                         🔲 Stub   (Phase 2)
│   │   ├── evidence_gatherer.go              🔲 Stub   (Phase 2)
│   │   ├── rule_analyzer.go                  🔲 Stub   (Phase 2)
│   │   └── ai_analyzer.go                    🔲 Stub   (Phase 2)
│   │
│   ├── remediation/
│   │   └── (directory only)                  🔲 Stub   (Phase 3)
│   │
│   ├── reporter/
│   │   ├── slack.go                          ✅ Build
│   │   ├── pagerduty.go                      ✅ Build
│   │   ├── email.go                          🔲 Stub   (Phase 2)
│   │   └── cr_reporter.go                    ✅ Build
│   │
│   └── controller/
│       ├── agent_controller.go               ✅ Build
│       └── incident_controller.go            ✅ Build
│
├── config/
│   ├── crd/                                  ✅ Build  (generated by make manifests)
│   ├── rbac/                                 ✅ Build
│   ├── manager/                              ✅ Build
│   └── samples/
│       └── rcaagent-sample.yaml              ✅ Build
│
├── charts/
│   └── rca-operator/                         ✅ Build
│
├── runbooks/                                 🔲 Stub   (Phase 3)
│
└── tests/
    ├── e2e/                                  ✅ Build  (1 scenario minimum)
    └── unit/                                 ✅ Build
```

---

## Definition of Done

Phase 1 is complete when **all** of the following pass without exception:

1. **Operator deploys cleanly** into a `kind` cluster with `helm install` in under 60 seconds.

2. **A pod in CrashLoopBackOff** causes an `IncidentReport` CR to be created in the watched namespace shortly after the crash loop threshold is reached (default: 3 restarts).

3. **A Slack message is sent** with severity badge, affected resource name, and incident type.

4. **Fixing the pod** causes the incident to auto-resolve and a Slack resolution message to fire.

5. **`kubectl get incidentreport -n <namespace>`** shows the incident with correct severity, type, and timeline entries.

6. **`make test`** passes all unit tests with >80% coverage on `correlator` and `watcher` packages.

7. **`make test-e2e`** passes the full CrashLoop E2E scenario.

8. **GitHub release `v0.1.0`** exists with `install.yaml`, `crds.yaml`, and Helm chart attached as release assets.

---

## Risks & Mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| **Event storm** — high-churn clusters produce thousands of events/sec, overwhelming the ring buffer | Medium | Configurable buffer size + per-namespace rate limiting. Overflow drops oldest events, never blocks watcher goroutines. |
| **Notification spam** — same incident fires Slack on every reconcile loop | High | Dedup flag on `IncidentReport` `status.notified` — notify once on open, once on resolve. Cool-down window in correlator. |
| **RBAC too broad** — some teams reject cluster-wide read access | Low | Document how to restrict to specific namespaces via `RoleBinding` instead of `ClusterRoleBinding`. |
| **Scope creep** — pressure to add AI or remediation in Phase 1 | High | This document is the source of truth. Stub files exist to show the plan, not the implementation. |
| **CRD versioning debt** — `v1alpha1` types become hard to evolve | Low | Accepted for v0.1. Plan a `v1beta1` migration path before v0.3. |

---

## GitHub Issues

Tracked under the milestone **[`v0.1 — Foundation`](https://github.com/gaurangkudale/RCA-Operator/milestone/1)**. Each maps to one discrete, reviewable PR.

| # | Issue | Label | Week |
|---|---|---|---|
| 1 | [#3 Init kubebuilder project + Go module](https://github.com/gaurangkudale/RCA-Operator/issues/3) | `setup` | 1 |
| 2 | [#4 Define RCAAgent CRD (Phase 1 fields)](https://github.com/gaurangkudale/RCA-Operator/issues/4) | `api` | 1 |
| 3 | [#5 Define IncidentReport CRD](https://github.com/gaurangkudale/RCA-Operator/issues/5) | `api` | 1 |
| 4 | [#6 Helm chart skeleton + values.yaml](https://github.com/gaurangkudale/RCA-Operator/issues/6) | `helm` | 1–2 |
| 5 | [#7 Implement pod_watcher.go](https://github.com/gaurangkudale/RCA-Operator/issues/7) | `watcher` | 3 |
| 6 | [#8 Implement event_watcher.go + dedup buffer](https://github.com/gaurangkudale/RCA-Operator/issues/8) | `watcher` | 3 |
| 7 | [#9 Implement node_watcher.go](https://github.com/gaurangkudale/RCA-Operator/issues/9) | `watcher` | 3 |
| 8 | [#10 Implement deployment_watcher.go](https://github.com/gaurangkudale/RCA-Operator/issues/10) | `watcher` | 3 |
| 9 | [#11 Implement correlator.go + 5 correlation rules](https://github.com/gaurangkudale/RCA-Operator/issues/11) | `correlator` | 4 |
| 10 | [#12 Implement incident lifecycle + auto-resolve](https://github.com/gaurangkudale/RCA-Operator/issues/12) | `correlator` | 4 |
| 11 | [#13 Implement slack.go notification](https://github.com/gaurangkudale/RCA-Operator/issues/13) | `reporter` | 5 |
| 12 | [#14 Implement pagerduty.go notification](https://github.com/gaurangkudale/RCA-Operator/issues/14) | `reporter` | 5 |
| 13 | [#15 Implement cr_reporter.go (IncidentReport CR writer)](https://github.com/gaurangkudale/RCA-Operator/issues/15) | `reporter` | 5 |
| 14 | [#16 RBAC + Helm chart finalization](https://github.com/gaurangkudale/RCA-Operator/issues/16) | `helm, rbac` | 6 |
| 15 | [#17 Prometheus metrics + health endpoints](https://github.com/gaurangkudale/RCA-Operator/issues/17) | `observability` | 6 |
| 16 | [#18 E2E test: CrashLoop scenario](https://github.com/gaurangkudale/RCA-Operator/issues/18) | `test` | 7 |
| 17 | [#19 README Quick Start verification + samples/](https://github.com/gaurangkudale/RCA-Operator/issues/19) | `docs` | 7–8 |
| 18 | [#20 GitHub release v0.1.0 + Docker image push](https://github.com/gaurangkudale/RCA-Operator/issues/20) | `release` | 8 |

---

## Incident Flow (End-to-End)

Concrete walkthrough of how Phase 1 handles the most common scenario:

```
[1]  Pod enters CrashLoopBackOff (restarts > threshold)
      │
[2]  pod_watcher.go emits CrashLoopEvent{pod, namespace, restartCount}
      │
[3]  event_watcher.go detects OOMKilled event on same pod within 5-min window
      → emits OOMEvent{pod, namespace}
      │
[4]  deployment_watcher.go has recorded a deploy to same namespace at T-4min
      → emits RecentDeployEvent{deployment, revision}
      │
[5]  correlator.go — Rule 1 fires (CrashLoop + OOM)
      → creates Incident{severity:P2, type:OOM, signals:[CrashLoop, OOMKilled, RecentDeploy]}
      │
[6]  Incident dedup check passes
      → IncidentReport CR created with status.phase=Active
      │
[7]  cr_reporter.go writes IncidentReport to cluster
      → K8s event emitted on the CR
      │
[8]  slack.go sends:
      "🔴 P2 | OOM Incident | payment-service
       Signals: CrashLoop (restarts: 8), OOMKilled, RecentDeploy (10:28 UTC)"
      │
[9]  pagerduty.go triggers PD alert with incident ID + summary
      │
[10] Pod returns to Running state
      → pod_watcher.go emits PodHealthyEvent
      │
[11] Incident auto-resolve: IncidentReport patched → status.phase=Resolved
      │
[12] slack.go sends resolve message. PD alert closed.
```

---

---

## IncidentReport CustomResource Lifecycle

This section defines exactly **who creates the CR, when, and how its fields transition** from detection through resolution.

### Who Creates It

The `IncidentReport` CR is **never created by a human**. It is always written by the operator:

| Component | Responsibility |
|---|---|
| `correlator.go` | Fires a correlation rule → emits an in-memory `Incident` struct |
| `cr_reporter.go` | Receives the `Incident` → creates or patches the `IncidentReport` CR |
| `incidentreport_controller.go` | Reconciles the CR → dispatches notifications, manages `status.notified` |
| `pod_watcher.go` | Detects healthy pod → triggers auto-resolve path |

### Phase Transitions

```
          ┌─────────────┐
          │  Detecting  │  ← correlator sees first signal, rule not yet fully matched
          └──────┬──────┘
                 │  all rule conditions met within correlation window
                 ▼
          ┌─────────────┐
          │   Active    │  ← CR created; Slack + PagerDuty fired; status.notified=true
          └──────┬──────┘
                 │  pod/node healthy for N consecutive minutes (auto-resolve)
                 ▼
          ┌─────────────┐
          │  Resolved   │  ← CR patched; resolvedTime set; Slack resolve + PD close sent
          └─────────────┘
```

### CR Creation — Step by Step

**Step 1 — Correlator fires a rule**
```
correlator.go detects: CrashLoop + OOMKilled on same pod within 5-min window
→ produces: Incident{
    severity:          P2,
    incidentType:      OOM,
    affectedResources: [{kind:Deployment, name:payment-service, ns:production}],
    correlatedSignals: ["CrashLoopBackOff (restarts: 8)", "OOMKilled (exit 137)"],
    timeline:          [{time:T+0, event:"Pod entered CrashLoopBackOff"},
                        {time:T+1m, event:"OOMKilled correlated"}],
  }
```

**Step 2 — Dedup check (in-memory)**
```
correlator checks: is there already an Active IncidentReport for this
                   (namespace + incidentType + affectedResource) tuple?
  YES → drop silently (cool-down window not expired)
  NO  → pass Incident to cr_reporter.go
```

**Step 3 — cr_reporter.go creates the CR**
```yaml
apiVersion: rca.rca-operator.io/v1alpha1
kind: IncidentReport
metadata:
  generateName: oom-payment-service-   # deterministic prefix
  namespace: production
  labels:
    rca.rca-operator.io/agent:         rcaagent-sample
    rca.rca-operator.io/severity:      P2
    rca.rca-operator.io/incident-type: OOM
spec:
  agentRef: rcaagent-sample
# status written immediately via status subresource:
status:
  severity:      P2
  phase:         Active
  incidentType:  OOM
  startTime:     "RFC3339"
  notified:      false          # will become true after notification fires
  affectedResources:
    - kind: Deployment
      name: payment-service
      namespace: production
  correlatedSignals:
    - "CrashLoopBackOff (restarts: 8)"
    - "OOMKilled (exit code 137)"
  timeline:
    - time: "RFC3339"
      event: "Pod payment-service-xxx entered CrashLoopBackOff"
    - time: "RFC3339"
      event: "OOMKilled event correlated"
  rootCause: ""                 # stub — Phase 2
```

**Step 4 — IncidentReport controller reconciles**
```
incidentreport_controller.go picks up the new CR
→ status.notified == false → send notifications:
    slack.go  → post open-incident message to #incidents
    pagerduty.go → trigger PD alert (severity >= spec.notifications.pagerduty.severity)
→ patch status.notified = true
→ emit K8s Event on the IncidentReport CR (visible in kubectl describe)
```

**Step 5 — Auto-resolve**
```
pod_watcher.go: pod Running + Ready for N consecutive minutes
→ notifies correlator → correlator marks Incident resolved
→ cr_reporter.go patches IncidentReport:
    status.phase        = Resolved
    status.resolvedTime = <now>
→ incidentreport_controller.go reconciles:
    slack.go     → post resolve message with duration
    pagerduty.go → close PD alert
```

### Field Population Responsibility

| Field | Set by | When |
|---|---|---|
| `status.severity` | `cr_reporter.go` | On creation |
| `status.phase` | `cr_reporter.go` | On creation (`Active`) and resolution (`Resolved`) |
| `status.incidentType` | `cr_reporter.go` | On creation |
| `status.startTime` | `cr_reporter.go` | On creation |
| `status.resolvedTime` | `cr_reporter.go` | On auto-resolve |
| `status.affectedResources` | `cr_reporter.go` | On creation |
| `status.correlatedSignals` | `cr_reporter.go` | On creation |
| `status.timeline` | `cr_reporter.go` | On creation; appended on resolve |
| `status.notified` | `incidentreport_controller.go` | After notifications are dispatched |
| `status.rootCause` | _RCA engine (Phase 2)_ | Not set in Phase 1 |
| `status.conditions` | `incidentreport_controller.go` | On each reconcile |

### Key Invariants

- **One CR per active incident** — the correlator dedup key is `(namespace, incidentType, primaryResource)`. Duplicate signals within the cool-down window are dropped, never create a second CR.
- **Notify exactly once** — `status.notified=true` gates notification dispatch. The controller checks this flag before sending; it will never resend even if it reconciles multiple times.
- **CR is the source of truth** — the in-memory `Incident` struct is transient. If the operator restarts, it reconstructs state from existing `IncidentReport` CRs with `phase != Resolved`.
- **Status is operator-owned** — users must never edit `status` directly. `spec.agentRef` is the only user-facing field.

---

<div align="center">

**Phase 1 is the foundation everything else is built on. Ship it lean, ship it right.**

*Part of the [RCA Operator](../README.md) project*

</div>
