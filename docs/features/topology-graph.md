# Incident Topology Graph

Every `IncidentReport` carries a topology graph describing the blast radius
of the incident: the affected Kubernetes resources, their owning workloads,
and — when trace data is available — the service-to-service call edges that
connect them.

---

## Where it lives

The graph is serialized into the `IncidentReport` CR at
`status.incidentGraph`:

```bash
kubectl get incidentreport <name> -n <ns> -o jsonpath='{.status.incidentGraph}' | jq
```

Schema (from [internal/correlator/graph/graph.go](../../internal/correlator/graph/graph.go)):

```jsonc
{
  "nodes": [
    { "id": "incident:default/foo", "kind": "Incident", "isRoot": true },
    { "id": "pod:default/foo-abc",  "kind": "Pod", "name": "foo-abc" },
    { "id": "svc:default/foo",      "kind": "Service", "name": "foo" },
    { "id": "svc:default/bar",      "kind": "Service", "name": "bar" }
  ],
  "edges": [
    { "from": "incident:default/foo", "to": "pod:default/foo-abc", "kind": "affects" },
    { "from": "svc:default/foo",      "to": "svc:default/bar",     "kind": "calls", "count": 1724 }
  ],
  "truncated": false
}
```

---

## How it's built

Three enrichment layers, applied in order. Each is best-effort — missing
inputs degrade gracefully without erroring the incident.

| Layer | Source | Produces |
|---|---|---|
| **Kubernetes** | `IncidentReport.status.affectedResources` + `spec.scope` | Incident → pod/service/workload edges (`affects`, `owns`) |
| **Trace buffer** | Correlator's in-memory span buffer, keyed by incident's trace-ids | Service → service edges derived from `parent_span → child_span` relationships |
| **Jaeger Query** | `http://<jaeger-service>:16686` via REST API | Service → service edges with call counts from the Jaeger dependencies API |

If Jaeger is disabled, the graph still renders from the first two layers. If
the operator has no correlated trace-ids either, you still get the K8s-only
subgraph — root incident node plus affected resources.

---

## Trace correlation

The operator annotates each `IncidentReport` with the OTel trace-ids that
contributed signals to it:

| Annotation | Meaning |
|---|---|
| `rca.rca-operator.tech/trace-id` | The most recent trace-id attached |
| `rca.rca-operator.tech/trace-ids` | Up to 20 unique trace-ids (comma-separated) |
| `rca.rca-operator.tech/fired-rule` | The correlation rule that last matched |

The graph builder reads these, pulls spans for each trace-id from the
correlator buffer and Jaeger, and merges service-call edges into the graph.

---

## Helm configuration

```yaml
# helm/values.yaml (defaults shown)
graphBuilder:
  # Override Jaeger Query URL. Empty = auto-compute from the in-chart Jaeger.
  jaegerQueryURL: "http://rca-jaeger:16686"
```

Point at an external Jaeger:

```bash
helm upgrade rca-operator rca-operator/rca-operator \
  --set jaeger.enabled=false \
  --set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686
```

Disable Jaeger enrichment entirely — the builder will fall back to the
K8s + trace-buffer graph:

```bash
--set jaeger.enabled=false --set graphBuilder.jaegerQueryURL=""
```

---

## Dashboard rendering

The built-in dashboard exposes three related views over this data:

1. **Per-incident graph** — when you open an incident, the dashboard renders
   `status.incidentGraph` as an interactive blast-radius view (colored nodes
   per kind, zoomable canvas, clickable edges that show span-id / trace-id
   chips).
2. **Cluster topology** — the Topology tab "Workload" mode shows the
   live K8s graph (Deployments / Pods / Nodes) via `GET /api/topology`,
   independent of any single incident.
3. **Service dependencies** — the Topology tab "Services" mode renders a
   Jaeger-style service graph with edge call counts via
   `GET /api/service-graph`. Click a service to see traffic stats
   (inbound / outbound / peers), trace IDs observed on incidents touching
   that service, open incidents, and recent K8s events.

Trace IDs surfaced in the incident pane and the service panel are
clickable — the dashboard opens an inline Jaeger trace detail modal
(`GET /api/traces/{id}`) without sending you to the Jaeger UI. See
[Dashboard](./DASHBOARD.md) for the full API surface and the trace payload
schema.

---

## Size bounds

The graph is truncated when the serialized payload would exceed the
`IncidentReport` annotation/size budget. When that happens:

- `incidentGraph.truncated` is set to `true`
- node dropping prefers leaves over nodes adjacent to the incident root
- the incident's root node and its direct affected resources are always
  preserved

This keeps `IncidentReport` CRs well under etcd's 1.5 MiB per-object limit
even on dense traces.
