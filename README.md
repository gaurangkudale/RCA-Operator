<div align="center">

**RCA Operator for Kubernetes Phase 1**

*Cluster-native incident detection, durable incident state, notifications, and dashboarding*

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![kubebuilder](https://img.shields.io/badge/Built%20with-kubebuilder-FF6B6B)](https://book.kubebuilder.io)

</div>

## What RCA Operator Does

RCA Operator Phase 1 is a Kubernetes-native incident detection operator that:

- collects failure signals from native Kubernetes APIs
- persists durable incident state in `IncidentReport`
- notifies humans from incident lifecycle state
- serves a built-in dashboard backed only by `IncidentReport` and `RCAAgent`

Phase 1 is intentionally narrow. It avoids AI systems, external databases, and log-scraping dependencies so the operator stays easy to run and reason about in-cluster.

## Phase 1 Architecture

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
      +-------+-------+
      |               |
      v               v
+-----------------------------+   +-----------------------------+
| Signal Collectors           |   | Dashboard API Server        |
|  - node                     |   | Reads IncidentReport CRs    |
|  - pod                      |   | Reads RCAAgent CRs          |
|  - workload                 |   | No raw cluster reads        |
|  - event                    |   +-----------------------------+
+-------------+---------------+
              |
              v
+-----------------------------+
| Incident Engine             |
|  - fingerprinting           |
|  - 5m stabilization         |
|  - deduplication            |
|  - lifecycle transitions    |
+-------------+---------------+
              |
              v
+-----------------------------+
| IncidentReport CRD          |
| durable source of truth     |
+-------------+---------------+
              |
      +-------+-------+
      |               |
      v               v
+-------------+  +----------------+
| Notifications|  | Dashboard UI   |
| Slack/PD/K8s |  | reads CRs only |
+-------------+  +----------------+
```

More detail lives in [Architecture](docs/concepts/Architecture.md) and [Phase 1 Architecture](docs/phases/PHASE1_ARCHITECTURE.md).

## Current Feature Set

| Feature | Description |
|---|---|
| Native Kubernetes signal collection | Reads pod, event, node, and workload state from Kubernetes |
| Durable incident records | Deduplicates repeated signals into one `IncidentReport` per fingerprint |
| Incident lifecycle | Tracks `Detecting`, `Active`, and `Resolved` phases |
| Notifications | Sends Slack and PagerDuty notifications and emits Kubernetes events |
| Dashboard | Built-in incident dashboard served by the operator |
| Retention | Automatically prunes old resolved incidents |

## Quick Install

### Helm

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace
```

### kubectl

```bash
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml
kubectl apply -f config/samples/rca_v1alpha1_rcaagent.yaml
```

The checked-in sample is minimal and does not require notification secrets. If you enable notifications, create the referenced Slack and PagerDuty secrets first.

## Documentation

| Section | Description |
|---|---|
| [Prerequisites](docs/getting-started/prerequisites.md) | Cluster and tooling requirements |
| [Installation](docs/getting-started/installation.md) | Helm and kubectl installation |
| [Quick Start](docs/getting-started/quickstart.md) | Deploy your first agent in minutes |
| [Architecture](docs/concepts/Architecture.md) | Phase 1 system design and data flow |
| [Phase 1 Architecture](docs/phases/PHASE1_ARCHITECTURE.md) | Production target and cleanup baseline |
| [RCAAgent CRD Reference](docs/reference/rcaagent-crd.md) | `RCAAgent` schema and examples |
| [Dashboard](docs/features/DASHBOARD.md) | Dashboard data model and access patterns |
| [RBAC Reference](docs/reference/rbac.md) | Permissions used by the operator |
| [Local Development](docs/development/local-setup.md) | Run locally against a cluster |
| [Testing Guide](docs/development/testing.md) | Unit, envtest, and e2e coverage |
| [Fixtures](test/fixtures/README.md) | Manual scenarios for incident testing |

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

## Contributing

Contributions are welcome.

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
2. Run `make test`
3. Open a pull request

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE).
