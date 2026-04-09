# Dashboard

RCA Operator includes a built-in dashboard for incident visibility, service topology, and AI-powered root cause investigation.

## Data Contract

The dashboard reads only:

- `IncidentReport` — incident lifecycle data, RCA results, blast radius
- `RCAAgent` — agent configuration and health status
- `RCACorrelationRule` — correlation rule definitions

It does not query Pods, Nodes, Events, Deployments, or any external datastore directly. This keeps the UI consistent with the operator's durable incident model.

All telemetry data (traces, metrics, logs, topology) is fetched through the operator's configured telemetry backend via the `/api/services/*` and `/api/topology` endpoints.

The dashboard uses an icon-based sidebar with seven views. The active view is highlighted in the sidebar.

### Topology View (default)
- interactive SVG service dependency graph with draggable/zoomable canvas
- status-colored nodes (healthy/warning/critical/unknown) with service icons
- animated edges showing call relationships with error rate indicators; edge color reflects health (green/yellow/red)
- node status propagates from edge error rates: if service-to-service calls are failing, the target node turns critical even without a K8s-level incident
- click any node to open a side panel showing: metrics (request rate, error rate, P99 latency, CPU, memory, active connections), active incidents, blast radius analysis, and quick links to traces and logs
- legend showing health status color coding

### Incidents View
- current incident phase and severity
- summary, reason, and message
- first seen, active, last seen, and resolved timestamps
- affected resources and scope
- incident timeline
- monitored namespaces and configured agents
- loaded correlation rules
- notification status (sent/pending)

### Metrics View (Phase 2)
- per-service RED metrics: request rate, error rate, P99 latency
- infrastructure metrics: CPU usage, memory usage, active connections
- data sourced from Prometheus (requires `telemetry.prometheus.endpoint` in Helm values)
- shows `—` for metrics when the telemetry backend is unavailable

### Logs View (Phase 2)
- unified log stream for a selected service
- filters by severity (error/warn/info)
- sourced from SigNoz logs API when configured; falls back to Kubernetes pod logs (tail of the most recent pod matching the service name)
- each log entry shows timestamp, severity badge, and message body

### Traces View (Phase 2)
- recent distributed traces for a selected service
- table shows trace ID, root span, duration (in ms), span count, start time, and error badge
- sourced from Jaeger or SigNoz depending on configured backend
- click a trace ID to view in the configured Jaeger/SigNoz UI

### Rules View
- table of all `RCACorrelationRule` CRs with name, confidence, and auto-generated flag
- shows which rules are active and their priority

### Agents View
- table of `RCAAgent` CRs with status, monitored namespaces, and configuration summary
- aggregate stats: active/detecting/resolved incident counts

Interactive SVG service dependency graph built from OTel span relationships:

- Draggable nodes on a canvas grid, color-coded by health status (green=healthy, amber=warning, red=critical, gray=unknown)
- Animated directional edges showing call relationships
- Click any node to open a side panel showing:
  - RED metrics (request rate, error rate, P99 latency)
  - Resource metrics (CPU, Memory)
  - Active incidents for that service
  - Blast radius (upstream/downstream impact)

Requires a telemetry backend to be configured. Shows an empty canvas when no backend is configured.

### Incidents

Full incident management view:

- Search box and phase filters (All / Active / Detecting / Resolved)
- Per-incident detail: severity, phase, type, summary, affected resources
- Timeline of phase transitions
- Correlated signals list
- Related trace IDs (Phase 2, populated by cross-signal enricher)
- Blast radius services (Phase 2)
- AI RCA block: root cause, confidence score, evidence, remediation playbook with copy buttons
- Trigger AI investigation via the **Investigate** button

### Metrics

Per-service metric cards using data from the configured Prometheus backend:

- CPU Usage
- Memory Usage
- Request Rate
- Error Rate
- P99 Latency
- Active Connections

Select a service from the dropdown and optionally set the time range.

### Logs

Terminal-style log viewer with color-coded severity levels:

- `INFO` — emerald
- `WARN` — amber
- `ERROR` / `FATAL` — red
- `K8S` events — indigo

Filter by service and minimum severity. Supports up to 200 log entries.

### Traces

Recent distributed traces for a selected service:

| Column | Description |
|---|---|
| Trace ID | W3C trace ID (truncated) |
| Root Operation | Top-level span operation name |
| Duration | End-to-end trace duration |
| Spans | Total span count |
| Status | OK / ERROR |
| Time | When the trace was recorded |

Click a trace row to copy the full trace ID.

### RCA Rules

Full table of all active `RCACorrelationRule` CRs:

