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

### Changed
## [0.0.17] — 2026-05-30

- **Admission webhooks default-on** — `--enable-webhooks` now defaults to `true` so invalid `RCACorrelationRule` and `RCAAgent` specs are rejected at admission time instead of surfacing only when the operator tries to load them. Set `--enable-webhooks=false` for local runs without a TLS certificate available.
- **Auto-detector lifecycle** — the auto-detector is now registered with the controller-runtime manager via `mgr.Add(det)` instead of being launched as a bare goroutine. It now participates in leader election and graceful shutdown. A `Detector.Start(ctx) error` method was added to satisfy the `manager.Runnable` interface; `Detector.Run` is retained for direct callers.
- **`cmd/main.go` refactor** — `main()` split into small `setup*` / `build*` helpers (`setupOTel`, `buildWebhookServer`, `buildMetricsServerOptions`, `resolveLeaderElectionNamespace`, `setupWebhooks`, `setupOTLPIngest`, `setupControllers`, `setupHealthChecks`). No behavior change; improves readability and removes the `// nolint:gocyclo` exception.

### Fixed

- **`IncidentReportStatus.StartTime` marker** — the deprecated `startTime` field was incorrectly marked `// +required`, which forced clients writing the new `firstObservedAt` field to also populate the deprecated one. It is now `// +optional`. CRD manifest regenerated.
- **JSON struct tags** — replaced `json:",omitzero"` (not recognised by `encoding/json`) with `json:",omitempty"` across `RCAAgent`, `IncidentReport`, and `RCACorrelationRule` types so omission behaviour matches the rest of the Kubernetes ecosystem.

---

## [0.0.16] — 2026-04-29

> **Phase 2 milestone** — OTLP ingest, distributed-trace correlation, service topology, dashboard redesign, auto-detected rules, and the first proper Prometheus metrics surface. CRD rule engine consolidates the entire correlation surface.

### Added

#### Phase 2 — OTel & topology

- **OTLP/HTTP receiver** (`internal/otelingest/`) — built-in `--otel-ingest-bind-address` endpoint accepts OTLP traces and logs, redacts configured PII fields, and translates spans/logs into correlator events. Filters via `--otel-ingest-filter-*` flags
- **Jaeger Query client** (`internal/jaeger/`) — `GetTrace` and `GetDependencies` wrappers used by the topology graph builder and the inline trace detail modal
- **Service-dependency topology** — `ServiceGraphBuilder` derives a Jaeger-style service call graph (service → service edges with call counts), exposed via `GET /api/service-graph`
- **Per-incident topology graph** — `IncidentReport.status.incidentGraph` serializes the blast-radius graph (K8s + trace + Jaeger enrichment) for the dashboard
- **`trace-id` / `trace-ids` / `fired-rule` annotations** — every IncidentReport carries up to 20 unique W3C trace IDs and the rule that fired
- **Inline Jaeger trace detail modal** — `GET /api/traces/{id}` wraps Jaeger Query and reshapes the response into a UI payload (summary tiles, ordered span waterfall, pinned error spans, per-service breakdown with overlap-merged intervals, per-span k8s/HTTP/DB tags). Trace IDs in the incident pane and service panel are clickable. Pod chips on spans deep-link into the Logs tab. Server cache TTL 5 min; responses bounded at 500 spans with error spans always preserved and `truncated: true` flagged
- **Service topology view** — second mode in the Topology tab renders the service dependency graph with edge call counts and an inbound/outbound/peers traffic block on the side panel
- **Auto fit-to-view** on first topology load — dense graphs that exceed the viewport are zoomed to fit; graphs that already fit are left at 100%
- **`sameTrace` correlation scope** — match conditions across services that share a W3C `trace_id` (CRD enum + rule engine support)
- **OTel attribute predicates** — `RuleCondition.attributes` (`AttributeMatch`) supports `Equals/NotEquals/Contains/NotContains/Regex/Exists/NotExists/Gte/Lte/Gt/Lt` against OTel resource/span/log attributes

#### Phase 1 metrics surface

- **`internal/metrics/` package** — first bespoke Prometheus collectors registered with controller-runtime: `rca_signals_received_total`, `rca_signals_deduplicated_total`, `rca_incidents_detecting_total`, `rca_incidents_activated_total`, `rca_incidents_resolved_total`, `rca_active_incidents` (gauge), `rca_incident_transition_seconds` (histogram). Wired into the correlator, reporter, and `incidentstatus.MarkActive/MarkResolved`

#### Auto-detection

- **Automatic correlation rule detection** — `internal/autodetect` mines the correlation buffer for recurring co-occurrence patterns and auto-creates `RCACorrelationRule` CRDs above an occurrence threshold. Includes pattern mining, accumulator, CRD lifecycle (create/update/expire), startup recovery, and Helm integration (`--enable-autodetect`, `--autodetect-*`)

