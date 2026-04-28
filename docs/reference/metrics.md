# Metrics Reference

RCA Operator currently exposes the standard **controller-runtime metrics endpoint** only. No bespoke `rca_*` Prometheus metrics are registered yet — the Phase 1 metric set listed in [Phase 1 Architecture](../phases/PHASE1_ARCHITECTURE.md) is on the roadmap but not implemented.

This document describes what is actually exported today and how to scrape it.

## What's Exposed

The metrics endpoint is wired via `sigs.k8s.io/controller-runtime/pkg/metrics/server`. It serves the controller-runtime defaults:

| Metric family | Source | Description |
|---|---|---|
| `controller_runtime_*` | controller-runtime | Reconcile counts, durations, queue depth, errors, per controller |
| `workqueue_*` | controller-runtime / client-go | Workqueue depth, latency, retries |
| `rest_client_*` | client-go | API server request latency, count, response codes |
| `go_*` / `process_*` | Prometheus Go client | Goroutines, GC, memory, file descriptors, CPU |
| `leader_election_master_status` | controller-runtime | `1` when this pod holds the leader lease, `0` otherwise |

Useful queries against the controller-runtime metrics:

```promql
# Reconcile rate per controller
sum by (controller) (rate(controller_runtime_reconcile_total[5m]))

# 95p reconcile duration
histogram_quantile(0.95, sum by (controller, le) (rate(controller_runtime_reconcile_time_seconds_bucket[5m])))

# Reconcile errors
sum by (controller) (rate(controller_runtime_reconcile_errors_total[5m]))

# Workqueue depth (per controller)
controller_runtime_workqueue_depth

# API throttling (client-go side)
sum(rate(rest_client_requests_total{code=~"4..|5.."}[5m]))
```

## CLI Flags

`cmd/main.go` exposes these metrics-related flags:

| Flag | Default | Description |
|---|---|---|
| `--metrics-bind-address` | `0` | `:8443` for HTTPS, `:8080` for HTTP, `0` to disable |
| `--metrics-secure` | `true` | Serve over HTTPS (BearerToken-protected) |
| `--metrics-cert-path` | _empty_ | Directory holding TLS cert + key (otherwise self-signed) |
| `--metrics-cert-name` | `tls.crt` | Cert filename inside `--metrics-cert-path` |
| `--metrics-cert-key`  | `tls.key` | Key filename inside `--metrics-cert-path` |

When `--metrics-secure=true` (default), the endpoint requires a valid `Authorization: Bearer <token>` header tied to a ServiceAccount with `nonResourceURLs: ["/metrics"]` `get`.

## Scraping

### In-cluster (Helm)

The Helm chart provisions a metrics `Service` and (optionally) a `ServiceMonitor`. See `helm/values.yaml` keys:

```yaml
metrics:
  enabled: true
  port: 8443
  secure: true
serviceMonitor:
  enabled: false  # set true to register with the Prometheus Operator
```

### Local development

```bash
# Disable TLS for easy scraping
make run ARGS="--metrics-bind-address=:8080 --metrics-secure=false"

curl http://localhost:8080/metrics | head
```

## Roadmap

The Phase 1 architecture document describes a planned set of `rca_*` metrics covering the incident lifecycle (signals received, deduplicated, transitions per phase, active incident gauge, etc.). These are **not** in the codebase today; if you build dashboards or alerts, do so against the controller-runtime / client-go metrics listed above. This page will be updated when bespoke metrics are wired through `internal/metrics/`.
