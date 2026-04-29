# Metrics Reference

RCA Operator exposes the standard **controller-runtime metrics endpoint** plus
RCA-specific Prometheus metrics for the incident lifecycle.

## What's Exposed

The metrics endpoint is wired via `sigs.k8s.io/controller-runtime/pkg/metrics/server`.
It serves the controller-runtime defaults:

| Metric family | Source | Description |
|---|---|---|
| `controller_runtime_*` | controller-runtime | Reconcile counts, durations, queue depth, errors, per controller |
| `workqueue_*` | controller-runtime / client-go | Workqueue depth, latency, retries |
| `rest_client_*` | client-go | API server request latency, count, response codes |
| `go_*` / `process_*` | Prometheus Go client | Goroutines, GC, memory, file descriptors, CPU |
| `leader_election_master_status` | controller-runtime | `1` when this pod holds the leader lease, `0` otherwise |

RCA-specific metrics:

| Metric | Type | Labels | Description |
|---|---|---|---|
| `rca_signals_received_total` | Counter | `event_type` | Signals accepted by the correlator pipeline |
| `rca_signals_deduplicated_total` | Counter | `event_type` | Signals suppressed by IncidentReport deduplication |
| `rca_incidents_detecting_total` | Counter | `incident_type`, `severity` | IncidentReports created in `Detecting` phase |
| `rca_incidents_activated_total` | Counter | `incident_type`, `severity` | Incidents promoted from `Detecting` to `Active` |
| `rca_incidents_resolved_total` | Counter | `incident_type`, `severity` | Incidents resolved from `Detecting` or `Active` |
| `rca_active_incidents` | Gauge | `incident_type`, `severity` | Currently non-resolved incidents |
| `rca_incident_transition_seconds` | Histogram | `from_phase`, `to_phase` | Time spent before lifecycle transitions |

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

Useful RCA queries:

```promql
# Active incidents by severity
sum by (severity) (rca_active_incidents)

# New active incidents by type over 15 minutes
sum by (incident_type) (increase(rca_incidents_activated_total[15m]))

# 95p detecting-to-active transition time
histogram_quantile(
  0.95,
  sum by (le) (rate(rca_incident_transition_seconds_bucket{from_phase="detecting",to_phase="active"}[15m]))
)

# Signals entering the pipeline
sum by (event_type) (rate(rca_signals_received_total[5m]))
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

The Helm chart enables the manager metrics endpoint and provisions a metrics
`Service` when `metrics.enabled=true`:

```yaml
metrics:
  enabled: true
  secure: true
  service:
    port: 8443
    type: ClusterIP
```

With `secure: true`, configure your scraper with a ServiceAccount token that
can read the `/metrics` non-resource URL.

### Local development

```bash
# Disable TLS for easy scraping
make run ARGS="--metrics-bind-address=:8080 --metrics-secure=false"

curl http://localhost:8080/metrics | head
```
