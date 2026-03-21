<div align="center">

**The Autonomous AI-Powered SRE Operator for Kubernetes**

*24x7 incident detection • Root cause analysis • Autonomous remediation*

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![kubebuilder](https://img.shields.io/badge/Built%20with-kubebuilder-FF6B6B)](https://book.kubebuilder.io)
[![Go Report Card](https://goreportcard.com/badge/github.com/gaurangkudale/RCA-operator)](https://goreportcard.com/report/github.com/gaurangkudale/RCA-operator)
[![codecov](https://codecov.io/gh/gaurangkudale/RCA-operator/branch/main-gk/graph/badge.svg)](https://codecov.io/gh/gaurangkudale/RCA-operator)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

</div>

---

## What is RCA Operator?

**RCA Operator** is an open-source Kubernetes operator that acts as your always-on Site Reliability Engineer. It watches namespaces in your cluster, correlates signals across pods, nodes, services, and metrics, and autonomously performs root cause analysis when incidents occur — then either alerts your team with a full diagnosis or automatically remediates the issue based on your configured autonomy level.

```
Traditional SRE:  Alert → Human wakes up → Investigates → Finds root cause → Fixes
RCA SRE:          Alert → Detect → Correlate → RCA in seconds → Fix → Post-mortem auto-drafted
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                              │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │                     RCA SRE Operator                            │    │
│  │  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────┐  │    │
│  │  │   Watcher   │  │  Correlator  │  │     RCA Engine         │  │    │
│  │  │   Layer     │─►│   & Triage   │─►│  (Rules + AI/LLM)      │  │    │
│  │  └─────────────┘  └──────────────┘  └──────────┬─────────────┘  │    │
│  │  ┌─────────────┐  ┌──────────────┐  ┌──────────▼─────────────┐  │    │
│  │  │  Remediation│◄─│  Decision    │◄─│   Incident Manager     │  │    │
│  │  │  Engine     │  │  Engine      │  │                        │  │    │
│  │  └─────────────┘  └──────────────┘  └────────────────────────┘  │    │
│  │  ┌──────────────────────────────────────────────────────────┐   │    │
│  │  │    Reporting & Notification Layer                        │   │    │
│  │  │    Slack · PagerDuty · Email · Webhooks · K8s Events     │   │    │
│  │  └──────────────────────────────────────────────────────────┘   │    │
│  └─────────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────────┘
```

→ [Full architecture details](docs/concepts/Architecture.md)

---

## Key Features

| Feature | Description |
|---|---|
| 🔭 **24x7 Watching** | Continuously monitors pods, nodes, events, and metrics in real time |
| 🧠 **AI-Powered RCA** | Integrates with OpenAI to analyze and explain incidents (Phase 2+) |
| 🔗 **Signal Correlation** | Correlates events across resources to find root causes, not just symptoms |
| 🎯 **Autonomy Levels** | Configurable from observe-only to fully autonomous remediation (Phase 2+) |
| 📋 **IncidentReport CRs** | Native Kubernetes CRs for every incident with full timeline and RCA |
| 📣 **Multi-channel Alerts** | Slack and PagerDuty with rich incident context |
| 🏗️ **GitOps Friendly** | All configuration via CRDs — fits naturally into GitOps workflows |
| 🔐 **RBAC Native** | Least-privilege permissions with fine-grained Kubernetes RBAC |

---

## Quick Install

### Helm (Recommended)
```bash
# Method A: From Helm repository
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace

# Method B: Direct from GitHub release
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system --create-namespace
```

### kubectl
```bash
# 1. Install CRDs and operator
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml

# 2. Create the required Secret
kubectl create secret generic rca-agent-openai-secret \
  --from-literal=apiKey=<YOUR_KEY> -n rca-system

# 3. Apply a minimal RCAAgent
kubectl apply -f config/samples/rca_v1alpha1_rcaagent.yaml
```

→ [Full installation guide](docs/getting-started/installation.md) · [Quick Start with notifications](docs/getting-started/quickstart.md)

---

## Documentation

| Section | Description |
|---|---|
| [Prerequisites](docs/getting-started/prerequisites.md) | Cluster version, tools, and AI provider requirements |
| [Installation](docs/getting-started/installation.md) | Helm and kubectl installation |
| [Quick Start](docs/getting-started/quickstart.md) | Deploy your first agent in 5 minutes |
| [Architecture](docs/concepts/Architecture.md) | System design and data flow |
| [RCAAgent CRD Reference](docs/reference/rcaagent-crd.md) | Full field reference, autonomy levels |
| [Watcher Reference](docs/reference/watcher.md) | Event catalog, detection thresholds, and CrashLoop exit-code context |
| [RBAC Reference](docs/reference/rbac.md) | Permissions explained |
| [Local Development](docs/development/local-setup.md) | Run the operator locally with Kind |
| [Testing Guide](docs/development/testing.md) | Unit, integration, and e2e testing |
| [Phase 1 Plan](docs/phases/PHASE1.md) | Current milestone scope and definition of done |
| [Test Fixtures](test/fixtures/README.md) | Scenario pods for manual incident testing |

---

## Custom Resources

### RCAAgent

The main configuration CRD. One agent can watch multiple namespaces.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

→ [Full RCAAgent field reference](docs/reference/rcaagent-crd.md)

### IncidentReport

Auto-created per detected incident. Contains the full timeline, RCA result, and actions taken.

```bash
kubectl get incidentreport -n <namespace>
kubectl describe incidentreport <name> -n <namespace>
```

→ [IncidentReport CRD reference](docs/reference/incidentreport-crd.md) *(coming in Phase 2)*

---

## Roadmap

| Version | Focus | Status |
|---|---|---|
| **v0.1** — Foundation | CRDs, Pod watcher, Correlator, Slack/PagerDuty, Helm chart | ✅ In progress |
| **v0.2** — RCA Engine | Rule-based + AI/LLM analyzer, evidence gathering, confidence scoring | 🔜 Planned |
| **v0.3** — Remediation | Autonomy levels, built-in playbooks, custom runbooks, rollback | 🔜 Planned |
| **v0.4** — Observability | Auto post-mortems, Grafana dashboards, SLO tracking, Prometheus metrics | 🔜 Planned |
| **v1.0** — Production Ready | OLM/OperatorHub, multi-cluster, web UI | 🔜 Planned |

---

## Contributing

Contributions are welcome — bug reports, docs improvements, playbooks, and code.

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
2. Fork → feature branch → `make test` → Pull Request

---

## License

Licensed under the **Apache License 2.0** — see [LICENSE](LICENSE) for details.

---

## Acknowledgements

Built on [kubebuilder](https://book.kubebuilder.io) and [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime). Inspired by real-world SRE practices from the [Google SRE Book](https://sre.google/books/).

<div align="center">

*If RCA saved your on-call rotation, give us a ⭐ on GitHub!*

</div>
