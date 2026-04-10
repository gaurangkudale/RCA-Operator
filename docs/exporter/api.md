# RCA Exporter API Reference

The RCA Exporter exposes one ingestion contract — **OpenTelemetry Protocol (OTLP) for logs** — over two transports, plus a small operational surface for health probes. This document is the canonical reference for both. For deployment instructions read [`usage.md`](usage.md); for the protocol itself the upstream spec lives at <https://opentelemetry.io/docs/specs/otlp/>.

---

## Transport summary

| Transport | Default port | Path | Wire format(s) | Used by |
|---|---|---|---|---|
| OTLP / gRPC | `4317` | `opentelemetry.proto.collector.logs.v1.LogsService/Export` | protobuf | OTel Collector `otlp` exporter, language SDKs |
| OTLP / HTTP | `4318` | `POST /v1/logs` | `application/x-protobuf`, `application/json` (optionally `Content-Encoding: gzip`) | OTel Collector `otlphttp` exporter, browser/JS SDKs, lightweight forwarders |
| Health | `8081` | `GET /healthz`, `GET /readyz` | plain text | Kubelet probes, smoke tests |

Both ingestion transports converge on the **same `LogsReceiver.Export` method** internally, so a spike detected from one transport will dedup against records arriving on the other. You can run either transport alone (set the other's flag to the empty string) or both side-by-side (the default).

---

## OTLP/gRPC

### Service definition

```
service LogsService {
  rpc Export(ExportLogsServiceRequest) returns (ExportLogsServiceResponse) {}
}
```

This is the standard `opentelemetry.proto.collector.logs.v1.LogsService` defined in the OTLP spec — no extensions, no custom methods. Any compliant client works.

### Request message

`ExportLogsServiceRequest` carries a `repeated ResourceLogs resource_logs = 1`. Each `ResourceLogs` bundles:

- A `Resource` (set of `KeyValue` attributes — see [Required attributes](#required-resource-attributes) below)
- One or more `ScopeLogs`, each containing one or more `LogRecord`s

The exporter walks this tree and processes every `LogRecord` whose `severity_number >= SEVERITY_NUMBER_ERROR (17)`. Records below ERROR are silently ignored — they never count toward the spike threshold, never appear in samples, and consume no memory beyond the cost of decoding.

### Response message

`ExportLogsServiceResponse` is always returned **empty** on success. The exporter never rejects records: a misclassified log is preferable to dropping client data and confusing on-call engineers chasing missing logs. Partial-success rejection (which the OTLP spec allows for unrecoverable errors) is reserved for a future iteration when we add rate-limiting.

### Example with `grpcurl`

```bash
# Port-forward the exporter (production: hit the Service directly)
kubectl -n rca-operator-system port-forward svc/rca-exporter 4317:4317 &

grpcurl -plaintext -d @ localhost:4317 \
  opentelemetry.proto.collector.logs.v1.LogsService/Export <<'EOF'
{
  "resourceLogs": [{
    "resource": {
      "attributes": [
        {"key": "service.name",        "value": {"stringValue": "payment-service"}},
        {"key": "k8s.namespace.name",  "value": {"stringValue": "dev"}},
        {"key": "k8s.pod.name",        "value": {"stringValue": "payment-service-0"}},
        {"key": "k8s.container.name",  "value": {"stringValue": "payment"}}
      ]
    },
    "scopeLogs": [{
      "logRecords": [
        {"severityNumber": 17, "body": {"stringValue": "payment failed for user 1"}},
        {"severityNumber": 17, "body": {"stringValue": "payment failed for user 2"}},
        {"severityNumber": 17, "body": {"stringValue": "payment failed for user 3"}}
      ]
    }]
  }]
}
EOF
# {} on success
```

### Connection notes

- **No TLS in the exporter binary.** TLS termination is the responsibility of the upstream collector or a service mesh sidecar (Istio, Linkerd) — the exporter is designed to run as an in-cluster Service reachable only from trusted pipelines, not as an Internet-facing endpoint.
- **No authentication.** Same rationale: trust comes from network policy, not a per-request token. If you expose the exporter outside the cluster, run an authenticating proxy in front of it.
- **Maximum message size** is the gRPC default (4 MiB). Upstream collectors should batch under this; the OTel Collector default of 8192 records or 5 MiB will need to be lowered for ultra-high-cardinality services.

---

## OTLP/HTTP

### Endpoint

```
POST /v1/logs
Host: <exporter-host>:4318
```

The path is fixed by the OTLP spec — clients always send to `/v1/logs` regardless of how the receiver is mounted. The exporter rejects any other path with `404 Not Found` and any other method with `405 Method Not Allowed`.

### Request

| Header | Required | Notes |
|---|---|---|
| `Content-Type` | yes | One of: `application/x-protobuf`, `application/protobuf`, `application/json`. Empty defaults to protobuf. A `; charset=...` suffix is tolerated and stripped. |
| `Content-Encoding: gzip` | optional | When set, the body is transparently inflated. Required by the OTel Collector's `otlphttp` exporter for batches ≥ 1 KiB. |

The body is an `ExportLogsServiceRequest` — the same proto message as gRPC — serialized in the chosen encoding. Maximum request size is **16 MiB** (enforced via `http.MaxBytesReader`); larger requests are rejected with `400 Bad Request`. This cap is well above any reasonable single OTLP batch and well below the exporter's container memory limits.

### Response

| Status | Body | When |
|---|---|---|
| `200 OK` | empty `ExportLogsServiceResponse` in the same encoding as the request | success — every record was accepted |
| `400 Bad Request` | plain-text error | malformed protobuf, invalid JSON, gzip decode failure, body exceeds 16 MiB |
| `405 Method Not Allowed` | plain-text error | request method is not `POST` |
| `415 Unsupported Media Type` | plain-text error | `Content-Type` is not one of the accepted media types |
| `500 Internal Server Error` | plain-text error | server-side marshalling failure (should never happen in practice) |

The success response body **mirrors the request encoding**: a JSON request gets a JSON response, a protobuf request gets a protobuf response. Both response bodies are valid `ExportLogsServiceResponse` messages so strict clients that parse them will not fail.

### Example with `curl` (protojson)

```bash
kubectl -n rca-operator-system port-forward svc/rca-exporter 4318:4318 &

curl -i -X POST http://localhost:4318/v1/logs \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'EOF'
{
  "resourceLogs": [{
    "resource": {
      "attributes": [
        {"key": "service.name",       "value": {"stringValue": "payment-service"}},
        {"key": "k8s.namespace.name", "value": {"stringValue": "dev"}}
      ]
    },
    "scopeLogs": [{
      "logRecords": [
        {"severityNumber": 17, "body": {"stringValue": "db connection refused"}},
        {"severityNumber": 17, "body": {"stringValue": "db connection refused"}},
        {"severityNumber": 17, "body": {"stringValue": "db connection refused"}}
      ]
    }]
  }]
}
EOF
# HTTP/1.1 200 OK
# Content-Type: application/json
# {}
```

### Example with `curl` (gzip + protobuf)

The OTel Collector sends this combination by default for batches ≥ 1 KiB:

```bash
# Encode an ExportLogsServiceRequest as protobuf, gzip it, POST it.
# (Use a small Go program or otel-cli to produce request.bin first.)
gzip -c request.bin | curl -X POST http://localhost:4318/v1/logs \
  -H 'Content-Type: application/x-protobuf' \
  -H 'Content-Encoding: gzip' \
  --data-binary @-
```

---

## Required resource attributes

The exporter performs **per-service** spike detection, so it must be able to attribute every log record to a `(namespace, service)` pair. Resource attributes are read once per `ResourceLogs` bundle (the OTLP spec guarantees they are shared by every record under it).

| Attribute | Required | Source | What happens if missing |
|---|---|---|---|
| `service.name` | yes (with fallback) | OTel SDK or collector `resource` processor | Falls back to `k8s.deployment.name`, then to a heuristic strip of `k8s.pod.name` |
| `k8s.namespace.name` | yes | `k8sattributes` processor | Empty namespace → IncidentReport will be created in the empty namespace and probably rejected by RBAC. Always set this. |
| `k8s.pod.name` | recommended | `k8sattributes` processor | Used for the IncidentReport's pod label and as a service-name fallback |
| `k8s.container.name` | optional | `k8sattributes` processor | Captured in the IncidentReport metadata for triage |
| `k8s.deployment.name` | optional | `k8sattributes` processor | Used as a `service.name` fallback for non-instrumented apps |

The standard production wiring is the OTel Collector's `k8sattributes` processor with `extract.metadata` set to all five fields. See [`config/rca-exporter/otel-collector-example.yaml`](../../config/rca-exporter/otel-collector-example.yaml) for the reference config.

### Severity classification

The exporter uses the OTLP-defined `SeverityNumber` enum and counts every record at or above `SEVERITY_NUMBER_ERROR (17)`. This includes:

| SeverityNumber | Name | Counted? |
|---|---|---|
| 1–4 | TRACE | no |
| 5–8 | DEBUG | no |
| 9–12 | INFO | no |
| 13–16 | WARN | no |
| 17–20 | **ERROR** | **yes** |
| 21–24 | **FATAL** | **yes** |

Records without an explicit `SeverityNumber` (i.e. `severity_number == 0`) are **not** counted, even if their text body contains the word "error". Severity must come from the producer or be inferred upstream by a collector operator like `severity_parser`.

---

## Health endpoints

The exporter hosts two HTTP probes on a separate listener (`--health-bind-address`, default `:8081`) so kubelet probes do not contend with OTLP traffic.

| Endpoint | Returns | Meaning |
|---|---|---|
| `GET /healthz` | `200 ok` | Process is alive — fail this and kubelet restarts the pod |
| `GET /readyz` | `200 ready` | Process can accept OTLP traffic — fail this and kubelet removes the pod from the Service endpoints |

Both probes are trivial 200 responses in the MVP. Future iterations will gate readiness on aggregator depth and last-OTLP timestamp once self-metrics land — see [`todos.md`](todos.md).

---

## What the exporter does NOT expose

By design, the exporter has no:

- **Prometheus metrics endpoint.** Self-observability flows through OTel via `OTEL_EXPORTER_OTLP_ENDPOINT`. There is no `/metrics` URL and no scrape contract. This is the deliberate Phase-2 anti-lock-in stance described in [`README.md`](README.md#vendor-lock-in-posture).
- **Admin / debug API.** No reset, no flush, no introspection of the in-memory aggregator state. Restart the pod if you need to clear state.
- **Trace ingestion.** Phase 2 will add OTLP/Traces on the same two ports, but it is not yet wired in. See [`todos.md`](todos.md).
- **Authenticated API.** As above, trust is established at the network layer.

---

## Production sizing notes

A single exporter Deployment can comfortably handle **10–50k records/sec per replica** on 1 vCPU / 512 MiB RAM, dominated by protobuf decoding cost. The aggregator's per-service window is a slice of timestamps + last-N message strings, so memory is `O(services × threshold)` — even 10 000 services at threshold 100 fits in well under 100 MiB.

Bottlenecks to watch in upstream sizing:

- **Fluent Bit's tail plugin** has finite buffers; under sustained burst the recommended fix is `Mem_Buf_Limit 10MB` plus `storage.type filesystem` so backpressure spills to disk instead of dropping records.
- **OTel Collector's `batch` processor** with `send_batch_size: 8192` and `timeout: 5s` is a sane default for moderate volumes; raise `send_batch_size` only if the exporter's gRPC listener shows latency.
- **Single-replica MVP.** The current exporter holds all per-service windows in memory and is not safe to scale horizontally (two replicas would each see half the records and miss spikes that should fire). Multi-replica support via consistent hashing is on the roadmap.

---

## Next steps

- Read [`usage.md`](usage.md) for a complete kind walkthrough and common-issues runbook
- Read [`development.md`](development.md) to extend the receiver or add new detectors
- Read [`todos.md`](todos.md) for upcoming protocol additions (traces, change events)
