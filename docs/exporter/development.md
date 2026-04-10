# Developing the RCA Exporter

This document is for contributors who want to **build, run, test, or extend** the exporter. For deployment instructions read [`usage.md`](usage.md); for the protocol contract read [`api.md`](api.md).

---

## Prerequisites

- Go 1.25+ (matches `Dockerfile.exporter`)
- Docker (for `docker-build-exporter` and `kind load`)
- [`kind`](https://kind.sigs.k8s.io) for local end-to-end runs
- `kubectl` configured against any cluster (kind is fine)
- Optional: [`grpcurl`](https://github.com/fullstorydev/grpcurl) for hand-firing OTLP/gRPC requests

The repository is a single Go module — `go.mod` at the root — so the exporter shares dependencies with the Phase-1 manager. There is no separate workspace to set up.

---

## Repository layout

The exporter lives entirely under three top-level directories so it is easy to grep, audit, and (eventually) extract into its own module.

```
cmd/
  rca-exporter/
    main.go                       # binary entrypoint, flag parsing, signal handling

internal/exporter/
  events/
    log_events.go                 # LogErrorSpikeEvent + DedupKey contract
  aggregator/
    error_rate.go                 # per-service sliding-window detector
    error_rate_test.go            # 7 unit tests (table-driven, fakeClock)
  bridge/
    bridge.go                     # aggregator → reporter.EnsureIncident wiring
  ingest/
    otlp_logs.go                  # OTLP/gRPC LogsServiceServer
    otlp_logs_test.go             # bufconn integration test (full stack)
    otlp_logs_http.go             # OTLP/HTTP handler + Serve wrapper
    otlp_logs_http_test.go        # 8 HTTP tests (encoding, gzip, errors)

config/rca-exporter/
  deployment.yaml                 # exporter Deployment (gRPC + HTTP + health ports)
  service.yaml                    # ClusterIP Service exposing 4317 / 4318 / 8081
  rbac.yaml                       # SA + minimal ClusterRole (incidentreports only)
  kustomization.yaml              # bundles the four manifests
  otel-collector-example.yaml     # reference upstream config (not deployed)

Dockerfile.exporter               # multi-stage distroless build
```

The exporter does **not** import:

- `internal/watcher` — no in-cluster watching
- `internal/metrics` — no Prometheus
- `controller-runtime/pkg/manager` — no controller loops, no leader election

It does import:

- `internal/reporter` — full IncidentReport lifecycle (dedup, reopen, cooldown, status patching)
- `internal/otel` — self-trace setup, identical to the Phase-1 manager
- `api/v1alpha1` — the IncidentReport CRD types

This is a deliberate constraint: the exporter is a thin client of existing libraries, not a fork.

---

## Build, run, test

All workflows are wrapped in `make` targets so contributors don't have to memorize Go invocations. Run `make help` to see them grouped under `RCA Exporter (Phase 2)` and `Kind (local end-to-end)`.

### Build

```bash
make build-exporter
# → bin/rca-exporter

# With a custom output path / extra flags:
GOFLAGS='-tags=foo' make build-exporter
```

### Run locally (out-of-cluster)

```bash
# Uses your current kubeconfig context to write IncidentReports
make run-exporter

# Or with custom args:
make run-exporter EXPORTER_ARGS='--error-threshold=3 --error-window=30s --otlp-grpc-addr=:14317'
```

The binary picks up the kubeconfig via `ctrl.GetConfig()` (same path as `kubectl`), so you do not need a service account — your `~/.kube/config` user is what writes the IncidentReports.

### Test

```bash
# Run all exporter tests:
make test-exporter

# Equivalent to:
go test ./internal/exporter/... ./cmd/rca-exporter/...

# Single-test debugging:
go test -run TestErrorRateAggregator_AtThresholdFiresOnce ./internal/exporter/aggregator/ -v
```

### Container image

```bash
# Build a distroless image tagged rca-exporter:latest
make docker-build-exporter

# Build and push to a registry:
make docker-build-exporter docker-push-exporter \
  EXPORTER_IMG=ghcr.io/your-org/rca-exporter:v0.1.0
```

The Dockerfile is `Dockerfile.exporter` (separate from the manager's `Dockerfile`) so the two binaries can evolve independently.

---

## Test strategy

The exporter has two layers of test coverage:

### Unit tests — `internal/exporter/aggregator/error_rate_test.go`

Cover the detection logic with no I/O. Use a `fakeClock` injected via `Config.Now` so tests can advance time deterministically without `time.Sleep`. Cases:

1. Below threshold → no fire
2. At threshold → fires exactly once
3. Cooldown suppresses second fire within the cooldown window
4. Old entries pruned out of the sliding window
5. Different services tracked independently
6. Sample messages bounded to `SampleSize`
7. Records without `Service` are silently dropped

These run in milliseconds and have no Kubernetes dependency.

### Integration tests — `internal/exporter/ingest/otlp_logs_test.go` and `otlp_logs_http_test.go`

Build the full stack (aggregator → bridge → reporter → fake k8s client → receiver → grpc/http server) and exercise it via real gRPC over `bufconn` and real HTTP over `httptest.Server`. Cases:

- **gRPC**: send 3 ERROR records, assert an `IncidentReport` of type `LogErrorSpike` exists in the fake client with `agentRef=rca-exporter`, `severity=P3`. Negative test: INFO records create no incident.
- **HTTP**: protobuf body, JSON body, gzipped body, charset suffix in Content-Type, all error paths (405 / 400 / 415), response body is a valid `ExportLogsServiceResponse`, full `ServeHTTP` lifecycle on `127.0.0.1:0`.

The fake client comes from `sigs.k8s.io/controller-runtime/pkg/client/fake` — no envtest required. Tests run in well under a second.

### What is intentionally NOT tested

- **Real OTel Collector wiring.** That is exercised manually via the kind walkthrough in `usage.md` and (eventually) by a make target that boots a kind cluster, deploys the collector, fires logs, and asserts an IncidentReport — see [`todos.md`](todos.md).
- **Reporter internals.** Already covered by the existing `internal/reporter` test suite. The exporter tests assert the *visible result* (an IncidentReport CR) rather than re-testing dedup/reopen logic.

---

## Adding a new detector

The exporter is built around a single detector — `aggregator.ErrorRateAggregator` — but the architecture is designed to host more. To add one (e.g. `LogPatternMatchAggregator` or `TraceErrorSpikeAggregator`):

### 1. Define the event type

Add a new struct to `internal/exporter/events/` that satisfies the `watcher.CorrelatorEvent` interface:

```go
type LogPatternMatchEvent struct {
    Service   string
    Namespace string
    Pattern   string
    Count     int
    Occurred  time.Time
}

func (e LogPatternMatchEvent) Type() watcher.EventType { return EventTypeLogPatternMatch }
func (e LogPatternMatchEvent) OccurredAt() time.Time   { return e.Occurred }
func (e LogPatternMatchEvent) DedupKey() string {
    return "LogPatternMatch:" + e.Namespace + ":" + e.Service + ":" + e.Pattern
}
```

The `DedupKey()` is what the reporter uses to deduplicate, so design it carefully — too coarse and you collapse distinct incidents, too fine and you spam the dashboard.

### 2. Implement the detector

Create a new package under `internal/exporter/aggregator/` (or a sibling if the structure is very different). It should accept a `LogRecord`-like input via an `Observe` method and call back into a `func(YourEvent)` handler when the detection condition is met. **Do not** call the reporter directly — keep the detector pure so it can be unit-tested with no I/O.

Mirror `error_rate.go`'s patterns:

- One `Config` struct holding tunables, including a `Now func() time.Time` for clock injection
- A single `sync.Mutex` for the MVP — sharded locking is a follow-up
- Cooldown gate so the same condition doesn't refire continuously
- Sample retention for triage strings

### 3. Bridge to the reporter

Add a small wiring function (or extend `internal/exporter/bridge/bridge.go`) that converts your event into a `reporter.EnsureIncident` call. Pick:

- An `incidentType` string that the dashboard knows about — register it in the reporter constants if it's truly new
- A starting `severity` (`P1`–`P4`) that reflects "this signal alone, without correlation". Cross-source elevation is the correlator's job.
- A summary string short enough to fit in a Slack notification

### 4. Wire it into `cmd/rca-exporter/main.go`

Construct the new detector alongside `aggregator.New(...)` and feed its output into the same bridge. If the detector needs OTLP records, route them through `LogsReceiver` by extending `Export` (which currently dispatches to a single aggregator) to fan out to multiple sinks.

### 5. Test

- Unit-test the detector in isolation with a fake clock — same pattern as `error_rate_test.go`
- Add an integration test that round-trips an OTLP request and asserts your new IncidentReport type appears in the fake k8s client
- Manually smoke-test on kind with `make kind-deploy-all`

### 6. Document

Update [`README.md`](README.md)'s feature matrix and add the flag to [`usage.md`](usage.md)'s configuration reference table. New incident types should also be linked from [`api.md`](api.md)'s severity classification section.

---

## Adding a new ingestion transport

If you want to accept signals from somewhere other than OTLP (e.g. a Kafka topic, a webhook, a syslog stream):

1. **Reuse the canonical proto type if possible.** Even non-OTel sources can be normalized to `ExportLogsServiceRequest` upstream — that lets the existing `LogsReceiver.Export` do all the work.
2. **Otherwise, write a new receiver in `internal/exporter/ingest/`** that converts the source format into `aggregator.LogRecord` and calls `agg.Observe` directly.
3. **Register a new flag** in `cmd/rca-exporter/main.go` (e.g. `--syslog-addr`) following the gRPC/HTTP pattern: empty string disables, non-empty enables, error if every transport is disabled.
4. **Add a new section to `api.md`** documenting the wire format and example clients.

The aggregator does not care which transport produced a record — it only sees `aggregator.LogRecord` — so transports compose freely.

---

## Coding conventions

- **No new dependencies without a write-up.** This module is intentionally lean. The exporter MVP added zero direct deps because `go.opentelemetry.io/proto/otlp` was already an indirect dep via the trace SDK. Before adding any new import, check whether the equivalent can be done with the standard library or a transitive package already in `go.sum`.
- **Constructors return concrete types, not interfaces.** Tests substitute fakes via field injection (`Config.Now`), not via mocking frameworks.
- **Errors are propagated with `fmt.Errorf("...: %w", err)`** so callers can `errors.Is` / `errors.As` upstream.
- **Logs use `logr.Logger`**, never `fmt.Println` or the standard `log` package — controller-runtime's logger is the only sink.
- **No `panic` in library code.** The exporter is a long-running server; a panic is a process restart. Return errors and let `cmd/rca-exporter/main.go` decide whether to `os.Exit`.
- **Use `_test` package suffixes only when testing the public API.** Most exporter tests live in the same package as the code under test so they can poke at unexported helpers.
- **Comments explain *why*, not *what*.** The code already says what; comments should answer "why this and not the obvious alternative". Look at `internal/exporter/ingest/otlp_logs.go` for the style.

---

## Local end-to-end loop

```bash
# 1. Build images and load them into kind in one shot
make kind-deploy-all

# 2. Tail the exporter logs in another terminal
kubectl -n rca-operator-system logs -l app.kubernetes.io/name=rca-exporter -f

# 3. Make a code change

# 4. Rebuild and reload just the exporter image (faster than full kind-deploy-all)
make docker-build-exporter kind-load-exporter
kubectl -n rca-operator-system rollout restart deploy/rca-exporter

# 5. Re-fire your test traffic and watch incidents appear
kubectl get incidentreports -A -w
```

The full loop (code change → image rebuild → reload → restart) takes ~30s on a recent laptop. For pure detector logic changes, prefer `go test ./internal/exporter/...` — no kind round-trip needed.

---

## Where to look first if something breaks

| Symptom | First place to look |
|---|---|
| Build fails with missing OTLP types | `go mod tidy` — the `go.opentelemetry.io/proto/otlp` indirect dep may need to be promoted to direct |
| Tests pass but no IncidentReport in kind | `kubectl -n rca-operator-system logs -l app.kubernetes.io/name=rca-exporter` — look for "service.name not set" warnings |
| `incidentreports.rca.rca-operator.tech is forbidden` | `config/rca-exporter/rbac.yaml` — re-run `make deploy-exporter` |
| HTTP receiver returns 415 from a known-good client | `contentType()` in `otlp_logs_http.go` — check the Content-Type header is in the accepted list |
| Aggregator never fires despite enough errors | inject a fake clock in a unit test that mirrors the production flow; the sliding window is the most subtle code in the package |

---

## Next steps

- Read [`api.md`](api.md) for protocol details when extending the receiver
- Read [`todos.md`](todos.md) for the prioritized roadmap and "good first issue" candidates
- Read [`README.md`](README.md) for the architectural overview if you joined this PR cold
