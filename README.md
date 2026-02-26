<div align="center">



**The Autonomous AI-Powered SRE Operator for Kubernetes**

*24x7 incident detection • Root cause analysis • Autonomous remediation*

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![kubebuilder](https://img.shields.io/badge/Built%20with-kubebuilder-FF6B6B)](https://book.kubebuilder.io)
[![Go Report Card](https://goreportcard.com/badge/github.com/gaurangkudale/RCA-operator)](https://goreportcard.com/report/github.com/gaurangkudale/RCA-operator)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![Discord](https://img.shields.io/badge/Discord-Join%20Community-5865F2?logo=discord)](https://discord.gg/RCA)

</div>

---

## What is RCA Operator?

**RCA Operator** is an open-source Kubernetes operator that acts as your always-on Site Reliability Engineer. It watches every namespace in your cluster, correlates signals across pods, nodes, services, and metrics, and autonomously performs root cause analysis when incidents occur — then either alerts your team with a full diagnosis or automatically remediates the issue based on your configured autonomy level.

> **Traditional SRE:** Alert → Human wakes up → Investigates for 20 mins → Finds root cause → Fixes
>
> **RCA SRE:** Alert → Detect → Correlate → RCA in seconds → Fix (optionally autonomous) → Post-mortem auto-drafted

---

## ✨ Key Features

| Feature | Description |
|---|---|
| 🔭 **24x7 Watching** | Continuously monitors pods, nodes, events, logs, and metrics in real time |
| 🧠 **AI-Powered RCA** | Integrates with OpenAI, Anthropic Claude, or local LLMs (Ollama) to analyze incidents |
| 🔗 **Signal Correlation** | Correlates events across resources to identify root causes, not just symptoms |
| 🎯 **Autonomy Levels** | Configurable from observe-only to fully autonomous remediation per namespace |
| 📋 **IncidentReport CRs** | Creates native Kubernetes CRs for every incident with full timeline and RCA |
| 🔄 **Auto Remediation** | Built-in playbooks for CrashLoop, OOMKill, bad deployments, node pressure, and more |
| 📣 **Multi-channel Alerts** | Slack, PagerDuty, email, and custom webhooks with rich incident context |
| 📝 **Auto Post-mortems** | Generates post-mortem drafts with timeline, blast radius, and recommendations |
| 🏗️ **GitOps Friendly** | All configuration via CRDs — fits naturally into GitOps workflows |
| 🔐 **RBAC Native** | Follows least-privilege principles with fine-grained Kubernetes RBAC |

---

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                               │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                     RCA SRE Operator                        │   │
│  │                                                                  │   │
│  │  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐ │   │
│  │  │   Watcher   │  │  Correlator  │  │     RCA Engine         │ │   │
│  │  │   Layer     │─►│   & Triage   │─►│  (Rules + AI/LLM)      │ │   │
│  │  └─────────────┘  └──────────────┘  └──────────┬─────────────┘ │   │
│  │                                                 │               │   │
│  │  ┌─────────────┐  ┌──────────────┐  ┌──────────▼─────────────┐ │   │
│  │  │  Remediation│◄─│  Decision    │◄─│   Incident Manager     │ │   │
│  │  │  Engine     │  │  Engine      │  │                        │ │   │
│  │  └──────┬──────┘  └──────────────┘  └────────────────────────┘ │   │
│  │         │                                                        │   │
│  │  ┌──────▼──────────────────────────────────────────────────┐   │   │
│  │  │           Reporting & Notification Layer                  │   │   │
│  │  │    Slack · PagerDuty · Email · Webhooks · K8s Events     │   │   │
│  │  └──────────────────────────────────────────────────────────┘   │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│                                                                         │
│   Watched Resources:                                                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────┐  │
│  │  Pods    │ │ Services │ │  Nodes   │ │  Events  │ │ Deployments│  │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

### How an Incident Flows

```
[1] Pod enters CrashLoopBackOff
[2] Watcher detects event stream anomaly
[3] Correlator links: CrashLoop + OOMKilled + recent deployment at T-4min
[4] Incident created with severity P2
[5] Evidence Gatherer pulls logs, describe output, metrics, deploy history
[6] Rule Analyzer: "OOM after deploy" → 80% confidence
    AI Analyzer:   reads logs → "heap not freed in request handler" → 94% confidence
[7] RCA Report generated with timeline and blast radius
[8] Decision Engine: autonomy level 2 → safe to auto-rollback
[9] Remediation: rollback deployment + annotate resource
[10] Slack: "🔴 P2 Incident | RCA: Memory leak in v2.3.1 | Auto-rolled back ✅"
[11] IncidentReport CR created in namespace
[12] Post-mortem draft generated and sent to team
```

---

## 🚀 Quick Start

### Prerequisites

- Kubernetes cluster v1.26+
- `kubectl` configured with cluster access
- Helm v3+ (recommended installation method)
- An AI provider API key (OpenAI, Anthropic) **or** a local Ollama instance

### Install via Helm

```bash
# Add the RCA Helm repository
helm repo add RCA https://charts.RCA.io
helm repo update

# Install RCA into its own namespace
helm install RCA RCA/RCA-operator \
  --namespace RCA-system \
  --create-namespace \
  --set aiProvider.type=openai \
  --set aiProvider.apiKey=<YOUR_API_KEY>
```

### Install via kubectl

```bash
# Install CRDs
kubectl apply -f https://github.com/gaurangkudale/RCA-operator/releases/latest/download/crds.yaml

# Install the operator
kubectl apply -f https://github.com/gaurangkudale/RCA-operator/releases/latest/download/install.yaml
```

### Deploy your first SRE Agent

```yaml
# RCA-agent.yaml
apiVersion: RCA.io/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: RCA-system
spec:
  watchNamespaces:
    - production
    - staging
  autonomyLevel: 1              # 0=observe, 1=suggest, 2=semi-auto, 3=full-auto
  aiProvider:
    type: openai                # openai | anthropic | ollama
    model: gpt-4o
    secretRef: ai-api-key
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
    pagerduty:
      secretRef: pd-api-key
      severity: P2
```

```bash
kubectl apply -f RCA-agent.yaml

# Verify the agent is running
kubectl get RCAagent -n RCA-system
kubectl get pods -n RCA-system
```

---

## ⚙️ Configuration

### Autonomy Levels

RCA gives you full control over how much it acts on its own. Set the level globally or per namespace.

| Level | Mode | What Happens |
|---|---|---|
| `0` | **Observe** | Monitors and logs only. No alerts, no action. |
| `1` | **Suggest** | Sends RCA + recommended fix to Slack/PagerDuty. Human approves all actions. |
| `2` | **Semi-Auto** | Auto-fixes safe actions (restart pod, scale up). Alerts human for risky actions (rollback, delete). |
| `3` | **Full-Auto** | Executes all remediations autonomously. Sends post-incident report afterward. |

```yaml
spec:
  namespaceAutonomy:
    production: 1     # always suggest-only in prod
    staging: 2        # semi-auto in staging
    dev: 3            # fully autonomous in dev
```

### Full Agent Configuration Reference

```yaml
apiVersion: RCA.io/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: RCA-system
spec:

  # Namespaces to watch (omit for all namespaces)
  watchNamespaces:
    - production
    - staging

  # Global autonomy level (overridden per namespace above)
  autonomyLevel: 1

  # AI Provider Configuration
  aiProvider:
    type: openai              # openai | anthropic | ollama
    model: gpt-4o
    secretRef: ai-api-key    # Secret with key: apiKey
    maxTokens: 4096
    temperature: 0.2

  # SLO / Error Budget Tracking
  sloConfig:
    errorBudget: 99.9         # % availability target
    latencyP99: 500ms
    windowDays: 30

  # Incident Severity Rules
  severity:
    p1:
      - pattern: "cluster-wide"
      - minPodsAffected: 10
    p2:
      - pattern: "namespace-wide"
      - minPodsAffected: 3

  # Notification Channels
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
    pagerduty:
      secretRef: pd-api-key
      escalationPolicy: default
    email:
      smtpSecretRef: smtp-credentials
      recipients:
        - sre-team@company.com
    webhook:
      url: https://your-endpoint.com/RCA
      secretRef: webhook-hmac-secret

  # Custom Runbooks (ConfigMap)
  runbooks:
    configMapRef: RCA-runbooks

  # Retention
  incidentRetentionDays: 90
```

---

## 📊 Custom Resources

### RCAAgent

The main configuration CRD. One agent can watch multiple namespaces.

```bash
kubectl get RCAagent -A
kubectl describe RCAagent sre-agent -n RCA-system
```

### IncidentReport

Auto-created per incident. Provides a full timeline and RCA result.

```bash
# List all incidents
kubectl get incidentreport -n production

# View a specific incident
kubectl describe incidentreport incident-2024-02-24-001 -n production
```

Example output:

```yaml
status:
  severity: P2
  startTime: "2024-02-24T10:32:00Z"
  resolvedTime: "2024-02-24T10:45:00Z"
  affectedResources:
    - kind: Deployment
      name: payment-service
  rootCause: >
    OOMKilled due to memory leak in v2.3.1.
    Heap not released after HTTP request handler completed.
    Correlated with deployment at 10:28 UTC.
  confidence: 94
  timeline:
    - time: "10:32:00" event: "Pod payment-service-xxx entered CrashLoopBackOff"
    - time: "10:33:00" event: "RCA detected OOMKilled pattern"
    - time: "10:34:00" event: "Correlated with deployment at 10:28 UTC"
    - time: "10:35:00" event: "Auto-rollback triggered to revision 14 (v2.3.0)"
    - time: "10:45:00" event: "Service healthy. Incident resolved."
  actionsTaken:
    - "Auto-rolled back Deployment to revision 14"
    - "Notified #incidents Slack channel"
    - "Created PagerDuty incident PD-44821"
  recommendations:
    - "Add memory profiling gate in CI pipeline"
    - "Set memory limit alerting at 80% threshold"
    - "Review heap allocation in request handler middleware"
```

---

## 🧩 Built-in Remediation Playbooks

RCA ships with playbooks for the most common Kubernetes incidents.

| Incident Type | Detection | Automated Actions |
|---|---|---|
| `CrashLoopBackOff` | Pod restart count + event pattern | Capture logs → check OOM → increase limit or alert |
| `OOMKilled` | Container exit code 137 | Correlate with deployment → rollback or raise memory limit |
| `Bad Deployment` | 5xx spike within N mins of deploy | Pause rollout → auto-rollback → notify |
| `Node NotReady` | Node condition change | Cordon → drain non-critical pods → notify |
| `Node Pressure` | DiskPressure / MemoryPressure | Evict best-effort pods → alert for node replacement |
| `PVC Pending` | PVC stuck in Pending state | Check StorageClass → suggest fix |
| `High CPU Throttling` | cAdvisor metrics | Scale horizontally → raise CPU limit → flag for right-sizing |
| `ImagePullBackOff` | Pod event pattern | Validate image name/tag → check registry credentials |
| `DNS Failures` | Endpoint resolution errors | Check CoreDNS health → restart DNS pods if needed |
| `HPA at Max Replicas` | HPA metric + maxReplicas | Alert for capacity review → suggest limit increase |

### Custom Runbooks

Define your own runbooks in a ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: RCA-runbooks
  namespace: RCA-system
data:
  payment-service-5xx.yaml: |
    trigger:
      service: payment-service
      errorRate: ">5%"
      duration: "2m"
    steps:
      - action: notify
        message: "Payment service degraded — check DB connection pool"
      - action: scaleUp
        target: payment-service
        replicas: +2
      - action: linkRunbook
        url: "https://wiki.company.com/runbooks/payment-service"
```

---

## 🔒 Security & RBAC

RCA follows least-privilege principles. The operator is granted only what it needs per the default ClusterRole.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: RCA-operator-role
rules:
  - apiGroups: ["RCA.io"]
    resources: ["RCAagents", "incidentreports"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods", "pods/log", "events", "nodes", "endpoints", "services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["metrics.k8s.io"]
    resources: ["pods", "nodes"]
    verbs: ["get", "list"]
```

> **Note:** Write permissions on Deployments are only used when autonomy level ≥ 2. For level 0 and 1, you can restrict verbs to `["get", "list", "watch"]` only.

---

## 🗂️ Project Structure

```
RCA-operator/
│
├── cmd/
│   └── main.go                        # Entry point, manager setup
│
├── api/
│   └── v1alpha1/
│       ├── RCAagent_types.go     # Agent CRD types
│       ├── incidentreport_types.go    # IncidentReport CRD types
│       └── zz_generated.deepcopy.go
│
├── internal/
│   ├── watcher/
│   │   ├── pod_watcher.go             # Pod events & status
│   │   ├── node_watcher.go            # Node health
│   │   ├── event_watcher.go           # K8s event stream
│   │   ├── metrics_watcher.go         # CPU, memory, network
│   │   └── log_watcher.go             # Pod log streaming
│   │
│   ├── correlator/
│   │   ├── correlator.go              # Signal correlation engine
│   │   ├── rules.go                   # Correlation rule definitions
│   │   └── incident.go                # Incident model & severity
│   │
│   ├── rca/
│   │   ├── engine.go                  # RCA orchestration
│   │   ├── evidence_gatherer.go       # Collects logs, metrics, events
│   │   ├── rule_analyzer.go           # Rule-based pattern matching
│   │   └── ai_analyzer.go             # LLM integration
│   │
│   ├── remediation/
│   │   ├── engine.go                  # Remediation orchestrator
│   │   ├── decision.go                # Autonomy level enforcement
│   │   └── playbooks/
│   │       ├── crashloop.go
│   │       ├── oom.go
│   │       ├── bad_deploy.go
│   │       ├── node_pressure.go
│   │       └── storage.go
│   │
│   ├── reporter/
│   │   ├── slack.go
│   │   ├── pagerduty.go
│   │   ├── email.go
│   │   └── cr_reporter.go             # Creates IncidentReport CRs
│   │
│   └── controller/
│       ├── agent_controller.go
│       └── incident_controller.go
│
├── config/
│   ├── crd/                           # Generated CRD manifests
│   ├── rbac/                          # RBAC rules
│   ├── manager/                       # Operator Deployment manifests
│   └── samples/                       # Example CRs
│
├── runbooks/                          # Built-in runbook YAML definitions
│   ├── crashloop.yaml
│   ├── oom.yaml
│   └── node-pressure.yaml
│
├── charts/
│   └── RCA-operator/             # Helm chart
│
├── docs/                              # Documentation
├── tests/
│   ├── e2e/
│   └── unit/
│
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
├── SECURITY.md
└── LICENSE
```

---

## 🛠️ Development

### Prerequisites

```bash
# Go 1.22+
go version

# kubebuilder
curl -L -o kubebuilder "https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)"
chmod +x kubebuilder && sudo mv kubebuilder /usr/local/bin/

# kind (for local cluster)
go install sigs.k8s.io/kind@latest
```

### Run Locally

```bash
# Clone the repo
git clone https://github.com/gaurangkudale/RCA-operator.git
cd RCA-operator

# Install dependencies
go mod tidy

# Start a local cluster
kind create cluster --name RCA-dev

# Install CRDs into cluster
make install

# Run the operator locally (uses your current kubeconfig context)
make run
```

### Run Tests

```bash
# Unit tests
make test

# E2E tests (requires a running cluster)
make test-e2e

# Generate CRD manifests after changing types
make generate manifests
```

### Build & Push Docker Image

```bash
make docker-build docker-push IMG=<your-registry>/RCA-operator:latest
```

---

## 🗺️ Roadmap

### v0.1 — Foundation *(current)*
- [x] CRD definitions: `RCAAgent`, `IncidentReport`
- [x] Pod and Event watchers
- [x] Basic signal correlation engine
- [x] Slack and PagerDuty notifications
- [x] Helm chart

### v0.2 — RCA Engine
- [ ] Rule-based analyzer with 10+ built-in incident patterns
- [ ] AI/LLM integration (OpenAI, Anthropic, Ollama)
- [ ] Evidence gatherer (logs, metrics, describe)
- [ ] Confidence scoring

### v0.3 — Remediation
- [ ] Autonomy levels (0–3)
- [ ] 10+ built-in remediation playbooks
- [ ] Custom runbook support via ConfigMap
- [ ] Rollback automation

### v0.4 — Observability
- [ ] Auto post-mortem generation
- [ ] Grafana dashboard provisioning
- [ ] SLO/error budget tracking
- [ ] Prometheus metrics for operator health

### v1.0 — Production Ready
- [ ] OLM / OperatorHub publishing
- [ ] Multi-cluster support
- [ ] Cost-aware scaling recommendations
- [ ] Web UI for incident history

---

## 🤝 Contributing

We love contributions! RCA Operator is built by the community, for the community.

### Ways to Contribute

- 🐛 **Report bugs** via [GitHub Issues](https://github.com/gaurangkudale/RCA-operator/issues)
- 💡 **Suggest features** via [GitHub Discussions](https://github.com/gaurangkudale/RCA-operator/discussions)
- 📖 **Improve documentation** — even small fixes matter
- 🔧 **Submit playbooks** — share runbooks for incidents you've solved
- 💻 **Write code** — pick up a `good first issue` to get started

### Getting Started

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-new-playbook`
3. Make your changes and write tests
4. Run the test suite: `make test`
5. Submit a Pull Request

Please read [CONTRIBUTING.md](CONTRIBUTING.md) and our [Code of Conduct](CODE_OF_CONDUCT.md) before contributing.

---

## 💬 Community

| Platform | Link |
|---|---|
>> TO-DO

---

## 📜 License

RCA Operator is licensed under the **MIT License** — see [LICENSE](LICENSE) for details.

You are free to use, modify, and distribute this software in commercial and non-commercial projects. Contributions are welcome under the same license.

---

## 🙏 Acknowledgements

RCA Operator stands on the shoulders of giants:

- [kubebuilder](https://book.kubebuilder.io) — operator scaffolding framework
- [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) — the reconciliation engine
- [client-go](https://github.com/kubernetes/client-go) — Kubernetes Go client
- Inspired by real-world SRE practices from the [Google SRE Book](https://sre.google/books/)

---

<div align="center">

**Built with ❤️ by the RCA Operator community**

*If RCA saved your on-call rotation, give us a ⭐ on GitHub!*

</div>