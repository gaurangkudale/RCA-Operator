# RCA Exporter — Roadmap & Open TODOs

This document tracks **planned work** for the exporter beyond the MVP that ships in this PR. Items are grouped by theme and roughly ordered by priority within each section. Anything marked **good first issue** is a self-contained scope appropriate for a new contributor.

The MVP delivered in this PR is intentionally narrow — OTLP logs ingestion + per-service error-rate detection — so each item below represents a deliberate "later" decision, not an oversight.

---

## Protocol additions

### OTLP traces ingestion
**Why:** Logs tell you *what* broke; traces tell you *where* in the call graph it broke. The exporter already speaks OTLP/gRPC and OTLP/HTTP for logs — adding a `TraceService.Export` handler on the same two ports is mostly mechanical because `go.opentelemetry.io/proto/otlp/collector/trace/v1` is already a transitive dep via the trace SDK.

**Scope:**
- New `internal/exporter/ingest/otlp_traces.go` mirroring `otlp_logs.go`
- New `aggregator/trace_error_rate.go` that counts spans with `status.code == ERROR` per `(service, operation)` pair
- New event type `TraceErrorSpikeEvent` and a bridge entry for `incidentType=TraceErrorSpike`
- Reuse the existing flag set: `--otlp-grpc-addr` and `--otlp-http-addr` already cover the wire — no new ports needed (OTLP multiplexes services on the same gRPC server).

