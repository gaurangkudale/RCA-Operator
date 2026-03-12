# Architecture

---

## Core Philosophy

```
Traditional SRE:  Alert → Human → Investigate → Fix → Post-mortem
RCA SRE:          Alert → Detect → Correlate → RCA → Fix → Report  (autonomous)
```

---

## High-Level System Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                              │
│                                                                         │
│  ┌────────────────────────────────────────────────────────────────-─┐   │
│  │                     RCA SRE Operator                             │   │
│  │                                                                  │   │
│  │  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐   │   │
│  │  │   Watcher   │  │  Correlator  │  │     RCA Engine         │   │   │
│  │  │   Layer     │─►│   & Triage   │─►│  (Rules + AI/LLM)      │   │   │
│  │  └─────────────┘  └──────────────┘  └──────────┬─────────────┘   │   │
│  │                                                │                 │   │
│  │  ┌─────────────┐  ┌──────────────┐  ┌──────────▼─────────────┐   │   │
│  │  │  Remediation│◄─│  Decision    │◄─│   Incident Manager     │   │   │
│  │  │  Engine     │  │  Engine      │  │                        │   │   │
│  │  └─────────────┘  └──────────────┘  └────────────────────────┘   │   │
│  │                                                                  │   │
│  │  ┌──────────────────────────────────────────────────────────┐    │   │
│  │  │           Reporting & Notification Layer                 │    │   │
│  │  │    Slack · PagerDuty · Email · Webhooks · K8s Events     │    │   │
│  │  └──────────────────────────────────────────────────────────┘    │   │
│  └─────────────────────────────────────────────────────────────────-┘   │
│                                                                         │
│   Watched Resources:                                                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────┐     │
│  │  Pods    │ │ Services │ │  Nodes   │ │  Events  │ │ Deployments│     │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └────────────┘     │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## How an Incident Flows

```
[1]  Pod enters CrashLoopBackOff
[2]  Watcher detects event stream anomaly
[3]  Correlator links: CrashLoop + OOMKilled + recent deployment at T-4min
[4]  Incident created with severity P2
[5]  Evidence Gatherer pulls logs, describe output, metrics, deploy history
[6]  Rule Analyzer: "OOM after deploy" → 80% confidence
     AI Analyzer:   reads logs → "heap not freed in request handler" → 94% confidence
[7]  RCA Report generated with timeline and blast radius
[8]  Decision Engine: autonomy level 2 → safe to auto-rollback
[9]  Remediation: rollback deployment + annotate resource
[10] Slack: "🔴 P2 Incident | RCA: Memory leak in v2.3.1 | Auto-rolled back ✅"
[11] IncidentReport CR created in namespace
[12] Post-mortem draft generated and sent to team
```

---

## Layer Responsibilities

### Layer 1 — Watcher Layer

The observation engine. Registers controller-runtime informers against the Kubernetes API server, detects failure signals in real time, and emits typed events to the correlator channel.

**Phase 1 implementation:** `PodWatcher` — detects CrashLoopBackOff, OOMKilled, ImagePullBackOff, ContainerExitCode, PodPendingTooLong, GracePeriodViolation, PodHealthy, PodDeleted.

→ See [reference/watcher.md](../reference/watcher.md) for the full event catalog.

### Layer 2 — Correlator & Triage

Consumes the watcher event channel, deduplicates signals, groups related events into a single `IncidentReport`, and assigns severity (P1–P4).

**Correlation rules (examples):**
- Pod CrashLoop + high memory + OOM event → "Memory Leak"
- Multiple pods down + node NotReady → "Node Failure cascading to pods"
- 5xx spike + recent deployment + config change → "Bad deployment"

### Layer 3 — RCA Engine *(Phase 2+)*

Performs deep root cause analysis. An **Evidence Gatherer** collects logs, metrics, and describe output; a **Rule Analyzer** matches known patterns; an **AI/LLM Analyzer** provides natural-language diagnosis and confidence scoring.

