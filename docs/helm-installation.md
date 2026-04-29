# RCA Operator — Helm Reference

> **Chart version:** `0.1.0` | **Kubernetes:** ≥ 1.26

For a first-time install, start with [Getting Started → Installation](getting-started/installation.md).
This page is the **reference** for installing from source, upgrading, override
flags, and troubleshooting.

---

## What gets installed

| Component | Kind | Version |
|-----------|------|---------|
| RCA Operator | Deployment | 0.1.0 |
| OpenTelemetry Operator | Deployment | 0.109.2 |
| OTel Collector | DaemonSet (one pod / node) | 0.121.0 |
| Jaeger | Deployment | 4.7.0 |

**Data flow**
```
App → OTel Collector (:4318 HTTP) → Jaeger (:4317 gRPC) → Jaeger UI (:16686)
```

---

## Prerequisites

```bash
# Helm ≥ 3.12
helm version

# Add repositories (one-time)
helm repo add opentelemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add jaegertracing  https://jaegertracing.github.io/helm-charts
helm repo update
```

---

## Install

### Profile: full

```bash
# Pull sub-charts (opentelemetry-operator + jaeger) into helm/charts/
helm dep update ./helm

# Install — --wait ensures the operator webhook is ready before CRs are applied
helm upgrade --install rca-operator ./helm \
  --namespace rca-system \
  --create-namespace \
  -f helm/values-full.yaml \
  --wait \
  --timeout 10m
```

> **`--wait` is required.**  
> The `OpenTelemetryCollector` and `Instrumentation` CRs are applied as
> post-install hooks. Without `--wait`, the hooks fire before the
> otel-operator webhook is ready and the CRs are rejected.

### Profile: minimal

```bash
helm upgrade --install rca-operator ./helm \
  --namespace rca-system \
  --create-namespace \
  -f helm/values-minimal.yaml \
  --wait \
  --timeout 5m
```

### Profile: external observability

```bash
helm upgrade --install rca-operator ./helm \
  --namespace rca-system \
  --create-namespace \
  -f helm/values-external-observability.yaml \
  --set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686 \
  --wait \
  --timeout 5m
```

---

## Verify

```bash
# All four pods should be Running
kubectl get pods -n rca-system

# NAME                                              READY   STATUS
# rca-operator-controller-manager-*                 1/1     Running
# rca-operator-opentelemetry-operator-*              2/2     Running
# rca-operator-jaeger-*                              1/1     Running
# rca-operator-otel-collector-*                      1/1     Running  ← DaemonSet

# Confirm CRs were applied by the hooks
kubectl get opentelemetrycollector,instrumentation -n rca-system
```

---

## Access Jaeger UI

```bash
kubectl port-forward -n rca-system svc/rca-operator-jaeger 16686:16686
```

Open **http://localhost:16686**

---

## Send traces from your application

### Option A — OTLP SDK (any language)

Set these env vars on your application pod:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://rca-operator-otel-collector.rca-system:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_SERVICE_NAME=<your-service-name>
```

### Option B — Zero-code auto-instrumentation (annotation)

Add **one annotation** to your Deployment — no code changes needed.

```bash
# Python
kubectl patch deployment <my-app> -n <app-namespace> -p \
  '{"spec":{"template":{"metadata":{"annotations":
    {"instrumentation.opentelemetry.io/inject-python":"rca-operator/rca-operator-instrumentation"}}}}}'

# Java
kubectl patch deployment <my-app> -n <app-namespace> -p \
  '{"spec":{"template":{"metadata":{"annotations":
    {"instrumentation.opentelemetry.io/inject-java":"rca-operator/rca-operator-instrumentation"}}}}}'

# Node.js
kubectl patch deployment <my-app> -n <app-namespace> -p \
  '{"spec":{"template":{"metadata":{"annotations":
    {"instrumentation.opentelemetry.io/inject-nodejs":"rca-operator/rca-operator-instrumentation"}}}}}'
```

> The annotation value is `<namespace>/<instrumentation-cr-name>`.

---

## RCA Agent

```bash
kubectl apply -f - <<EOF
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: default-agent
  namespace: rca-system
spec:
  watchNamespaces:
    - default        # namespaces to monitor for incidents
  incidentRetention: "7d"
EOF

