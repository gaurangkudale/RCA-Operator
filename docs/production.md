# Production Guide

This page summarizes the production knobs to review before running RCA Operator
on a shared or high-traffic cluster.

## Install Profile

Use one of the explicit profiles:

| Profile | Use when |
|---|---|
| `helm/values-full.yaml` | You want the operator, OTel Operator, Collector, Jaeger, and Instrumentation installed together |
| `helm/values-minimal.yaml` | You only want Kubernetes-native incident detection and dashboarding |
| `helm/values-external-observability.yaml` | You already operate Collector and Jaeger and want RCA Operator to ingest filtered telemetry |

For production, start from `helm/values-production.yaml` and merge in the
profile values that match your environment.

## Resource Sizing

The manager defaults target small clusters. Increase requests/limits when you
watch many namespaces or enable OTLP ingest:

| Cluster shape | Manager request | Manager limit |
|---|---:|---:|
| dev / kind | `50m`, `64Mi` | `200m`, `256Mi` |
| small, up to 20 namespaces | `100m`, `128Mi` | `500m`, `512Mi` |
| medium, 20-100 namespaces | `200m`, `256Mi` | `1000m`, `1Gi` |
| large or heavy OTLP ingest | `500m`, `512Mi` | `2000m`, `2Gi` |

Scale memory first if incident graphs or trace ingest cause OOMKills.

## RBAC Scope

The operator needs cluster-wide read access to watched Kubernetes resources and
write access to RCA custom resources:

- read `pods`, `pods/log`, `events`, `nodes`, `services`, workloads, jobs, and cronjobs
- write `IncidentReport`, `RCAAgent` status/finalizers, and `RCACorrelationRule` status
- read notification `Secret` references in the RCAAgent namespace

Review [RBAC Reference](reference/rbac.md) before reducing permissions.

## Network Policies

The full profile enables an OTLP ingest `NetworkPolicy` that only allows the
bundled Collector pods to reach the operator ingest port. If you use an
external Collector, either:

- disable the built-in policy with `rcaIngest.networkPolicy.enabled=false`, or
- customize selectors so only your Collector namespace/service account can reach port `4319`.

Dashboard and metrics Services are ClusterIP by default. Put authentication in
front of any external dashboard ingress.

## Jaeger Storage

The bundled Jaeger all-in-one deployment is for evaluation and small clusters.
For production:

- use an external Jaeger or configure persistent storage
- define retention outside the RCA chart
- monitor Jaeger query latency because `/api/traces/{id}` depends on it

## Retention

`RCAAgent.spec.incidentRetention` controls how long resolved incidents remain.
Use shorter retention for high-churn clusters and longer retention where audit
history matters.

Incident graphs can be larger than simple status fields. Keep retention aligned
with etcd capacity and incident volume.

## Cardinality Limits

Keep Prometheus labels and rule attributes bounded:

- avoid using pod UID, trace ID, or full error messages as Prometheus labels
- keep rule names stable and low-cardinality
- keep `RCACorrelationRule` attribute predicates focused on semantic fields
- tune OTLP filters so only useful spans/logs enter the correlator

The operator caps stored trace IDs per incident and trims incident timeline /
signal arrays to protect CR size.

## Security Posture

Recommended production defaults:

- run with `replicaCount: 2` and leader election enabled
- keep `readOnlyRootFilesystem`, `runAsNonRoot`, and dropped capabilities
- keep `metrics.secure=true`
- keep dashboard Service internal unless protected by ingress authentication
- keep OTel ingest restricted by NetworkPolicy
- use immutable image tags, not `latest`
- disable `autoDetect.enabled` until you trust cluster baselines

## Pre-Release Verification

Before publishing a release, run:

```bash
scripts/verify-prerelease-install.sh
```

Set `RUN_KIND_INSTALL=false` to run only lint/template/kubeconform checks.
