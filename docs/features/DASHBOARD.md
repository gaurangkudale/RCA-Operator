# Dashboard

RCA Operator includes a built-in dashboard for incident visibility.

## Data Contract

The dashboard's primary data source is the operator's CRDs:

- `IncidentReport` — durable incident state
- `RCAAgent` — agent configuration and health
- `RCACorrelationRule` — correlation rules

For the topology and trace views the dashboard additionally reads:

- Pod / Deployment / Node / Service objects (read-only) for the cluster topology graph
- Pod logs (streamed) for the Logs tab
- Jaeger Query API for service-dependency edges and trace detail (when `WithJaegerClient` is wired)

The CRDs remain the source of truth for incident lifecycle; everything else is enrichment.

## What It Shows

**Incidents tab**
- Current incident phase and severity
- Summary, reason, message, fired rule, trace IDs
- First seen, active, last seen, resolved timestamps
- Affected resources and scope
- Incident timeline (timeline + lifecycle transition events, deduplicated)
- Notification status (sent/pending)

**Topology tab** — two modes:
- **Workload** — Deployments, Pods, Nodes laid out as a hierarchical DAG; click a node for resource detail
- **Services** — Jaeger-style service dependency graph with edge call counts; click a service for traffic stats (inbound / outbound / peers), trace IDs observed on incidents touching that service, open incidents, and recent events
- Auto fits to viewport on first load when the laid-out graph exceeds the visible area

**Trace detail modal** — click any trace ID anywhere in the UI to open a Jaeger trace inline:
- Header tiles: status (OK/ERROR), total duration, span count, service count, start time, root operation, critical path
- Pinned error spans with status code + exception type/message
- Waterfall: time-aligned bars indented by parent-child depth, red for errors, amber for slow outliers
- Per-span detail panel (click a row): HTTP / DB / RPC tags, k8s pod context, freeform tags
- **Pod deep-link** — clicking a pod chip on a span closes the modal, switches to the Logs tab, and prefills namespace + pod selectors
- Bounded at 500 spans per response (errors + longest non-error spans kept; `truncated: true` flagged in payload)

**Logs tab** — streams pod logs via `/api/logs` with namespace/pod/deployment selectors.

**Rules tab** — all `RCACorrelationRule` CRDs, including auto-detected rules (default priority 30, labeled `rca.rca-operator.tech/auto-generated: "true"`).

**Agents tab** — `RCAAgent` CRDs with status and last-sync time.

## Reports

The dashboard can generate shareable, archivable incident reports for postmortems, tickets, or email — no screenshots required.

- **Per-incident report** — a **Report** button in the incident detail pane opens `GET /api/incidents/{namespace}/{name}/report`: a printable page with the incident summary, lifecycle/metadata, affected resources, grouped correlated signals, trace IDs, and full timeline.
- **Cluster summary report** — an **Export Report** button on the Overview tab opens `GET /api/report`: incident counts, open-severity mix, agent health, and an incidents table.

Each report is a **self-contained HTML page** (inline CSS, no external assets), so the downloaded file is fully portable. A toolbar (hidden when printing) offers:

- **Save as PDF** — triggers the browser's native print dialog; choose "Save as PDF" as the destination. The print stylesheet hides the toolbar and lays the report out for paper.
- **Download HTML** — saves the page as a standalone `.html` file. (Equivalent to requesting the endpoint with `?download=1`, which sets a `Content-Disposition: attachment` header.)

Reports are rendered server-side with Go's `html/template`, so all incident-supplied text is HTML-escaped. No extra dependencies or external services are involved.

## Theme

The dashboard supports light and dark themes with a toggle button in the top navigation. The selected theme is persisted to `localStorage`.

## Access

The dashboard is enabled by default in the Helm chart (port 9090).

### Port-forward

```bash
kubectl port-forward -n rca-system service/rca-operator-dashboard 9090:9090
```

Open `http://localhost:9090`.

### Ingress

Use the Helm values to expose the dashboard through an ingress:

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

See [examples/dashboard](../../examples/dashboard) for more example configurations.

## API Endpoints

All endpoints return JSON unless noted otherwise. Most responses are served through a short-TTL in-process cache with `ETag` / `If-None-Match` support, so repeat polls coalesce to `304 Not Modified`.