# View detected incidents
kubectl get incidentreports -A
```

---

## Values overlays shipped with the chart

| File | Purpose |
|---|---|
| [`helm/values.yaml`](../helm/values.yaml) | Chart defaults — safe for evaluation and small clusters |
| [`helm/values-full.yaml`](../helm/values-full.yaml) | Explicit full-stack profile: operator + OTel Operator + Collector + Jaeger + instrumentation |
| [`helm/values-minimal.yaml`](../helm/values-minimal.yaml) | Operator-only profile: no bundled observability and no OTLP ingest surface |
| [`helm/values-external-observability.yaml`](../helm/values-external-observability.yaml) | Operator + ingest endpoint for existing Collector/Jaeger deployments |
| [`helm/values-dev.yaml`](../helm/values-dev.yaml) | Local kind development: `pullPolicy: Never`, single replica, PDB off, low-resource Jaeger |
| [`helm/values-production.yaml`](../helm/values-production.yaml) | Production hardening: pinned image, `replicaCount: 2`, pod anti-affinity across zones, network policies, self-telemetry, auto-detect off by default |

Apply an overlay with `-f`:

```bash
helm upgrade --install rca-operator rca-operator/rca-operator \
  -f helm/values-production.yaml \
  --namespace rca-system --create-namespace \
  --wait --timeout 10m
```

Review the header comment in `values-production.yaml` — it lists the fields
you **must** edit before applying (image tag, Jaeger storage backend,
instrumentation target namespaces).

---

## Common configuration overrides

```bash
# Disable the full observability stack (RCA Operator only)
--set opentelemetryOperator.enabled=false \
--set jaeger.enabled=false \
--set otelCollector.enabled=false \
--set instrumentation.enabled=false \
--set rcaIngest.enabled=false \
--set graphBuilder.jaegerQueryURL=""

# Use an external Jaeger already running in the cluster
--set jaeger.enabled=false \
--set otelCollector.jaegerEndpoint="jaeger.observability.svc:4317" \
--set graphBuilder.jaegerQueryURL="http://jaeger-query.observability.svc:16686"

# Production trace storage (Elasticsearch)
--set jaeger.storage.type=elasticsearch \
--set jaeger.storage.elasticsearch.host=elasticsearch-master \
--set jaeger.storage.elasticsearch.port=9200

# Traces only — disable log collection
--set otelCollector.filelog.enabled=false

# Reduce sampling for high-traffic clusters (10 %)
--set instrumentation.sampler.type=parentbased_traceidratio \
--set instrumentation.sampler.argument="0.1"
```

---

## Upgrade

```bash
helm dep update ./helm   # refresh sub-chart archives if Chart.yaml changed

helm upgrade rca-operator ./helm \
  --namespace rca-system \
  --wait \
  --timeout 10m
```

RCA CRDs are cluster-scoped and should be applied explicitly before upgrading
an existing release when schemas change. Hook-created resources such as default
`RCACorrelationRule`s, `OpenTelemetryCollector`, and `Instrumentation` are
re-applied on post-upgrade after their CRDs/webhooks are available. See
[Helm Upgrade Guide](HELM_UPGRADE.md) for the full ownership model.

> **If CRDs already exist in the cluster** (installed outside Helm), adopt them first:
>
> ```bash
> for crd in opentelemetrycollectors.opentelemetry.io instrumentations.opentelemetry.io; do
>   kubectl label crd $crd app.kubernetes.io/managed-by=Helm --overwrite
>   kubectl annotate crd $crd \
>     meta.helm.sh/release-name=rca-operator \
>     meta.helm.sh/release-namespace=rca-system \
>     --overwrite
> done
> ```

---

## Uninstall

```bash
helm uninstall rca-operator -n rca-system

# CRDs are cluster-scoped — remove manually if no longer needed
kubectl delete crd \
  opentelemetrycollectors.opentelemetry.io \
  instrumentations.opentelemetry.io \
  rcaagents.rca.rca-operator.tech \
  incidentreports.rca.rca-operator.tech \
  rcacorrelationrules.rca.rca-operator.tech
```

Deleting CRDs deletes their custom resources, including historical
`IncidentReport`s. Back up or export CRs before removing CRDs from shared
clusters.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `OpenTelemetryCollector` CR not created after install | Hooks fired before operator webhook was ready | Run `helm upgrade ... --wait` — triggers post-upgrade hooks with operator already running |
| `invalid ownership metadata` on CRDs | CRDs exist in cluster without Helm labels | Run the CRD adoption script in the **Upgrade** section above |
| `Secret name invalid: must be lowercase` | Helm alias used as Chart.Name (camelCase) | `nameOverride: "opentelemetry-operator"` is set in `values.yaml` — ensure it is present |
| OTel Collector pod in `Pending` | DaemonSet resource limits too high for nodes | Lower `otelCollector.resources.requests` in `values.yaml` |
| No traces in Jaeger | Wrong OTLP endpoint in app | Use `http://rca-operator-otel-collector.rca-system:4318` |