| Column | Description |
|---|---|
| Priority | Rule evaluation order (lower = higher priority) |
| Name | Rule name |
| Trigger | Event type that activates this rule |
| Fires As | Incident type created when the rule fires |
| Severity | Incident severity |
| Conditions | Required signal conditions |
| Agent | Associated RCAAgent selector |
| Auto | Whether the rule was auto-generated |
| Confidence | Auto-detection confidence score |
| Age | Time since creation |

### RCA Agents

Card grid showing all `RCAAgent` resources:

- Health dot (green = Available, amber = Degraded)
- Watched namespace pills
- Slack / PagerDuty integration badges
- Incident retention period
- Signal mapping count
- Status conditions

## Live Correlation Stream

A persistent stream bar at the bottom of every view shows live signals as they arrive, fed via Server-Sent Events from `/api/stream/correlation`.

Events are color-coded by source:

| Source | Color |
|---|---|
| `prometheus` | Blue |
| `jaeger` | Violet |
| `signoz` | Slate |
| `rca-operator` | Red |
| `ai` | Purple |

Event format:

```
[HH:MM:SS] [SOURCE] signal description
```

The stream reconnects automatically on disconnect. When no telemetry backend is configured, the stream shows incident lifecycle events (created, resolved) from the operator itself.

## Access

The dashboard is enabled by default in the Helm chart on port 9090.

### Port-forward

```bash
kubectl port-forward -n rca-system service/rca-operator-dashboard 9090:9090
```

Open `http://localhost:9090`.

### Ingress

```yaml
dashboard:
  enabled: true
  port: 9090
  ingress:
    enabled: true
    className: nginx
    hosts:
      - host: rca.example.com
        paths:
          - path: /
            pathType: Prefix
```

## API Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Dashboard UI (static HTML/CSS/JS) |
| `GET /api/incidents` | All IncidentReport CRs. Query params: `namespace`, `phase`, `severity`, `type`, `query`, `limit`, `offset`, `sort` |
| `GET /api/incidents/{namespace}/{name}` | Single IncidentReport detail |
| `GET /api/stats` | Aggregate statistics: active/detecting/resolved counts, namespace breakdown |
| `GET /api/rules` | All RCACorrelationRule CRs (includes `autoGenerated` and `confidence` fields) |
| `GET /api/agents` | All RCAAgent resources with health, conditions, and configuration summary |
| `GET /api/timeline?fingerprint=...` | Unified chronological timeline for a given incident fingerprint |
| `GET /api/topology` | ServiceGraph JSON (nodes + edges + metrics) |
| `GET /api/topology/blast?service=X` | Blast radius for service X (upstream + downstream) |
| `GET /api/services` | Discovered services with health status and icons |
| `GET /api/services/{name}` | Single service node detail |
| `GET /api/services/{name}/metrics` | RED + resource metrics for a service |
| `GET /api/services/{name}/traces` | Recent traces. Query param: `limit` |
| `GET /api/services/{name}/logs` | Recent logs. Query params: `limit`, `severity` |
| `GET /api/investigate/{ns}/{name}` | Get existing AI RCA result for an incident |
| `POST /api/investigate/{ns}/{name}` | Trigger AI investigation for an incident |
| `SSE /api/stream/topology` | Live topology graph updates |
| `SSE /api/stream/correlation` | Live correlation signal stream |

See [Dashboard API Reference](../reference/dashboard-api.md) for full request/response schemas.

## SSE Event Reference

Both SSE streams use the same wire format:

```
event: <event-type>
data: <json>

: keepalive
```

Keepalive comments are sent every 30 seconds to prevent proxy timeouts.

### /api/stream/correlation events

| Event Type | Payload |
|---|---|
| `connected` | `{"channel": "correlation", "time": "2026-04-01T10:00:00Z"}` |
| `signal.new` | `{"type": "...", "service": "...", "message": "...", "source": "prometheus\|jaeger\|signoz\|rca-operator\|ai", "time": "..."}` |
| `incident.created` | `{"name": "...", "namespace": "...", "type": "...", "severity": "...", "time": "..."}` |

### /api/stream/topology events

| Event Type | Payload |
|---|---|
| `connected` | `{"channel": "topology", "time": "..."}` |
| `topology.update` | Full ServiceGraph JSON |

## Operational Notes

- The dashboard is best treated as an operator-facing UI, not a multi-user portal.
- Authentication should be handled at the ingress or network boundary.
- If the dashboard looks wrong, check the underlying `IncidentReport` objects first since they are the source of truth.
- Topology, metrics, logs, and traces tabs require a telemetry backend to be configured. They return empty data gracefully when no backend is set.
