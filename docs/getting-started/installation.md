# Installation

RCA Operator ships as a Helm chart. The chart bundles the operator plus an
optional OpenTelemetry stack (OTel Operator + Collector DaemonSet + Jaeger)
so you get end-to-end tracing and correlation out of the box.

You have three supported install paths, in order of what most users want:

1. [Full stack — operator + observability](#1-full-stack-recommended) *(recommended)*
2. [Minimal — operator only, bring your own observability](#2-minimal-operator-only)
3. [From source — developer / contributor path](#3-from-source)

---

## 1. Full stack *(recommended)*

Installs the operator, the OpenTelemetry Operator, an OTel Collector DaemonSet,
and Jaeger — all wired together. This is what you want for a demo, a new
cluster, or any environment that doesn't already have tracing in place.

```bash
# Add chart repositories (one-time)
helm repo add rca-operator   https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo add opentelemetry  https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add jaegertracing  https://jaegertracing.github.io/helm-charts
helm repo update

# Install
helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --wait --timeout 10m
```

> **`--wait` is required.** The `OpenTelemetryCollector` and `Instrumentation`
> resources are applied as post-install hooks, which need the OTel Operator
> webhook to be Ready first.

### Verify

```bash
# All four pods should be Running
kubectl get pods -n rca-system
# rca-operator-controller-manager-*         1/1  Running
# rca-operator-opentelemetry-operator-*     2/2  Running
# rca-operator-jaeger-*                     1/1  Running
# rca-operator-otel-collector-*             1/1  Running   (DaemonSet, one per node)

# CRDs registered
kubectl get crd rcaagents.rca.rca-operator.tech \
               incidentreports.rca.rca-operator.tech \
               rcacorrelationrules.rca.rca-operator.tech

# Default correlation rules loaded
kubectl get rcacorrelationrules
```

You now have four default correlation rules, an OTLP ingest endpoint at
`rca-operator-otel-ingest.rca-system:4319`, and a dashboard at
`rca-operator-dashboard.rca-system:9090`. Jump to the
[Quick Start](quickstart.md) to trigger your first incident.

---

## 2. Minimal — operator only

Use this if you already run an OTel Collector / Jaeger / other tracing backend
and just want the RCA Operator control plane.

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update

helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set opentelemetryOperator.enabled=false \
  --set jaeger.enabled=false \
  --set otelCollector.enabled=false \
  --set instrumentation.enabled=false \
  --wait --timeout 5m
```

The operator will run without OTLP ingest or topology enrichment. You can
re-enable either piece by pointing at an existing Jaeger / Collector:

```bash
# Point the topology graph builder at an existing Jaeger Query endpoint
--set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686

# Or forward spans from your existing Collector to the operator's ingest
# endpoint (once rcaIngest.enabled=true) — see docs/features/otlp-ingest.md
```

---

## 3. From source

For contributors and for pinning a specific `values.yaml` override locally.

```bash
git clone https://github.com/gaurangkudale/RCA-Operator.git
cd RCA-Operator

helm dep update ./helm
helm upgrade --install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  --wait --timeout 10m
```

See [Helm Installation Reference](../helm-installation.md) for all override
flags, storage/backend options, and CRD adoption during upgrades.

---

## Uninstall

```bash
helm uninstall rca-operator -n rca-system

# CRDs are cluster-scoped — remove only if nothing else in the cluster uses them
kubectl delete crd \
  rcaagents.rca.rca-operator.tech \
  incidentreports.rca.rca-operator.tech \
  rcacorrelationrules.rca.rca-operator.tech
```

---

Next: [Quick Start](quickstart.md)
