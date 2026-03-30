# Changelog

All notable changes to RCA Operator are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

> **How to read this file**
>
> - `[Unreleased]` — changes on `main` not yet in a release
> - Entries are newest-first within each section
> - Each version links to the GitHub diff since the previous release
> - Section types: `Added` · `Changed` · `Deprecated` · `Removed` · `Fixed` · `Security`

---

## [Unreleased]

### Added

- ADR-0001 documenting the Phase 1 Kubernetes-native incident architecture

### Changed

- Simplified `RCAAgent` to Phase 1 fields only
- Switched secret validation from unused AI settings to real Slack and PagerDuty notification secrets
- Rewrote core docs and samples to focus on watcher, correlator, incident lifecycle, notifications, and dashboard

---

## [0.1.0] — *Target: TBD*

> Phase 1 Foundation — first deployable release of RCA Operator.

### Added

- **RCAAgent CRD** (`rca-operator.tech/v1alpha1`) — primary configuration resource for the operator
- **IncidentReport CRD** (`rca-operator.tech/v1alpha1`) — auto-created per detected incident, with full status lifecycle
- **Pod Watcher** — detects CrashLoopBackOff, OOMKilled (exit 137), ImagePullBackOff, and pods stuck in Pending
- **Event Watcher** — streams `core/v1` Kubernetes events with in-memory deduplication via ring buffer
- **Node Watcher** — detects `NotReady`, `DiskPressure`, and `MemoryPressure` node conditions
- **Deployment Watcher** — tracks recent deployment timestamps and revision history; detects stalled rollouts
- **Correlator** — in-memory ring buffer feeding a rule engine with 5 built-in correlation rules:
  - Rule 1: CrashLoop + OOMKilled → Memory pressure incident
  - Rule 2: CrashLoop + recent deployment → Bad deploy incident
  - Rule 3: Multiple pods failing on same node → Node-level incident
  - Rule 4: ImagePullBackOff + no prior pull success → Registry / credentials incident
  - Rule 5: Node NotReady + eviction events → Node failure incident
- **Incident lifecycle** — status transitions: `Detecting → Active → Resolved`, with auto-resolve detection
- **Severity scoring** — P1 (cluster-wide) / P2 (namespace) / P3 (single service) / P4 (warning)
- **Slack notifications** — incident open and resolved messages via webhook, configurable channel and @mention on P1
- **PagerDuty notifications** — Events API v2 trigger and auto-resolve for P1/P2 incidents
- **Kubernetes event emission** — events written to `IncidentReport` CRs for `kubectl` visibility
- **Prometheus metrics** — `rca_incidents_detected_total`, `rca_incidents_resolved_total`, `rca_watcher_events_processed_total`
- **Health endpoints** — `/healthz` and `/readyz`
- **Structured JSON logging** — via `zap` with incident ID correlation field
- **Helm chart** — `charts/rca-operator/` with RBAC, ServiceAccount, Deployment, and CRD templates
- **RBAC** — read-only `ClusterRole` on pods/events/nodes/deployments + write access to RCA Operator CRDs only
- **E2E test** — CrashLoop scenario: broken pod → IncidentReport created → Slack notified → pod healed → incident resolved
- **Sample configs** — `config/samples/rcaagent-minimal.yaml` and `config/samples/rcaagent-full.yaml`

---

## [0.0.1] — *Project scaffolding*

### Added

- Initial kubebuilder project structure
- Go module `github.com/gaurangkudale/rca-operator`
- CI pipeline (lint, build, unit test)
- `LICENSE` (Apache 2.0)
- `README.md` skeleton
- Stub directories for all planned Phase 1 packages

---

<!-- Version diff links — update on each release -->
[Unreleased]: https://github.com/gaurangkudale/rca-operator/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/gaurangkudale/rca-operator/compare/v0.0.1...v0.1.0
[0.0.1]: https://github.com/gaurangkudale/rca-operator/releases/tag/v0.0.1
