<div align="center">

**RCA Operator for Kubernetes Phase 1**

*Cluster-native incident detection, correlation, notification, and dashboarding*

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://golang.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.26+-326CE5?logo=kubernetes)](https://kubernetes.io)
[![kubebuilder](https://img.shields.io/badge/Built%20with-kubebuilder-FF6B6B)](https://book.kubebuilder.io)

</div>

## What RCA Operator Does Today

RCA Operator Phase 1 is a production-focused Kubernetes operator that:

- watches pods, events, nodes, and deployments
- correlates noisy signals into durable `IncidentReport` resources
- notifies humans through Slack and PagerDuty
- exposes a built-in dashboard backed only by Kubernetes custom resources

The Phase 1 goal is simple: detect what is broken, group repeated symptoms into one incident, preserve a useful timeline, and keep the operator easy to run in-cluster.

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
              v
+-----------------------------+
| Watchers                     |
|  - Pod watcher               |
|  - Event watcher             |
|  - Deployment watcher        |
|  - Node watcher              |
+-------------+---------------+
              |
              v
+-----------------------------+
| Correlator & Incident Logic |
|  - deduplication            |
|  - severity scoring         |
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

More detail lives in [Architecture](docs/concepts/Architecture.md) and [PHASE1_ARCHITECTURE.md](docs/phases/PHASE1_ARCHITECTURE.md).

## Current Feature Set

| Feature | Description |
|---|---|
| Watcher pipeline | Watches pod, event, deployment, and node state from Kubernetes |
| Incident correlation | Deduplicates and groups related signals into one `IncidentReport` |
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
| [RCAAgent CRD Reference](docs/reference/rcaagent-crd.md) | `RCAAgent` schema and examples |
| [Watcher Reference](docs/reference/watcher.md) | Signal catalog and trigger rules |
| [RBAC Reference](docs/reference/rbac.md) | Permissions used by the operator |
| [Local Development](docs/development/local-setup.md) | Run locally against a cluster |
| [Testing Guide](docs/development/testing.md) | Unit, envtest, and e2e coverage |
| [Phase 1 Plan](docs/phases/PHASE1.md) | Scope and definition of done |
| [Fixtures](test/fixtures/README.md) | Manual scenarios for incident testing |

## Custom Resources

### RCAAgent

The main configuration resource. One agent can watch multiple namespaces and optionally configure notifications and retention.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

### IncidentReport

Created automatically for detected incidents. Each report carries the current lifecycle phase, severity, affected resources, correlated signals, and timeline.

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
