# RCA Exporter (Phase 2)

The **RCA Exporter** is the Phase-2 ingestion service for RCA-Operator. It runs as a separate Deployment alongside the in-cluster operator manager and turns **logs / traces / Kubernetes change events** into the same `IncidentReport` CRDs the operator's Phase-1 controllers already reconcile, dashboard, and route to notifications.

> **One-line summary:** Phase 1 watches the Kubernetes API. Phase 2 listens to OTLP. Both produce the same `IncidentReport` CR, so the dashboard and notification pipeline see one unified stream.

## Why a separate binary?

| Concern | Phase 1 (`rca-operator` manager) | Phase 2 (`rca-exporter`) |
|---|---|---|
| Signal source | K8s API (Pods, Nodes, Events) via informers | OTLP logs (later: traces, change events) |
| Run mode | controller-runtime manager + reconcilers | stateless gRPC/HTTP server, no leader election |
| RBAC surface | broad: Pods, Nodes, Events, Deployments, ... | minimal: only `incidentreports` |
| Failure blast radius | controller loop crash stops reconciliation | exporter crash stops *new* spike detection only |
| Vendor lock-in | none (uses K8s API) | **none — no Prometheus, no SaaS SDK** |

The exporter is intentionally a thin client of the existing `internal/reporter` library so the entire incident lifecycle (dedup, reopen, cooldown, status patching) is shared with Phase 1 — no fork, no copy-paste.

## Architecture

```
Pods (stdout/stderr)
  └── containerd
       └── /var/log/containers/*.log on each node
            └── Fluent Bit DaemonSet (tail + kubernetes filter)
                 └── OpenTelemetry Collector
                      ├── k8sattributes processor (adds k8s.namespace.name, k8s.pod.name, ...)
                      ├── batch + memory_limiter
                      └── otlp / otlphttp exporter
                           │
                           ▼
                  ┌────────────────────────────────────────────┐
                  │  rca-exporter Deployment (Phase 2)         │
                  │  ┌──────────────────────────────────────┐  │
                  │  │  OTLP gRPC :4317  +  HTTP :4318      │  │
                  │  └──────────────┬───────────────────────┘  │
                  │                 ▼                          │
                  │  ┌──────────────────────────────────────┐  │
                  │  │  ErrorRateAggregator                 │  │
                  │  │  per-(namespace,service) sliding     │  │
                  │  │  window + cooldown gate              │  │
                  │  └──────────────┬───────────────────────┘  │
                  │                 ▼ LogErrorSpikeEvent       │
                  │  ┌──────────────────────────────────────┐  │
                  │  │  bridge → reporter.EnsureIncident    │  │
                  │  └──────────────┬───────────────────────┘  │
                  └─────────────────┼──────────────────────────┘
                                    ▼
                            Kubernetes API
                                    │
                                    ▼
                  IncidentReport CR  ──▶  IncidentReportReconciler
                                          (Phase 1 controllers, dashboard,
                                           notification pipeline — unchanged)
```

## What's in this folder

| File | What it covers |
|---|---|
| [`README.md`](README.md) | This file — overview, architecture, feature matrix |
| [`usage.md`](usage.md) | End-to-end deploy on a local kind cluster, including Fluent Bit + OTel Collector wiring |
| [`api.md`](api.md) | OTLP gRPC + HTTP API reference, content types, gzip, examples with `grpcurl` and `curl` |
| [`development.md`](development.md) | How to build, run, and test locally; package layout; how to add new detectors |
| [`todos.md`](todos.md) | Roadmap: traces, change events, multi-replica, pattern detection |

Phase 1 docs live alongside in [`docs/phases/PHASE1_ARCHITECTURE.md`](../phases/PHASE1_ARCHITECTURE.md) and [`docs/development/local-setup.md`](../development/local-setup.md).

## Features (today)

| Feature | Status | Notes |
|---|---|---|
| OTLP/gRPC log ingestion (`:4317`) | ✅ shipped | Default OTLP port; what the OTel Collector's `otlp` exporter sends to |
| OTLP/HTTP log ingestion (`:4318`) | ✅ shipped | Default OTLP port; what the OTel Collector's `otlphttp` exporter and browser SDKs send to |
| Protobuf request bodies | ✅ shipped | `application/x-protobuf` (default) |
| JSON request bodies | ✅ shipped | `application/json` via canonical protojson — used by JS/browser SDKs |
| gzip Content-Encoding | ✅ shipped | Transparently inflated; required by collector batches ≥ 1 KiB |
| Per-service sliding-window error-rate detection | ✅ shipped | Configurable `--error-window` and `--error-threshold` |
| Cooldown gate (no incident thrash) | ✅ shipped | Defaults to 5m, matching `reporter.SignalCooldown` |
| Sample message capture | ✅ shipped | Last N error messages embedded in the IncidentReport summary |
| Service-scoped dedup | ✅ shipped | A spike across N replicas produces 1 incident, not N |
| Reuse of Phase-1 incident lifecycle | ✅ shipped | Dedup, reopen, cooldown, status patching all inherited from `internal/reporter` |
| OTel self-traces | ✅ shipped | Honors `OTEL_EXPORTER_OTLP_ENDPOINT` so the exporter's own traces flow through the same collector |
| Health probes (`/healthz`, `/readyz`) | ✅ shipped | Exposed on `:8081` (configurable) |
| Minimal RBAC (`incidentreports` only) | ✅ shipped | No pod/node/event read permissions |
| Distroless container image | ✅ shipped | `Dockerfile.exporter` builds a `gcr.io/distroless/static:nonroot` image |
| Make targets for build / run / kind / deploy | ✅ shipped | See [`development.md`](development.md) and `make help` |

## Features (planned, see [`todos.md`](todos.md))

- OTLP traces ingestion → trace-error-spike detection
- Kubernetes change tracking (Deployments, ConfigMaps, Secrets) for "deployment-caused error spike" correlation
- Cross-source correlation rules via the existing `RCACorrelationRule` CRD
- Pattern-based detection (regex / template extraction) on top of pure error-rate
- Multi-replica horizontal scaling (consistent hashing or shared backend)
- Self-metrics published via OTel meter (no Prometheus dependency)

## Vendor lock-in posture

The exporter has **zero hard dependency on Prometheus or any SaaS observability vendor** for its signal-source path. It speaks the open OTLP protocol on both gRPC and HTTP, so any compliant upstream — Fluent Bit, the OTel Collector, vendor agents, custom forwarders, or browser SDKs — works without modification. The only Prometheus symbols compiled into the binary come transitively through `internal/reporter` (which Phase 1 still uses for its own self-metrics) and are never exposed on a scrape endpoint by the exporter itself.

This is a deliberate Phase-2 design constraint: **logs are ground truth, traces are causality, K8s state is change context**. None of those require a TSDB.

## Quick links

- **Try it on kind in 5 minutes:** [`usage.md → Local kind walkthrough`](usage.md#local-kind-walkthrough)
- **Send a synthetic OTLP request:** [`api.md → curl examples`](api.md#sending-a-synthetic-otlp-http-request-with-curl)
- **Add a new detector:** [`development.md → Adding a new detector`](development.md#adding-a-new-detector)
