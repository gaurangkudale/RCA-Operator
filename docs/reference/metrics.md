# Metrics Reference

RCA Operator exposes Prometheus-compatible metrics via the controller-runtime metrics endpoint. These are available when `metrics.enabled: true` in the Helm values (default).

## Phase 1 Incident Lifecycle Metrics

These metrics track the core incident pipeline from signal ingestion through resolution.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `rca_signals_received_total` | Counter | `event_type`, `agent` | Total signals entering the pipeline |
| `rca_signals_deduplicated_total` | Counter | `event_type` | Signals suppressed by the deduplication window |
| `rca_incidents_detecting_total` | Counter | `agent`, `incident_type`, `severity` | Incidents that entered the Detecting phase |
| `rca_incidents_activated_total` | Counter | `agent`, `incident_type`, `severity` | Incidents promoted from Detecting to Active |
| `rca_incidents_resolved_total` | Counter | `agent`, `incident_type`, `severity` | Incidents that reached Resolved |
| `rca_active_incidents` | Gauge | `agent`, `incident_type`, `severity` | Current number of Active (non-resolved) incidents |
| `rca_incident_transition_seconds` | Histogram | `from_phase`, `to_phase` | Duration of phase transitions (detecting to active, active to resolved, detecting to resolved) |

### Histogram Buckets

`rca_incident_transition_seconds` uses these buckets (in seconds): 10, 30, 60, 120, 300, 600, 1800, 3600.

## Operational Metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `rca_notifications_sent_total` | Counter | `channel`, `action`, `outcome`, `severity` | Notification attempts by channel (slack, pagerduty), action (trigger, resolve), and outcome (success, error) |
| `rca_notification_duration_seconds` | Histogram | `channel` | Duration of notification dispatch |
| `rca_signal_processing_duration_seconds` | Histogram | `event_type` | Duration of signal processing in the pipeline |
| `rca_rule_evaluations_total` | Counter | `rule_name`, `fired` | Correlation rule evaluations with fired=true/false |
| `rca_correlation_buffer_size` | Gauge | `agent` | Current number of events in the correlation buffer |

## Auto-Detection Metrics

These metrics are only active when `--enable-autodetect` is set.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `rca_autodetect_patterns_tracked` | Gauge | | Current patterns in the accumulator |
| `rca_autodetect_rules_active` | Gauge | | Current auto-generated rule count |
| `rca_autodetect_rules_created_total` | Counter | | Total auto rules created |
| `rca_autodetect_rules_expired_total` | Counter | | Total auto rules expired |
| `rca_autodetect_analysis_duration_seconds` | Histogram | | Time per analysis tick |

## Scraping

### In-cluster (Helm)

The Helm chart creates a metrics `Service` on port 8443 (HTTPS by default). Configure your Prometheus to scrape this endpoint, or use the ServiceMonitor if your cluster runs the Prometheus Operator.

### Local development

```bash
# Start with HTTP metrics on port 8080
make run ARGS="--metrics-bind-address=:8080 --metrics-secure=false"

# Scrape
curl http://localhost:8080/metrics
```

## Alerting Examples

```yaml
# Alert when incidents stay in Detecting for too long
- alert: RCAIncidentStuckDetecting
  expr: histogram_quantile(0.95, rate(rca_incident_transition_seconds_bucket{from_phase="detecting",to_phase="active"}[1h])) > 600
  for: 15m
  labels:
    severity: warning
  annotations:
    summary: "95th percentile detecting-to-active transition exceeds 10 minutes"

# Alert when active incidents are rising
- alert: RCAActiveIncidentsHigh
  expr: sum(rca_active_incidents) > 10
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "More than 10 active incidents in the cluster"
```