### Layer 4 — Decision Engine *(Phase 2+)*

Enforces the configured autonomy level (0–3) before passing actions to the Remediation Engine. See [reference/rcaagent-crd.md](../reference/rcaagent-crd.md#autonomy-levels) for the level definitions.

### Layer 5 — Remediation Engine *(Phase 3+)*

Executes approved actions: pod restarts, deployment rollbacks, scaling, node cordoning. Each action type is implemented as a discrete playbook.

### Layer 6 — Reporting & Notification

Posts incident summaries to Slack / PagerDuty and persists the full timeline as an `IncidentReport` CR in the affected namespace.

---

## Source Layout

```
cmd/main.go                    Manager entry point
api/v1alpha1/                  CRD types (RCAAgent, IncidentReport)
internal/
  watcher/                     Pod informer + event types
  correlator/                  Signal correlation + incident creation
  controller/                  Reconciliation logic for both CRDs
  retention/                   Periodic cleanup of resolved IncidentReports
config/
  crd/bases/                   Generated CRDs (do not edit)
  rbac/                        Generated RBAC (do not edit)
  samples/                     Minimal example CRs
test/fixtures/                 Local scenario fixtures for manual testing
```

---

## Detailed Component Architecture

### Layer 1 — Watcher Layer (Eyes of the SRE)

This is the data collection brain. It watches everything happening in the cluster in real time.

```
┌──────────────────────────────────────────────────────────────┐
│                        Watcher Layer                         │
│                                                              │
│  ┌─────────────────┐   ┌──────────────────┐                  │
│  │  K8s Event      │   │  Metrics Watcher  │                 │
│  │  Watcher        │   │  (cAdvisor/HPA)   │                 │
│  │                 │   │                   │                 │
│  │ - Pod OOMKilled │   │ - CPU spikes      │                 │
│  │ - CrashLoop     │   │ - Memory pressure │                 │
│  │ - BackoffFailed │   │ - Throttling      │                 │
│  │ - NodePressure  │   │ - Network I/O     │                 │
│  └────────┬────────┘   └─────────┬─────────┘                 │
│           │                      │                           │
│  ┌────────▼────────┐   ┌─────────▼────────┐                  │
│  │  Log Watcher    │   │  Endpoint/Service │                 │
│  │                 │   │  Health Watcher   │                 │
│  │ - Error patterns│   │                   │                 │
│  │ - Stack traces  │   │ - 5xx rates       │                 │
│  │ - Panic logs    │   │ - Latency spikes  │                 │
│  │ - OOM messages  │   │ - DNS failures    │                 │
│  └────────┬────────┘   └─────────┬─────────┘                 │
│           │                      │                           │
│           └──────────┬───────────┘                           │
│                      ▼                                       │
│              ┌───────────────┐                               │
│              │  Event Stream │  (internal ring buffer)       │
│              └───────────────┘                               │
└──────────────────────────────────────────────────────────────┘
```

What it watches via Kubernetes APIs:

```go
// Watches in parallel using controller-runtime
- core/v1:       Pods, Events, Nodes, Endpoints, PersistentVolumes
- apps/v1:       Deployments, StatefulSets, ReplicaSets, DaemonSets
- networking/v1: Ingress, NetworkPolicy
- metrics.k8s.io: PodMetrics, NodeMetrics
- custom metrics: Prometheus scraping
```

---

### Layer 2 — Correlator & Triage Engine

Takes raw events and correlates them into **meaningful incidents** — not just noise.

```
┌──────────────────────────────────────────────────────────────┐
│                    Correlator Engine                         │
│                                                              │
│   Raw Events ──► Deduplication ──► Correlation ──► Incident  │
│                                                              │
│  Correlation Rules:                                          │
│  ┌──────────────────────────────────────────────────────┐    │
│  │ IF pod CrashLoop + high memory + OOM event           │    │
│  │   → Incident: "Memory Leak in pod X"                 │    │
│  │                                                      │    │
│  │ IF multiple pods down + node NotReady                │    │
│  │   → Incident: "Node Failure cascading to pods"       │    │
│  │                                                      │    │
│  │ IF 5xx spike + recent deployment + config change     │    │
│  │   → Incident: "Bad deployment causing errors"        │    │
│  │                                                      │    │
│  │ IF PVC pending + storageClass missing                │    │
│  │   → Incident: "Storage provisioning failure"         │    │
│  └──────────────────────────────────────────────────────┘    │
│                                                              │
│  Severity Scoring:                                           │
│    P1 (Critical) → cluster-wide impact                       │
│    P2 (High)     → namespace-wide impact                     │
│    P3 (Medium)   → single service degraded                   │
│    P4 (Low)      → warning, no current impact                │
└──────────────────────────────────────────────────────────────┘
```

---

### Layer 3 — RCA Engine (The Brain)

This is the most powerful part. It performs deep root cause analysis using a combination of rules + AI.

```
┌──────────────────────────────────────────────────────────────┐
│                       RCA Engine                             │
│                                                              │
│   Incident                                                   │
│      │                                                       │
│      ▼                                                       │
│  ┌────────────────────┐                                      │
│  │  Evidence Gatherer │  ← pulls logs, metrics, events,      │
│  │                    │    k8s describe, recent changes      │
│  └────────┬───────────┘                                      │
│           │                                                  │
│           ▼                                                  │
│  ┌────────────────────┐   ┌─────────────────────────────┐    │
│  │  Rule-Based        │   │  AI/LLM Analysis            │    │
│  │  Analyzer          │   │  (Claude/OpenAI/local LLM)  │    │
│  │                    │   │                             │    │
│  │ - Known patterns   │   │ - Analyze logs + events     │    │
│  │ - Runbook matching │   │ - Correlate timeline        │    │
│  │ - SLO breach calc  │   │ - Suggest root cause        │    │
│  │ - Dependency graph │   │ - Propose remediation       │    │
│  └────────┬───────────┘   └──────────────┬──────────────┘    │
│           │                              │                   │
│           └──────────────┬───────────────┘                   │
│                          ▼                                   │
│               ┌──────────────────┐                           │
│               │   RCA Report     │                           │
│               │                  │                           │
│               │ - Root Cause     │                           │
│               │ - Timeline       │                           │
│               │ - Blast Radius   │                           │
│               │ - Fix Options    │                           │
│               │ - Confidence %   │                           │
│               └──────────────────┘                           │
└──────────────────────────────────────────────────────────────┘
```

---

### Layer 4 — Decision Engine (Autonomy Level Control)

Critical design — how much should the operator act on its own?

```
┌──────────────────────────────────────────────────────────────┐
│                    Decision Engine                           │
│                                                              │
│   Autonomy Levels (configurable per namespace/incident type) │
│                                                              │
│   Level 0: OBSERVE                                           │
│   └── Only report, never act. Pure monitoring mode.          │
│                                                              │
│   Level 1: SUGGEST                                           │
│   └── Send RCA + recommended fix to Slack/PagerDuty.         │
│       Human approves.                                        │
│                                                              │
│   Level 2: SEMI-AUTO                                         │
│   └── Auto-fix safe actions (restart pod, scale up).         │
│       Alert human for risky actions (rollback, delete).      │
│                                                              │
│   Level 3: FULL-AUTO                                         │
│   └── Execute all remediations autonomously.                 │
│       Post-incident report sent after.                       │
│                                                              │
│   Example config:                                            │
│   ┌──────────────────────────────────────────────────────┐   │
│   │  production:  level: 1  (suggest only)               │   │
│   │  staging:     level: 2  (semi-auto)                  │   │
│   │  dev:         level: 3  (full-auto)                  │   │
│   └──────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

---

### Layer 5 — Remediation Engine

Pre-built remediation playbooks the operator can execute:

```
┌──────────────────────────────────────────────────────────────┐
│                   Remediation Playbooks                      │
│                                                              │
│  CrashLoopBackOff                                            │
│  ├── Capture logs before restart                             │
│  ├── Check if OOMKilled → increase memory limit              │
│  ├── Check if config error → alert + block rollout           │
│  └── If > N restarts → cordon + notify                       │
│                                                              │
│  High CPU / Throttling                                       │
│  ├── Scale horizontally (increase replicas)                  │
│  ├── Raise CPU limit temporarily                             │
│  └── Flag for right-sizing review                            │
│                                                              │
│  Node Pressure / NotReady                                    │
│  ├── Cordon node                                             │
│  ├── Drain non-critical pods                                 │
│  ├── Trigger replacement node (if cloud provider API)        │
│  └── Notify on-call                                          │
│                                                              │
│  Bad Deployment (5xx spike post-deploy)                      │
│  ├── Detect deployment timestamp correlation                 │
│  ├── Pause rollout                                           │
│  ├── Auto-rollback to last good revision                     │
│  └── Send post-mortem draft                                  │
│                                                              │
│  PVC / Storage Issues                                        │
│  ├── Detect pending PVCs                                     │
│  ├── Check StorageClass availability                         │
│  └── Alert + suggest fix                                     │
└──────────────────────────────────────────────────────────────┘
```

---

### Layer 6 — Reporting & Notification

```
┌──────────────────────────────────────────────────────────────┐
│                  Reporting Layer                             │
│                                                              │
│  Real-time Channels:                                         │
│  ├── Slack     → incident start, RCA, resolution             │
│  ├── PagerDuty → P1/P2 alerts with runbook link              │
│  ├── Email     → post-incident summary                       │
│  └── Webhook   → any custom endpoint                         │
│                                                              │
│  Kubernetes Native:                                          │
│  ├── K8s Events    → visible via kubectl get events          │
│  ├── CRD Status    → IncidentReport CR created per incident  │
│  └── Annotations   → stamped on affected resources           │
│                                                              │
│  Post-Incident Reports (auto-generated):                     │
│  ├── Timeline of events                                      │
│  ├── Root cause identified                                   │
│  ├── Impact duration + blast radius                          │
│  ├── Actions taken (automated + manual)                      │
│  └── Recommendations to prevent recurrence                   │
└──────────────────────────────────────────────────────────────┘
```

---

## CRD Design

### 1. RCAAgent — The SRE Agent Config

```yaml
apiVersion: RCA.io/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: RCA-system
spec:
  watchNamespaces:
    - production
    - staging
  aiProviderConfig:
    type: openai                    # or anthropic, ollama (local)
    model: gpt-4o
    secretRef: ai-api-key
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
    pagerduty:
      secretRef: pd-key
      severity: P2
  sloConfig:
    errorBudget: 99.9
    latencyP99: 500ms
  runbooks:
    configMapRef: RCA-runbooks
```

---

### 2. IncidentReport CR — Auto-created per incident

```yaml
apiVersion: RCA.io/v1alpha1
kind: IncidentReport
metadata:
  name: incident-2024-02-24-001
  namespace: production
spec: {}
status:
  severity: P2
  startTime: "2024-02-24T10:32:00Z"
  resolvedTime: "2024-02-24T10:45:00Z"
  affectedResources:
    - kind: Deployment
      name: payment-service
  rootCause: "OOMKilled due to memory leak in v2.3.1 — heap not released after request"
  timeline:
    - time: "10:32:00"
      event: "Pod payment-service-xxx restarted (CrashLoopBackOff)"
    - time: "10:33:00"
      event: "RCA detected OOMKilled pattern"
    - time: "10:34:00"
      event: "RCA correlated with recent deployment at 10:28"
    - time: "10:35:00"
      event: "Rollback triggered to v2.3.0"
    - time: "10:45:00"
      event: "Service healthy. Incident resolved."
  actionsT aken:
    - "Auto-rolled back Deployment to revision 14"
    - "Notified #incidents Slack channel"
  recommendations:
    - "Add memory profiling in CI pipeline"
    - "Set memory limit alerts at 80% threshold"
  confidence: 94
```

---

## Project File Structure

```
RCA-operator/
│
├── cmd/
│   └── main.go
│
├── api/
│   └── v1alpha1/
│       ├── RCAagent_types.go
│       ├── incidentreport_types.go
│       └── zz_generated.deepcopy.go
│
├── internal/
│   ├── watcher/
│   │   ├── pod_watcher.go
│   │   ├── node_watcher.go
│   │   ├── event_watcher.go
│   │   ├── metrics_watcher.go
│   │   └── log_watcher.go
│   │
│   ├── correlator/
│   │   ├── correlator.go
│   │   ├── rules.go
│   │   └── incident.go
│   │
│   ├── rca/
│   │   ├── engine.go
│   │   ├── evidence_gatherer.go
│   │   ├── rule_analyzer.go
│   │   └── ai_analyzer.go          ← LLM integration
│   │
│   ├── remediation/
│   │   ├── engine.go
│   │   ├── playbooks/
│   │   │   ├── crashloop.go
│   │   │   ├── oom.go
│   │   │   ├── bad_deploy.go
│   │   │   └── node_pressure.go
│   │   └── decision.go
│   │
│   ├── reporter/
│   │   ├── slack.go
│   │   ├── pagerduty.go
│   │   └── cr_reporter.go
│   │
│   └── controller/
│       ├── agent_controller.go
│       └── incident_controller.go
│
├── config/
│   ├── crd/
│   ├── rbac/
│   └── manager/
│
└── runbooks/                        ← YAML runbooks for known incidents
    ├── crashloop.yaml
    ├── oom.yaml
    └── node-pressure.yaml
```

---

## Incident Flow — End to End

```
[1] Pod enters CrashLoopBackOff
         │
[2] Pod Watcher detects event
         │
[3] Correlator matches pattern:
    CrashLoop + OOMKilled + recent deploy
         │
[4] Incident created (P2)
         │
[5] Evidence Gatherer pulls:
    - Last 100 log lines
    - kubectl describe pod
    - Deployment history
    - Memory metrics last 30m
         │
[6] Rule Analyzer: matches "OOM after deploy" pattern → 80% confidence
    AI Analyzer:   reads logs + events → "heap not freed" → 94% confidence
         │
[7] RCA Report generated
         │
[8] Decision Engine:
    - Autonomy Level 2 → safe to auto-rollback
         │
[9] Remediation:
    - Rollback deployment to prev revision
    - Add annotation to deployment
         │
[10] Notification:
    - Slack: "🔴 P2 Incident in production/payment-service
              RCA: OOM due to memory leak in v2.3.1
              Action: Auto-rolled back to v2.3.0 ✅"
         │
[11] IncidentReport CR created in namespace
         │
[12] Post-mortem report generated and sent
```

---

## Tech Stack

| Component | Technology |
|---|---|
| Language | Go |
| Operator Framework | kubebuilder + controller-runtime |
| Metrics Collection | Prometheus client + metrics-server API |
| Log Analysis | Loki API or k8s pod log streaming |
| AI/LLM | OpenAI GPT-4o / Anthropic Claude / Ollama (local) |
| Notifications | Slack SDK, PagerDuty API, SMTP |
| Storage (incident history) | etcd via CRDs or SQLite sidecar |
| Dashboards | Grafana (auto-provisioned via CRD) |

---

## Build Phases

| Phase | Scope |
|---|---|
| **Phase 1** | Watcher + Correlator + basic Slack alerts |
| **Phase 2** | RCA Engine with rule-based analysis + IncidentReport CRs |
| **Phase 3** | AI/LLM integration for log analysis |
| **Phase 4** | Remediation playbooks + autonomy levels |
| **Phase 5** | Auto post-mortems + Grafana dashboards + runbook library |

