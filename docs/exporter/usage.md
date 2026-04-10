# Using the RCA Exporter

This document covers everything an operator needs to **deploy, send logs to, and observe** the RCA Exporter. For background on what the exporter is and why it exists, read [`README.md`](README.md) first.

---

## Prerequisites

- A Kubernetes cluster (kind, k3d, EKS, GKE, AKS, on-prem — all work)
- `kubectl` configured to point at it
- The RCA-Operator CRDs already installed (see [`docs/getting-started/installation.md`](../getting-started/installation.md))
- The RCA-Operator manager already running (the exporter creates `IncidentReport` CRs that the manager reconciles)
- Optional but recommended: an OpenTelemetry Collector deployed in the cluster as a forwarder for application logs

You do **not** need:

- Prometheus
- A managed observability vendor account
- Application code changes (apps just write to stdout/stderr)
- A trace backend (traces are a Phase-2 follow-up, not yet wired in)

---

## Local kind walkthrough

The fastest way to see the exporter end-to-end is on a local [kind](https://kind.sigs.k8s.io) cluster. Total time: ~5 minutes.

### 1. Create the cluster and deploy the operator + exporter

```bash
# Build images, create the kind cluster, install CRDs, deploy operator + exporter
make kind-deploy-all
```

This single target performs:

1. `kind create cluster --name rca-dev` (skipped if already present)
2. `make install` — installs the RCA CRDs (`incidentreports`, `rcaagents`, `rcacorrelationrules`)
3. `make docker-build` + `kind load docker-image controller:latest`
4. `make docker-build-exporter` + `kind load docker-image rca-exporter:latest`
5. `make deploy` — applies `config/default` (operator manager + RBAC)
6. `make deploy-exporter` — applies `config/rca-exporter` (RBAC + Service + Deployment)

Verify both pods are running:

```bash
kubectl -n rca-operator-system get pods
# NAME                              READY   STATUS    RESTARTS   AGE
# controller-manager-xxxxxxxxx-yy   1/1     Running   0          1m
# rca-exporter-yyyyyyyyy-zz         1/1     Running   0          1m
```

### 2. Wire up Fluent Bit + OpenTelemetry Collector to forward pod logs

The exporter needs OTLP log records on `:4317` (gRPC) or `:4318` (HTTP). The standard production wiring is **Fluent Bit (DaemonSet) → OpenTelemetry Collector → rca-exporter**, but for local testing the simplest path is a single OTel Collector with the `filelog` receiver tailing `/var/log/containers/*.log` directly on each kind node.

The exporter ships a reference collector config at [`config/rca-exporter/otel-collector-example.yaml`](../../config/rca-exporter/otel-collector-example.yaml). For kind, the easiest path is:

```bash
# Install the OTel Collector via Helm with a minimal values file
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update

helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
  --namespace rca-operator-system \
  --set mode=daemonset \
  --set image.repository=otel/opentelemetry-collector-contrib \
  --values - <<EOF
config:
  receivers:
    filelog:
      include: [ /var/log/pods/*/*/*.log ]
      start_at: end
      include_file_path: true
      operators:
        - type: container
          id: container-parser
  processors:
    k8sattributes:
      auth_type: serviceAccount
      passthrough: false
      extract:
        metadata:
          - k8s.namespace.name
          - k8s.pod.name
          - k8s.container.name
          - k8s.deployment.name
    batch:
      timeout: 5s
  exporters:
    otlp/rca-exporter:
      endpoint: rca-exporter.rca-operator-system.svc.cluster.local:4317
      tls:
        insecure: true
  service:
    pipelines:
      logs:
        receivers: [filelog]
        processors: [k8sattributes, batch]
        exporters: [otlp/rca-exporter]
EOF
```

> **Why `filelog` instead of Fluent Bit on kind?**
> Both work. `filelog` is built into the OTel Collector contrib distribution so it's one fewer DaemonSet. In production with high log volumes, Fluent Bit's tail plugin has better backpressure handling — see [`api.md → Production sizing`](api.md#production-sizing-notes).

### 3. Deploy two synthetic apps and trigger an incident

The exporter fires when a service produces ≥ `--error-threshold` ERROR-or-higher log records inside `--error-window` (defaults: 10 errors / 1 minute). To exercise this, deploy two pods that emit error logs and one that emits info logs:

```bash
kubectl create namespace dev

# A pod that emits an error every second
kubectl -n dev run payment-service --image=busybox --restart=Never -- \
  sh -c 'i=0; while true; do
    echo "{\"severity\":\"ERROR\",\"msg\":\"payment failed for user $i\"}";
    i=$((i+1)); sleep 1;
  done'

# A pod that emits info logs only — should NOT cause an incident
kubectl -n dev run api-gateway --image=busybox --restart=Never -- \
  sh -c 'while true; do echo "{\"severity\":\"INFO\",\"msg\":\"healthy\"}"; sleep 1; done'
```

> **Important:** the OTel Collector's `filelog` operator parses container log lines but does **not** automatically map JSON `severity` fields to OTLP `SeverityNumber`. For a real production setup you would add a `severity_parser` operator. For this walkthrough — to keep the example focused — replace the busybox apps with one that emits OTLP directly via the [OTel Demo](https://github.com/open-telemetry/opentelemetry-demo) or any SDK-instrumented app. The exporter only counts records whose `SeverityNumber >= 17 (ERROR)`, regardless of how they were tagged.

### 4. Watch the IncidentReport appear

After ~10 errors arrive within 1 minute, the exporter creates an IncidentReport:

```bash
kubectl get incidentreports -A -w
# NAMESPACE   NAME                              SEVERITY   PHASE        TYPE             AGE
# dev         logerrorspike-payment-service-x   P3         Detecting    LogErrorSpike    5s
# dev         logerrorspike-payment-service-x   P3         Active       LogErrorSpike    5m5s
```

`Detecting → Active` is driven by the **Phase-1 `IncidentReportReconciler`**'s 5-minute stabilization window — proof that the exporter and the manager are sharing the same incident lifecycle.

```bash
kubectl -n dev describe incidentreport logerrorspike-payment-service-x
```

You should see:

- `spec.agentRef: rca-exporter` (Phase-2 attribution)
- `spec.incidentType: LogErrorSpike`
- `status.severity: P3`
- `status.summary: 10 errors in 60s for dev/payment-service (threshold 10): payment failed for user 9 | payment failed for user 8 | payment failed for user 7`
- `status.timeline` showing the lifecycle phases

### 5. Tear down

```bash
make kind-delete
```

---

## Production deployment

For real clusters, the same pattern applies:

```bash
# 1. Build and push the exporter image to a registry your cluster can reach
make docker-build-exporter EXPORTER_IMG=ghcr.io/your-org/rca-exporter:v0.1.0
make docker-push-exporter   EXPORTER_IMG=ghcr.io/your-org/rca-exporter:v0.1.0

# 2. Deploy
make deploy-exporter EXPORTER_IMG=ghcr.io/your-org/rca-exporter:v0.1.0
```

Or via Helm: the chart in [`helm/`](../../helm) will receive an exporter sub-chart in a follow-up; see [`todos.md`](todos.md).

### Recommended upstream pipeline

For a production setup we recommend running **Fluent Bit as a DaemonSet** (one pod per node) feeding into a dedicated **OpenTelemetry Collector Deployment** that centralises k8sattributes enrichment, batching, and routing. The collector then forwards to the exporter Service.

```
Fluent Bit DaemonSet (tail /var/log/containers/*.log)
        │  fluentforward
        ▼
OpenTelemetry Collector Deployment
        │  k8sattributes → batch → memory_limiter
        ▼
otlp exporter → rca-exporter.rca-operator-system.svc.cluster.local:4317
```

The reference config in [`config/rca-exporter/otel-collector-example.yaml`](../../config/rca-exporter/otel-collector-example.yaml) shows this exact wiring.

---

## Configuration reference

All flags can be passed to `cmd/rca-exporter` directly or set in the container `args` section of [`config/rca-exporter/deployment.yaml`](../../config/rca-exporter/deployment.yaml).

| Flag | Default | Purpose |
|---|---|---|
| `--otlp-grpc-addr` | `:4317` | Bind address for the OTLP/gRPC receiver. Set to `""` to disable gRPC. |
| `--otlp-http-addr` | `:4318` | Bind address for the OTLP/HTTP receiver (POST `/v1/logs`). Set to `""` to disable HTTP. |
| `--health-bind-address` | `:8081` | HTTP listener for `/healthz` and `/readyz`. |
| `--error-window` | `1m` | Rolling detection window per service. |
| `--error-threshold` | `10` | Minimum ERROR records inside the window required to fire a `LogErrorSpike` incident. |
| `--spike-cooldown` | `5m` | Minimum interval between two consecutive spikes for the same service. Should be ≥ `reporter.SignalCooldown`. |
| `--sample-size` | `3` | Maximum number of error message samples retained per service for the IncidentReport summary. |
| `--agent-ref` | `rca-exporter` | Value written to `spec.agentRef` and the `rca/agent` label on every IncidentReport. Useful when running multiple exporters. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` (env) | _(unset)_ | Optional: forward the exporter's *own* self-traces through the same OTel pipeline. |

### Picking thresholds

The defaults (`10 errors / 1m`) are deliberately conservative — they avoid noise but may miss low-volume services. Tuning guidelines:

- **High-traffic services** (>1000 req/s): start with `--error-threshold=50 --error-window=1m`
- **Low-traffic services** (<10 req/s): start with `--error-threshold=3 --error-window=5m`
- **Batch jobs / cronjobs**: not yet a good fit — `JobFailed` from Phase 1 is better suited

You can run multiple exporter Deployments with different thresholds against different namespaces by using `--agent-ref` to disambiguate them in dashboards. A single exporter with namespace-specific config is on the roadmap (see [`todos.md`](todos.md)).

---

## Operations runbook

### Check the exporter is healthy

```bash
kubectl -n rca-operator-system port-forward svc/rca-exporter 8081:8081 &
curl -s http://localhost:8081/healthz   # → ok
curl -s http://localhost:8081/readyz    # → ready
```

### Confirm OTLP records are arriving

```bash
kubectl -n rca-operator-system logs -l app.kubernetes.io/name=rca-exporter -f
# Look for: "LogErrorSpike incident ensured  namespace=dev service=payment ..."
```

### Inspect the incident pipeline end to end

```bash
# 1. Phase-2 IncidentReports created by the exporter
kubectl get incidentreports -A -l rca.rca-operator.tech/agent=rca-exporter

# 2. Phase-1 IncidentReports created by the manager (for comparison)
kubectl get incidentreports -A -l rca.rca-operator.tech/agent!=rca-exporter
```

### Common issues

| Symptom | Likely cause | Fix |
|---|---|---|
| No IncidentReports created | Records arriving but `SeverityNumber < ERROR (17)` | Tag your logs with the proper severity at the SDK / collector level |
| One incident per pod instead of one per service | Resource attribute `service.name` missing | Add `service.name` via the OTel Collector's `resource` processor or set it in your app's SDK |
| Exporter pod restarts on OOM | Single replica in the MVP holds all per-service windows in memory | Increase `resources.limits.memory` or scope the upstream collector to fewer namespaces |
| Spikes fire continuously instead of once per cooldown | `--spike-cooldown` set to zero or very small | Use the default `5m` or higher; this should match `reporter.SignalCooldown` |
| `incidentreports.rca.rca-operator.tech is forbidden` in exporter logs | RBAC not applied | Re-run `make deploy-exporter` |

---

## Next steps

- Read [`api.md`](api.md) for full OTLP request/response details and `curl`/`grpcurl` examples
- Read [`development.md`](development.md) to build, test, and extend the exporter
- Read [`todos.md`](todos.md) for upcoming features and how to contribute
