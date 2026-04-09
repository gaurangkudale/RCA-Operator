<div align="center">

**RCA Operator for Kubernetes**

*Cluster-native incident detection, cross-signal correlation, topology visualization, and AI-powered root cause analysis*

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![kubebuilder](https://img.shields.io/badge/Built%20with-kubebuilder-FF6B6B)](https://book.kubebuilder.io)

</div>

## What RCA Operator Does

RCA Operator is a Kubernetes-native incident detection and root cause analysis operator that:

- collects failure signals from native Kubernetes APIs (pods, events, nodes, deployments)
- evaluates CRD-driven correlation rules (`RCACorrelationRule`) to detect multi-signal incidents
- persists durable incident state in `IncidentReport` CRDs
- manages incident lifecycle: `Detecting` → `Active` → `Resolved`
- queries external observability backends (SigNoz, Jaeger, Prometheus) for cross-signal correlation
- builds interactive service topology maps with blast radius analysis
- provides AI-powered root cause analysis via LLM-driven investigation
- notifies humans via Slack and PagerDuty from incident lifecycle state
- serves a built-in dashboard with topology visualization, metrics, logs, and trace views

## Architecture

![alt text](<architecture.png>)

More detail lives in [Architecture](docs/concepts/Architecture.md) and [Phase 1 Architecture](docs/phases/PHASE1_ARCHITECTURE.md).

## Feature Set

### Phase 1 - Kubernetes-Native Incident Detection

| Feature | Description |
|---|---|
| Native Kubernetes signal collection | Reads pod, event, node, and workload state from Kubernetes (Deployments, StatefulSets, DaemonSets, Jobs, CronJobs) |
| CRD-driven correlation rules | `RCACorrelationRule` CRDs define multi-signal rules — no Go code changes needed |
| Automatic rule detection | Mines the correlation buffer for recurring signal patterns and auto-creates `RCACorrelationRule` CRDs |
| Durable incident records | Deduplicates repeated signals into one `IncidentReport` per fingerprint |
| Incident lifecycle | Tracks `Detecting`, `Active`, and `Resolved` phases |
| Notifications | Sends Slack and PagerDuty notifications and emits Kubernetes events |
| Dashboard | Built-in incident dashboard with light/dark theme toggle |
| Retention | Automatically prunes old resolved incidents |
| OpenTelemetry | Optional OTLP trace/metric export |

### Phase 2 - Cross-Signal Observability and AI-Powered RCA

| Feature | Description |
|---|---|
| Telemetry query layer | Pluggable `TelemetryQuerier` interface with SigNoz, Jaeger, and Prometheus backends |
| Cross-signal enrichment | Auto-enrich incidents with related error traces and upstream blast radius from telemetry backends |
| Service topology engine | Builds service dependency DAG from OTel span parent-child relationships |
| Blast radius analysis | BFS graph traversal to identify all upstream/downstream services affected by an incident |
| Topology visualization UI | Interactive SVG service graph with status-colored nodes, blast radius overlay, and service detail side panel |
| Live dashboard streams | Server-Sent Events for real-time topology and correlation signal updates |
| AI-powered RCA insights | LLM-driven investigation with tool-use pattern (query metrics, search logs, get traces) |
| PII redaction | Automatic sanitization of telemetry data before sending to LLM |

### Phase 2 Architecture

```
                    External Observability Stack
                    (SigNoz / Jaeger + Prometheus)
                            |
                   RCA Operator queries via REST/gRPC
                            |
    +------ internal/telemetry/ ------+
    | SigNoz | Jaeger | Prometheus    |   <-- TelemetryQuerier interface
    +---------------------------------+
                   |
    +------ internal/topology/ -------+
    | ServiceGraph | BlastRadius      |   <-- DAG from OTel span dependencies
    +---------------------------------+
                   |
    +------ internal/rca/ ------------+
    | Investigator | LLM Client       |   <-- AI-powered root cause analysis
    +---------------------------------+
                   |
         Dashboard REST API + SSE
    /api/topology  /api/services  /api/investigate  /api/stream/*
```

### Telemetry Backend Support

| Backend | Traces | Metrics | Logs | Topology | Best For |
|---|---|---|---|---|---|
| **SigNoz** | REST API | REST API | REST API | Service deps API | Unified stack (recommended) |
| **Jaeger v2** | HTTP/gRPC query | N/A | N/A | `/api/dependencies` | Existing Jaeger clusters |
| **Prometheus** | N/A | PromQL API | N/A | N/A | Metrics only |
| **Composite** | Jaeger | Prometheus | (Loki) | Jaeger | Fragmented stacks |

## Quick Install

### Helm

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace
```

### kubectl

```bash
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/latest/download/install.yaml
kubectl apply -f config/samples/rca_v1alpha1_rcaagent.yaml
```

The checked-in sample is minimal and does not require notification secrets. If you enable notifications, create the referenced Slack and PagerDuty secrets first.

### Enable Telemetry Integration (Phase 2)

```bash
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=signoz \
  --set telemetry.signoz.endpoint=http://signoz-query-service:8080
```

Or with Jaeger + Prometheus (composite mode):

```bash
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set telemetry.enabled=true \
  --set telemetry.backend=composite \
  --set telemetry.jaeger.endpoint=http://jaeger-query:16686 \
  --set telemetry.prometheus.endpoint=http://prometheus:9090
```

### Enable AI-Powered RCA (Phase 2)

```bash
# Create a Secret with your OpenAI API key
kubectl create secret generic openai-api-key \
  --from-literal=apiKey=sk-... \
  -n rca-system

helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set ai.enabled=true \
  --set ai.endpoint=https://api.openai.com/v1 \
  --set ai.model=gpt-4o \
  --set ai.secretRef=openai-api-key \
  --set ai.autoInvestigate=true
```

Works with any OpenAI-compatible API (OpenAI, Azure OpenAI, ollama, LiteLLM, vLLM).

## Documentation

| Section | Description |
|---|---|
| [Prerequisites](docs/getting-started/prerequisites.md) | Cluster and tooling requirements |
| [Installation](docs/getting-started/installation.md) | Helm and kubectl installation |
| [Quick Start](docs/getting-started/quickstart.md) | Deploy your first agent in minutes |
| [Architecture](docs/concepts/Architecture.md) | System design and data flow |
| [Phase 1 Architecture](docs/phases/PHASE1_ARCHITECTURE.md) | Production target and cleanup baseline |
| [RCAAgent CRD Reference](docs/reference/rcaagent-crd.md) | `RCAAgent` schema and examples |
| [IncidentReport CRD Reference](docs/reference/incidentreport-crd.md) | `IncidentReport` schema and fields |
| [RCACorrelationRule CRD Reference](docs/reference/rcacorrelationrule-crd.md) | Correlation rule schema and examples |
| [Auto-Detection](docs/features/auto-detection.md) | Automatic correlation rule detection |
| [Topology](docs/features/TOPOLOGY.md) | Service topology visualization, testing with OTel Demo |
| [Dashboard](docs/features/DASHBOARD.md) | Dashboard data model and access patterns |
| [Metrics Reference](docs/reference/metrics.md) | Prometheus metrics exposed by the operator |
| [RBAC Reference](docs/reference/rbac.md) | Permissions used by the operator |
| [Local Development](docs/development/local-setup.md) | Run locally against a cluster |
| [Testing Guide](docs/development/testing.md) | Unit, envtest, and e2e coverage |
| [Fixtures](test/fixtures/README.md) | Manual scenarios for incident testing |
| [Phase 2 Architecture](docs/phases/PHASE2_ARCHITECTURE.md) | Cross-signal correlation, topology, and AI-powered RCA |
| [Helm Chart Setup](docs/HELM_PAGES_SETUP.md) | Helm repo publishing to GitHub Pages |
| [Helm Upgrade Guide](docs/HELM_UPGRADE.md) | CRD upgrade and migration steps |

### Observability Backend Integration Guides

| Guide | Description |
|---|---|
| [SigNoz Integration](docs/integrations/signoz.md) | Unified traces + metrics + logs + topology via SigNoz |
| [Jaeger Integration](docs/integrations/jaeger.md) | Distributed traces and service topology via Jaeger |
| [Prometheus Integration](docs/integrations/prometheus.md) | Service metrics via Prometheus PromQL |
| [OpenTelemetry Integration](docs/integrations/opentelemetry.md) | Operator self-instrumentation and app trace pipeline |

## Custom Resources

### RCAAgent

The main configuration resource. One agent can watch multiple namespaces and optionally configure notifications and retention.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

### IncidentReport

Created automatically for detected incidents. Each report carries the incident fingerprint, lifecycle phase, severity, affected resources, and timeline.

```bash
kubectl get incidentreport -A
kubectl describe incidentreport <name> -n <namespace>
```

### RCACorrelationRule

Cluster-scoped rules that define multi-signal correlation logic. Rules are loaded dynamically — no operator restart needed when rules change.

```bash
kubectl get rcacorrelationrules
kubectl describe rcacorrelationrule <name>
```

Four default rules ship with the Helm chart:

| Rule | Trigger | Condition | Severity |
|---|---|---|---|
| `node-plus-eviction` | NodeNotReady | PodEvicted on same node | P1 |
| `crashloop-plus-oom` | CrashLoopBackOff | OOMKilled on same pod | P2 |
| `crashloop-plus-deploy` | CrashLoopBackOff | StalledRollout in same namespace | P2 |
| `imagepull-no-history` | ImagePullBackOff | No PodHealthy on same pod | P2 |

When auto-detection is enabled (`--enable-autodetect`), the operator also creates rules automatically from observed signal patterns. Auto-generated rules use a fixed priority of 30 (below user rules) and are labeled `rca.rca-operator.tech/auto-generated: "true"`. See [Auto-Detection](docs/features/auto-detection.md) for details.

## Contributing

Contributions are welcome.

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
2. Run `make test`
3. Open a pull request

## License

Licensed under the MIT License. See [LICENSE](LICENSE).