### OTLP metrics ingestion (deliberately deferred)
**Why deferred:** Metrics ingestion is the gateway drug to becoming a TSDB, which is exactly the lock-in posture Phase 2 is trying to avoid. If you find yourself wanting metrics ingestion, the right answer is almost always "send the metrics to a TSDB and have it call the exporter's webhook" — see the [Webhook ingestion](#webhook-ingestion) item.

### Webhook ingestion
**Why:** Some teams already have an alerting pipeline (Prometheus Alertmanager, Datadog monitors, PagerDuty escalations) and want to forward fired alerts into the RCA correlation graph without re-piping the underlying telemetry.

**Scope:**
- `POST /v1/webhook/alertmanager` accepting Alertmanager's JSON envelope
- `POST /v1/webhook/generic` accepting a documented minimal schema (`source`, `severity`, `summary`, `service`, `namespace`, `dedupKey`)
- New event type `ExternalAlertEvent` → `incidentType=ExternalAlert`
- Bearer-token auth via a `--webhook-token-file` flag (the only ingress that should ever be authenticated, since webhooks can come from outside the cluster)

### Kubernetes change tracking
**Why:** "Deployment X went out at 10:42 and error rate spiked at 10:43" is the single highest-leverage RCA signal. The Phase-1 manager already watches Deployments via `internal/watcher/deployment_watcher.go`, but the data does not yet feed the exporter's correlator.

**Scope:**
- Add a thin in-cluster watcher inside the exporter for `Deployments`, `ConfigMaps`, and `Secrets` (read-only — the exporter still does not need write RBAC for these)
- New event type `K8sChangeEvent` carrying `kind`, `name`, `namespace`, `revision`, `changedAt`
- A correlation rule that elevates `LogErrorSpike` to severity P2 when a `K8sChangeEvent` for the same namespace happened within the last 10 minutes
- This is the first place the exporter needs to talk to the existing `internal/correlator` rule engine — see [Cross-source correlation](#cross-source-correlation) below

---

## Detection improvements

### Pattern-based detection
**Why:** Pure error-rate detection misses low-volume but high-signal patterns ("five distinct services all logged `database connection refused` in the last minute"). Pattern detection groups records by extracted templates instead of by service.

**Scope:**
- `internal/exporter/aggregator/pattern_match.go` running a [drain3](https://github.com/IBM/Drain3)-style log template extractor
- Configurable pattern allow-list for high-cost regexes
- New event type `LogPatternMatchEvent` with the template and the matching services
- This is the largest single new feature on the roadmap — likely a separate PR with its own design doc

### Severity scaling by service tier
**Why:** A 10-error spike in `auth-service` is P1; a 10-error spike in `dev-bench-tool` is P4. The exporter currently has one global threshold and emits everything as P3.

**Scope:**
- New flag `--service-tier-config` pointing at a YAML/JSON file mapping service-name globs to severity floors
- When a spike fires, look up the service in the tier table and use that severity instead of the hard-coded P3
- **good first issue** — small, self-contained, no architectural changes

### Anomaly-based thresholds
**Why:** Static thresholds are wrong for everything. A service that normally produces 50 errors/min is healthy at 50 errors/min and broken at 10 errors/min if it usually does 5. Z-score or EWMA-based thresholds adapt automatically.

**Scope:**
- Per-service rolling baseline (mean + stddev over the last 24h)
- Fire when current rate exceeds `baseline + N*stddev` for K consecutive windows
- Persist baselines somewhere durable so they survive pod restarts (probably a ConfigMap, definitely not Prometheus)
- This depends on first solving the [horizontal scaling](#multi-replica-horizontal-scaling) problem because per-pod baselines don't compose

---

## Lifecycle & operations

### Multi-replica horizontal scaling
**Why:** The MVP holds all per-service windows in memory, so two replicas would each see half the records and miss spikes. This caps the exporter at whatever a single pod can handle (~10–50k records/sec).

**Scope:**
- Consistent hashing on `(namespace, service)` so each replica owns a deterministic subset
- A small in-cluster gossip layer (or just a headless Service + DNS lookup) so replicas know about each other
- A failover protocol that handles a replica disappearing without dropping its records on the floor
- This is the largest single architectural change on the roadmap — defer until a real user hits the single-replica ceiling

### Self-metrics via OTel meter
**Why:** The exporter currently has no internal observability beyond logs. We need at minimum: records-received-per-second, spikes-fired, aggregator-depth, last-OTLP-timestamp.

**Scope:**
- Use the OTel SDK's meter (already initialized in `internal/otel/setup.go`) — **not** a Prometheus client
- Export via the same OTLP endpoint the rest of the stack uses
- Wire the metrics into the readiness gate so `/readyz` fails when `last-OTLP-timestamp > 5 * window` (tells the kubelet that upstream has gone quiet)
- **good first issue** for the metrics surface; the readiness wiring is a follow-up

### Dynamic config reload
**Why:** Tuning thresholds currently requires a Deployment rollout. For incident response, you want to be able to lower a threshold without restarting the pod.

**Scope:**
- Read tunable flags from a `ConfigMap` mounted at `/etc/rca-exporter/config.yaml`
- `fsnotify` watch on the file → call `aggregator.SetConfig(...)` atomically
- Flag values become defaults; ConfigMap overrides
- **good first issue** if scoped to threshold/window/cooldown only

### Helm sub-chart
**Why:** The exporter currently ships as raw kustomize manifests under `config/rca-exporter/`. Most production users want a Helm chart so they can `helm install` and `helm upgrade` without managing kustomize themselves.

**Scope:**
- New `helm/rca-exporter/` sub-chart structured as a sibling to the existing `helm/rca-operator` chart
- Values for `image`, `replicaCount`, `resources`, `errorThreshold`, `errorWindow`, `agentRef`
- Optional dependency on the `opentelemetry-collector` chart so users can install both atomically
- A CI test that does `helm template` + `kubeval` to catch schema regressions

---

## Correlation & incident shaping

### Cross-source correlation
**Why:** The biggest unrealized win is "Phase-1 K8s events + Phase-2 log spikes + Phase-2 trace errors all point at the same root cause". The Phase-1 `internal/correlator` package already implements rule-based correlation but only sees K8s events.

**Scope:**
- Teach `internal/signals.Normalizer` and `internal/signals.Enricher` about `LogErrorSpikeEvent` and `TraceErrorSpikeEvent`, OR introduce a new "exporter consumer" that bypasses the K8s-typed normalizer and feeds the rule engine directly
- Add new correlation rules in `rules.go`:
  - LogErrorSpike + recent Deployment rollout → `BadDeploy/P2` (elevation)
  - LogErrorSpike + ImagePullBackOff in same namespace → `Registry/P2` (elevation)
  - TraceErrorSpike on service A + LogErrorSpike on service B → `UpstreamFailure/P2` linking the two
- This was deferred from the MVP plan because it requires teaching the normalizer about non-K8s event types — non-trivial scope

### Resolution signals from logs
**Why:** Today, a `LogErrorSpike` IncidentReport is created when errors arrive but only resolves when the Phase-1 reconciler's stabilization window elapses. We could resolve faster by listening for the *absence* of errors.

**Scope:**
- Aggregator emits a `LogErrorRecoveryEvent` when a previously-spiking service has zero errors for `--recovery-window` (default 5 min)
- Bridge calls `reporter.ResolveIncident(ctx, dedupKey, "log error rate normalized")`
- Need to be careful about flapping — the cooldown gate must apply to recoveries too

### IncidentReport summary improvements
**Why:** The current summary string is `N errors in Ws for ns/svc (threshold T): msg1 | msg2 | msg3` which is fine for Slack but misses obvious enrichments.

**Scope:**
- Group sample messages by similarity (cluster the last 100, show top 3 clusters with counts)
- Include the most recent successful log timestamp ("last healthy log: 2m ago") as a triage hint
- Link to a Grafana/Loki query if the user provides a `--logs-query-template` flag
- **good first issue** for the message clustering — pure local logic, well-isolated

---

## Testing & CI

### End-to-end kind smoke test in CI
**Why:** The kind walkthrough in `usage.md` is currently manual. CI should run it on every PR so we catch wiring regressions before they hit users.

**Scope:**
- New GitHub Actions workflow `.github/workflows/exporter-e2e.yml`:
  1. `make kind-deploy-all`
  2. Deploy a synthetic ERROR-emitting pod
  3. Wait for an `IncidentReport` of type `LogErrorSpike` to appear
  4. Tear down
- Time budget: under 5 minutes total or it gets skipped on every PR
- **good first issue** if you're comfortable with GH Actions

### Load test harness
**Why:** We have no published numbers for "how many records/sec can a single replica handle". The "10–50k records/sec" claim in `api.md` is informed estimate, not measurement.

**Scope:**
- A `cmd/rca-exporter-loadgen/` helper that fires synthetic OTLP requests at a target rate
- A make target `make loadtest-exporter` that boots the binary, runs the loadgen for 60s, prints throughput / p99 latency / max RSS
- Document the results in `api.md`'s "Production sizing notes" section so users can plan capacity

### Fuzz tests on the OTLP receivers
**Why:** Both `otlp_logs.go` and `otlp_logs_http.go` parse untrusted input. Standard library `proto.Unmarshal` and `protojson.Unmarshal` are battle-tested but our flattening logic and severity classification are not.

**Scope:**
- `FuzzExportRequest` in `otlp_logs_fuzz_test.go` feeding random bytes to `proto.Unmarshal` followed by `LogsReceiver.Export`
- `FuzzHTTPHandler` doing the same for the HTTP path
- Run for 60s in CI on every PR, longer nightly
- **good first issue**

---

## Documentation

### Architecture decision records
**Why:** The README explains *what* the exporter does; we don't currently have a place that explains *why* we made specific tradeoffs (no Prometheus, hand-rolled OTLP receiver instead of importing the collector, single-replica MVP, etc.). Future contributors will re-litigate these decisions if we don't write them down.

**Scope:**
- New `docs/exporter/adr/` directory with one markdown file per decision
- ADR-001: No Prometheus dependency
- ADR-002: Hand-rolled OTLP receiver
- ADR-003: Single-replica MVP
- ADR-004: Severity floor at SeverityNumber 17
- **good first issue** — these are mostly extractions from existing comments in the code

### Migration guide for Phase-1-only users
**Why:** Existing users who only run the Phase-1 manager need a clear "here's what changes when you turn on the exporter" doc. Specifically: which incidents will now have `agentRef=rca-exporter`, which alerts will start firing that previously didn't, and how to filter in the dashboard.

**Scope:**
- New `docs/exporter/migration.md`
- Concrete `kubectl get incidentreports` queries showing the before/after
- Notes on the dashboard filter changes

---

## Things we deliberately won't do

These appear obvious but have been considered and rejected. Listed here so future contributors don't waste time pitching them.

- **Prometheus scrape endpoint.** Vendor lock-in, contradicts the Phase-2 design constraint. Use OTel for self-metrics.
- **Built-in log storage / search.** Loki, OpenSearch, and CloudWatch Logs already exist. The exporter is a real-time detector, not a TSDB.
- **A web UI.** The Phase-1 dashboard already shows IncidentReports — there is no second UI to maintain.
- **Custom CRDs.** Reusing `IncidentReport` is the entire point of Phase 2. New event types live as enum values, not as new CRDs.
- **Inline transformation rules** ("rewrite this log message before classifying"). That is the upstream OTel Collector's job — see its `transform` processor.

---

## How to pick something up

1. Pick an item — **good first issue** items are explicitly scoped for new contributors
2. Open a GitHub issue referencing this file's section so we can avoid duplicate work
3. Read the relevant section of [`development.md`](development.md) for the local loop
4. Send a PR — we prefer many small PRs over one large one, especially for the roadmap items above which often touch multiple files
