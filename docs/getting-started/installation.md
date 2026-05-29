# Installation

RCA Operator ships as a Helm chart. The chart bundles the operator plus an
optional OpenTelemetry stack (OTel Operator + Collector DaemonSet + Jaeger)
so you get end-to-end tracing and correlation out of the box.

You have four supported install paths, in order of what most users want:

0. [One-line installer](#0-one-line-installer) *(fastest)*
1. [Full stack — operator + observability](#1-full-stack-recommended) *(recommended for Helm users)*
2. [Minimal — operator only](#2-minimal-operator-only)
3. [External observability — bring your own Collector and Jaeger](#3-external-observability)
4. [From source — developer / contributor path](#4-from-source)

---

## 0. One-line installer

For new clusters and demos. Wraps the full-stack Helm install below and creates
a starter `RCAAgent` automatically so the operator begins detecting incidents
immediately.

```bash
curl -fsSL https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main/scripts/install.sh | bash
```

Tunable with environment variables (default values shown):

```bash
RCA_NAMESPACE=rca-system \
RCA_RELEASE=rca-operator \
RCA_PROFILE=full \
RCA_CHART_VERSION= \
RCA_VALUES_FILE= \
  curl -fsSL https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main/scripts/install.sh | bash
```

Set `RCA_PROFILE=minimal` for the operator-only install (no bundled
otel-collector / Jaeger). Set `RCA_DRY_RUN=1` to print the commands without
executing them.

---

## 1. Full stack *(recommended)*

Installs the operator, the OpenTelemetry Operator, an OTel Collector DaemonSet,
and Jaeger — all wired together. This is what you want for a demo, a new
cluster, or any environment that doesn't already have tracing in place.

The otel-operator and Jaeger Helm charts are bundled as **vendored sub-charts**
inside this chart, so you only need to add one repo.

```bash
# One repo, one install.
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update

helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --wait --timeout 10m
```

A starter `RCAAgent` is created automatically by a post-install hook so the
operator starts detecting incidents in the release namespace immediately.
Disable with `--set defaultAgent.enabled=false` if you manage agents
declaratively (e.g. GitOps).

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

From source:

```bash
git clone https://github.com/gaurangkudale/RCA-Operator.git
cd RCA-Operator

helm dep update ./helm
helm upgrade --install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-minimal.yaml \
  --wait --timeout 5m
```

From the published chart:

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update

helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set opentelemetryOperator.enabled=false \
  --set jaeger.enabled=false \
  --set otelCollector.enabled=false \
  --set instrumentation.enabled=false \
  --set rcaIngest.enabled=false \
  --set graphBuilder.jaegerQueryURL="" \
  --wait --timeout 5m
```

The operator will run without OTLP ingest or Jaeger enrichment. Kubernetes
watchers, CRD correlation rules, dashboard, metrics, and notifications still
work.

---

## 3. External observability

Use this if you already run an OTel Collector and Jaeger. The chart installs
only RCA Operator and its OTLP ingest Service; your existing Collector forwards
filtered telemetry into that endpoint.

From source:

```bash
git clone https://github.com/gaurangkudale/RCA-Operator.git
cd RCA-Operator

helm dep update ./helm
helm upgrade --install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-external-observability.yaml \
  --set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686 \
  --wait --timeout 5m
```

From the published chart:

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update

helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set opentelemetryOperator.enabled=false \
  --set jaeger.enabled=false \
  --set otelCollector.enabled=false \
  --set instrumentation.enabled=false \
  --set rcaIngest.enabled=true \
  --set rcaIngest.networkPolicy.enabled=false \
  --set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686 \
  --wait --timeout 5m
```

Forward traces/logs from your existing Collector to
`http://rca-operator-otel-ingest.rca-system.svc.cluster.local:4319`. See
[OTLP Ingest](../features/otlp-ingest.md#forwarding-from-an-existing-collector)
for the pipeline snippet.

---

## 4. From source

For contributors and for pinning a specific `values.yaml` override locally.

```bash
git clone https://github.com/gaurangkudale/RCA-Operator.git
cd RCA-Operator

helm dep update ./helm
helm upgrade --install rca-operator ./helm \
  --namespace rca-system --create-namespace \
  -f helm/values-full.yaml \
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

Helm uninstall removes chart-managed Deployments, Services, RBAC, hooks, and
default rules, but CRDs and existing custom resources may remain depending on
cluster policy and `crds.keep`. Review [Helm Upgrade Guide](../HELM_UPGRADE.md)
before deleting CRDs in shared clusters.

---

## Admission webhooks (advanced, opt-in)

The operator can validate `RCAAgent` and `RCACorrelationRule` resources at
admission time (`--enable-webhooks`), so an invalid spec is rejected by the API
server rather than surfacing when the operator later tries to load it.

This is **off by default** and is not part of the base install, because it
requires webhook serving infrastructure to be provisioned first:

1. A TLS **serving certificate** mounted at the manager's webhook cert path
   (typically issued by [cert-manager](https://cert-manager.io)).
2. A **`ValidatingWebhookConfiguration`** pointing the API server at the
   operator's webhook Service, with the CA bundle injected.

Enabling the flag without that infrastructure present crashes the manager on
startup (the webhook server cannot find its serving certificate). Until the
chart ships these resources, treat webhooks as an advanced, self-managed
opt-in. Track progress in the project issues before turning it on in
production.

---

Next: [Quick Start](quickstart.md)