#### CRD-driven correlation engine

- **`RCACorrelationRule`** cluster-scoped CRD — dynamic rule loading with template-based summaries; controller reloads on create/update/delete without operator restart
- **CRD rule engine** — factory-based plugin (priority 200) evaluating rules from CRDs at runtime
- **4 default correlation rules** shipped via Helm: `node-plus-eviction`, `crashloop-plus-oom`, `crashloop-plus-deploy`, `imagepull-no-history`
- **Helm hooks** — `post-install,post-upgrade` order CRDs before default rules

#### Workload coverage

- **StatefulSet, DaemonSet, Job, CronJob collectors** — detect stalled StatefulSet/DaemonSet rollouts, failed Jobs (`BackoffLimitExceeded`, `DeadlineExceeded`), and failed CronJob child runs, with incident resolution and signal mappings
- **Workload-scoped fingerprinting** — `Workload|ns|kind|name` format for proper dedup across pod restarts
- **Three-stage incident lookup** — `findOpenIncident` → `findResolvableIncident` → `findExistingByWorkloadRef` to guarantee zero duplicates
- **Signal processing pipeline** — Normalizer → Enricher → Rule Engine → Reporter architecture
- **`spec.signalMappings`** on `RCAAgent` for per-cluster event→incident-type overrides

#### Dashboard

- **Dashboard redesign** — clean UI with light/dark theme toggle (persisted to localStorage), Inter + JetBrains Mono fonts
- **Timeline API** — `GET /api/timeline?fingerprint=...` returns a unified chronological timeline across all lifecycle phases, including lifecycle transition events

#### Documentation & ops

- **Phase 1 architecture doc** (`docs/phases/PHASE1_ARCHITECTURE.md`) and ADR-0001
- **Metrics reference** (`docs/reference/metrics.md`)
- **OpenTelemetry export** — optional OTLP trace/metric export via `RCAAgent.spec.otel`
- **Leader election namespace flag** — `--leader-election-namespace` for explicit control; auto-detects out-of-cluster runs and defaults to `default`

### Changed

- **Leader election enabled by default** — `--leader-elect` defaults to `true` for production HA safety
- **All correlation rules are CRD-driven** — hardcoded Go rules removed from `internal/correlator/rules.go`
- **Self-describing incident types** — `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, `NodeNotReady`, `StalledRollout` (replacing the legacy aliases `OOM`, `Registry`, `NodeFailure`, `BadDeploy`)
- **`RCAAgent` simplified** to Phase 1 fields only
- **Secret validation** now targets real Slack + PagerDuty notification secrets (replacing legacy AI-settings paths)
- **Release workflows split** — `release.yml` (Docker + manifests), `helm-release.yml` (chart release), `helm-gh-pages.yml` (chart repo)
- **Webhook validation in sync with CRDs** — `RCACorrelationRule` admission webhook now allows `sameTrace`, OTel-derived event types, and the newer workload event types that the CRD enum already permitted

### Removed

- **Live Correlation Stream** bottom panel from the dashboard topology view — redundant with the Incidents tab. The `/api/stream` SSE endpoint is retained for external consumers
- All 4 hardcoded Go correlation rules (now CRD-driven)
- Legacy correlator rule engine factory (`internal/engine/correlator_rule_engine.go`)
- CPU throttling and `ResourceSaturation` incident paths that are outside the current architecture
- Stale AI/OpenAI setup guidance and watcher-first planning docs

### Fixed

- Duplicate incidents on operator restart (40h-resolved `StalledRollout` plus a new Active for the same workload)
- `StalledRollout` getting pod-scoped fingerprint instead of workload-scoped
- Enricher overwriting pre-existing `WorkloadRef` during scope resolution
- Helm install failure when CRDs and CRs ship in one release (post-install hooks)
- ExitCodePattern suppression — prevents duplicate incidents when exit codes build toward `ConsecutiveExitCode` threshold
- FrequencySpike auto-resolution guard — namespace-scoped incidents are no longer incorrectly auto-resolved by pod-name lookups
- Trace-modal Jaeger load — server-side 5-min TTL on `/api/traces/{id}`, dropped `noCache` on the client, capped span payload at 500 spans (preserving error spans)

### Test coverage

| Package | Before | After |
|---|---:|---:|
| `internal/metrics` | _(new)_ | 96.9% |
| `internal/webhook` | 0% | 94.4% |
| `internal/dashboard` | 15.9% | 63.2% |
| `internal/reporter` | 9.2% | 61.9% |

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
[Unreleased]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.16...HEAD
[0.0.16]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.15...v0.0.16
[0.0.5]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/gaurangkudale/RCA-Operator/compare/v0.0.1...v0.0.4
[0.0.1]: https://github.com/gaurangkudale/RCA-Operator/releases/tag/v0.0.1