| Endpoint | Description |
|---|---|
| `GET /` | Dashboard UI (static HTML/CSS/JS, served from embedded FS) |
| `GET /api/incidents` | All `IncidentReport` CRs. Query params: `namespace`, `phase`, `severity`, `type`, `query`, `limit`, `offset`, `sort` |
| `GET /api/incidents/{namespace}/{name}` | Single `IncidentReport` detail with `traceId` / `traceIds` and `firedRule` |
| `GET /api/incidents/{namespace}/{name}/graph` | Per-incident topology subgraph (K8s + trace + Jaeger enrichment, see [Topology Graph](./topology-graph.md)) |
| `GET /api/incidents/{namespace}/{name}/report` | Self-contained, print-optimized HTML report for one incident (**not** JSON). `?download=1` serves it as a file attachment. See [Reports](#reports) |
| `GET /api/stats` | Aggregate statistics: active/detecting/resolved counts, namespace breakdown, agent info |
| `GET /api/report` | Self-contained, print-optimized cluster summary HTML report (**not** JSON): all incidents, stats, severity mix, agent health. `?download=1` serves it as a file attachment. See [Reports](#reports) |
| `GET /api/rules` | All `RCACorrelationRule` CRs (includes `autoGenerated` and `confidence` fields for auto-detected rules) |
| `GET /api/agents` | All `RCAAgent` CRs with last-sync status |
| `GET /api/timeline?fingerprint=...` | Unified chronological timeline across all lifecycle phases for a given incident fingerprint |
| `GET /api/topology` | Cluster topology graph (Deployments / Pods / Nodes / owns / scheduled_on edges). Query: `view=summary` (default) or `view=detail` |
| `GET /api/service-graph?lookback=1h` | Jaeger-style service dependency graph with call counts per edge. Returns an empty graph with a fallback marker when Jaeger is unreachable |
| `GET /api/resources/{namespace}/{kind}/{name}` | Read-only detail for a single resource (Pod, Deployment, Node, Service) — used by the topology side panel |
| `GET /api/traces/{id}` | Trace detail (summary + ordered spans + service breakdown + per-span k8s/tag context). Cached server-side for 5 minutes — trace data is immutable. `503` when Jaeger client isn't configured. `400` on malformed IDs. Bounded at 500 spans per response |
| `GET /api/logs?ns=...&pod=...` | Streams pod logs (also accepts `deployment`, `container`, `since`, `tail`) |
| `GET /api/pods?ns=...` | Pod list for the Logs tab pod selector |
| `GET /api/stream` | Server-Sent Events of correlation buffer activity. Currently unused by the bundled UI; kept for external consumers |

### Timeline API

The `/api/timeline` endpoint accepts a `fingerprint` query parameter and returns a JSON array of timeline entries collected from all `IncidentReport` CRs matching that fingerprint. Each entry contains:

```json
{
  "time": "2026-04-02T10:05:00Z",
  "event": "Incident confirmed active after stabilisation period",
  "phase": "Active",
  "incidentName": "incident-abc123",
  "namespace": "production"
}
```

The response includes both timeline entries from the incident and lifecycle transition events (detected, activated, resolved), sorted chronologically and deduplicated.

### Trace API

The `/api/traces/{id}` endpoint wraps the Jaeger Query API and reshapes the response into a UI-friendly payload:

```jsonc
{
  "traceId": "7b4b790140a40d99dc7ccb306a7d5036",
  "startTime": "2026-04-28T03:14:07.512Z",
  "durationMicros": 487213,
  "spanCount": 42,
  "serviceCount": 5,
  "status": "error",                 // "ok" | "error"
  "rootService": "frontend",
  "rootOperation": "POST /checkout",
  "criticalPathMs": 412,
  "errorSpans": ["a1b2c3d4e5f60718"],
  "spans": [
    {
      "spanId": "a1b2c3d4e5f60718",
      "parentSpanId": "0011223344556677",
      "service": "checkout",
      "operation": "GET /quote",
      "startOffsetUs": 12450,
      "durationUs": 98321,
      "depth": 2,
      "kind": "CLIENT",
      "isError": true,
      "statusCode": "500",
      "podName": "checkout-7b9d-xy",
      "podNamespace": "rca-demo",
      "containerName": "checkout",
      "exceptionType": "RuntimeError",
      "exceptionMessage": "quote service timeout",
      "tags": { "http.target": "/quote" }
    }
  ],
  "serviceBreakdown": [
    { "service": "checkout", "spanCount": 14, "totalDurationUs": 312044, "percentOfTrace": 64.0 }
  ],
  "truncated": false
}
```

Notes:
- `durationMicros` is wall-clock end minus start (concurrent spans don't double-count).
- `serviceBreakdown[].totalDurationUs` is overlap-merged per service so concurrent spans within one service report a real share of trace time.
- When `spanCount > 500`, the response keeps every error span plus the longest non-error spans up to the cap, sets `truncated: true`, and the UI annotates the waterfall with "Trace has N spans; showing the M most relevant."
- Requires `WithJaegerClient` to be wired on the dashboard server (set automatically in `cmd/main.go` when `graphBuilder.jaegerQueryURL` resolves to a reachable endpoint).

## Operational Notes

- The dashboard is best treated as an operator-facing UI, not a multi-user portal.
- Authentication should be handled at the ingress or network boundary.
- If the dashboard looks wrong, check the underlying `IncidentReport` objects first since they are the source of truth.
- The Jaeger-backed views (service graph, trace detail) degrade gracefully — if Jaeger is unreachable the rest of the UI keeps working; the affected sections show empty states rather than erroring.
