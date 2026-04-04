# Changelog

All notable changes to RCA Operator are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

> **How to read this file**
>
> - `[Unreleased]` — changes on `main-gk` not yet in a release
> - Entries are newest-first within each section
> - Each version links to the GitHub diff since the previous release
> - Section types: `Added` · `Changed` · `Deprecated` · `Removed` · `Fixed` · `Security`

---

## [Unreleased]

### Added

- **StatefulSet, DaemonSet, Job, CronJob collectors** — new workload-level signal collectors detect stalled StatefulSet/DaemonSet rollouts, failed Jobs (BackoffLimitExceeded, DeadlineExceeded), and failed CronJob child runs; includes incident resolution support, signal mappings, RBAC markers, and full test coverage
- **Automatic correlation rule detection** — `internal/autodetect` package mines the correlation buffer for recurring signal co-occurrence patterns and auto-creates `RCACorrelationRule` CRDs when occurrence thresholds are met; includes pattern mining, accumulator, CRD lifecycle management, expiry, startup recovery, Prometheus metrics, and Helm integration (`--enable-autodetect`)
- **CRD-driven correlation rules** — `RCACorrelationRule` cluster-scoped CRD with dynamic rule loading, template-based summaries, and a dedicated controller that reloads rules on create/update/delete without operator restart
- **RCACorrelationRule controller** — watches `RCACorrelationRule` CRDs and reloads the rule engine automatically
- **CRD rule engine** — factory-based plugin (priority 200) that evaluates rules loaded from CRDs at runtime
- **4 default correlation rules** shipped via Helm chart (`node-plus-eviction`, `crashloop-plus-oom`, `crashloop-plus-deploy`, `imagepull-no-history`)
- **Dashboard redesign** — new clean UI with light/dark theme toggle (persisted to localStorage), Inter + JetBrains Mono fonts
- **Workload-scoped fingerprinting** — `Workload|ns|kind|name` format for proper incident dedup across pod restarts
- **Signal processing pipeline** — Normalizer → Enricher → Rule Engine → Reporter architecture
- **Three-stage incident lookup** — `findOpenIncident` → `findResolvableIncident` → `findExistingByWorkloadRef` for zero-duplicate guarantees
- **ExitCodePattern suppression** — prevents duplicate incidents when exit codes build toward ConsecutiveExitCode threshold
- **FrequencySpike auto-resolution guard** — namespace-scoped incidents no longer incorrectly auto-resolved
- **Helm hooks for default rules** — `post-install,post-upgrade` hooks ensure CRDs are registered before rules are applied
- **OpenTelemetry support** — optional OTLP trace/metric export via `spec.otel` configuration
- **Signal mappings** — `spec.signalMappings` allows overriding default event→incident type mappings
- ADR-0001 documenting the Phase 1 Kubernetes-native incident architecture

### Changed

- **All correlation rules are now CRD-driven** — removed all hardcoded Go rules from `internal/correlator/rules.go`
- Removed legacy correlator rule engine factory (`correlator_rule_engine.go`) — CRD engine is the only active engine
- Incident types are now self-describing (`CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, `NodeNotReady`, `StalledRollout`) instead of aliases (`OOM`, `Registry`, `NodeFailure`, `BadDeploy`)
- Simplified `RCAAgent` to Phase 1 fields only
- Switched secret validation from unused AI settings to real Slack and PagerDuty notification secrets
- Consolidated 3 release workflows into clear separation: `release.yml` (Docker + manifests), `helm-release.yml` (chart release), `helm-gh-pages.yml` (chart repo)

### Removed

- All 4 hardcoded correlation rules from Go code (replaced by CRD rules)
- Legacy correlator rule engine factory (`internal/engine/correlator_rule_engine.go`)
- CPU throttling and `ResourceSaturation` incident paths that are outside the current architecture
- Stale AI/OpenAI setup guidance and watcher-first planning docs

### Fixed

- Duplicate incidents on operator restart (40h resolved StalledRollout + new Active for same workload)
- StalledRollout getting pod-scoped fingerprint instead of workload-scoped
- Enricher overwriting pre-existing WorkloadRef during scope resolution
- Helm chart install failure when CRDs and CRs are in the same release (added post-install hooks)

---

## [0.0.5] — 2026-04-02

> CRD rule engine, dashboard redesign, Helm production readiness.

### Added

- RCACorrelationRule CRD and controller
- CRD rule engine with dynamic rule loading
- Dashboard light/dark theme
- Helm hooks for default rules
- RBAC for `rcacorrelationrules` get/list/watch

---

## [0.0.4] — 2026-03-22

> Duplicate incident prevention, workload-scoped fingerprinting.

### Fixed

- ExitCodePattern suppression for consecutive exit codes
- FrequencySpike auto-resolution guard for namespace-scoped incidents

---

## [0.0.1] — *Project scaffolding*

### Added

- Initial kubebuilder project structure
- Go module `github.com/gaurangkudale/rca-operator`
- CI pipeline (lint, build, unit test)
- `LICENSE` (MIT)
- `README.md` skeleton
- Stub directories for all planned packages

---

<!-- Version diff links — update on each release -->
[Unreleased]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.5...HEAD
[0.0.5]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.1...v0.0.4
[0.0.1]: https://github.com/gaurangkudale/RCA-Operator/releases/tag/v0.0.1
