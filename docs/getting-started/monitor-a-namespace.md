# Monitor an Existing Namespace End-to-End

You have a namespace — say `payments` — running 5–6 microservices written in
different languages (Python, Java, Node.js, Go, .NET), and **no monitoring** in
place. This guide takes you from nothing to full incident detection and a
trace-aware topology view, without changing a single line of application code.

RCA Operator gives you monitoring in **two layers**:

| Layer | What you get | App changes | Works for |
|---|---|---|---|
| **1. Kubernetes-native signals** | Crash loops, OOMKills, ImagePullBackOff, evictions, stalled rollouts, failed Jobs → `IncidentReport`s | None | **All** languages (pod/event level) |
| **2. Trace-aware correlation** | Request-level error/latency correlation, service topology graph, inline trace view | One pod annotation per service (auto-instrumentation, no code) | Python, Java, Node.js, Go, .NET |

Layer 1 works the moment an `RCAAgent` watches your namespace. Layer 2 adds
distributed-trace context on top. Most teams do Layer 1 first, then opt
services into Layer 2.

---

## Prerequisites

- A Kubernetes cluster (1.26+) and `kubectl` + `helm` configured against it.
- Your workloads already running in the `payments` namespace.

Replace `payments` with your real namespace name throughout.

---

## Step 1 — Install RCA Operator and point it at your namespace

Install the full stack (operator + OpenTelemetry Operator + Collector + Jaeger)
in one shot. Two values tie it to your namespace:

- `defaultAgent.watchNamespaces` — which namespaces the operator watches for incidents.
- `instrumentation.targetNamespaces` — which namespaces get a copy of the
  auto-instrumentation `Instrumentation` CR (needed for Layer 2).

```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts
helm repo update

helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set 'defaultAgent.watchNamespaces={payments}' \
  --set 'instrumentation.targetNamespaces={payments}' \
  --wait --timeout 10m
```

> `--wait` is required — the Collector and Instrumentation resources are applied
> as post-install hooks once the OTel Operator webhook is Ready.

This creates a starter `RCAAgent` named `default` that watches `payments`, and
mirrors the `Instrumentation` CR into `payments` so pods there can opt into
tracing.

### Verify the install

```bash
# Operator + observability stack Running
kubectl get pods -n rca-system

# The agent is watching your namespace (READY = True)
kubectl get rcaagent -n rca-system

# The Instrumentation CR exists in YOUR namespace (enables Layer 2)
kubectl get instrumentation -n payments
```

**At this point Layer 1 is live.** Any pod in `payments` that crash-loops, gets
OOMKilled, fails to pull an image, or is evicted will produce an
`IncidentReport` automatically.

---

## Step 2 — See Layer 1 working

Open the dashboard:

```bash
kubectl port-forward -n rca-system svc/rca-operator-dashboard 9090:9090
# → http://localhost:9090
```

Watch incidents appear as they happen:

```bash
kubectl get incidentreports -n payments -w
```

If your services are healthy, the list is empty — that's expected. To prove the
pipeline, you can temporarily scale a Deployment to a bad image or let a known
flaky service crash; an incident shows up within ~30s.

---

## Step 3 — Turn on trace-aware monitoring (Layer 2)

Auto-instrumentation works by adding **one annotation to each service's pod
template**, matched to its language. The OTel Operator injects an init-container
that installs the SDK and sets `OTEL_*` env vars — **no code or image rebuild**.

Pick the annotation per service:

| Language | Pod-template annotation |
|---|---|
| Python  | `instrumentation.opentelemetry.io/inject-python: "true"` |
| Java    | `instrumentation.opentelemetry.io/inject-java: "true"` |
| Node.js | `instrumentation.opentelemetry.io/inject-nodejs: "true"` |
| .NET    | `instrumentation.opentelemetry.io/inject-dotnet: "true"` |
| Go      | `instrumentation.opentelemetry.io/inject-go: "true"` |

Add it under `spec.template.metadata.annotations` of each Deployment. For
example, a Python service and a Java service:

```bash
# Python service
kubectl patch deployment checkout -n payments --type merge -p \
  '{"spec":{"template":{"metadata":{"annotations":{"instrumentation.opentelemetry.io/inject-python":"true"}}}}}'

# Java service
kubectl patch deployment ledger -n payments --type merge -p \
  '{"spec":{"template":{"metadata":{"annotations":{"instrumentation.opentelemetry.io/inject-java":"true"}}}}}'
```

Repeat for each microservice with its matching language. The patch triggers a
rollout, so the injected pods come up instrumented. (Annotations are applied at
pod creation — existing pods must be recreated, which the patch does for you. If
you edit the manifest in Git instead, run `kubectl rollout restart deployment/<name> -n payments`.)

> **Go is special.** The Go auto-instrumentation uses an eBPF agent and needs
> elevated privileges; check the
> [OTel Go instrumentation docs](https://opentelemetry.io/docs/zero-code/go/)
> if a Go service doesn't emit spans.

### Verify Layer 2

```bash
# Injected pods gain an opentelemetry init-container + OTEL_* env
kubectl get pod -n payments <pod> -o jsonpath='{.spec.initContainers[*].name}'

# Traces reaching Jaeger
kubectl port-forward -n rca-system svc/rca-operator-jaeger 16686:16686
# → http://localhost:16686  (pick a service, find traces)
```

Once traces flow, the dashboard's **Topology** tab shows the service
dependency graph, and new incidents carry trace IDs and an inline trace
waterfall. Error spans (HTTP 5xx, span status ERROR) and latency spikes from
your services are correlated into incidents automatically.

---

## How the pieces connect

```text
your microservices (payments ns)
   │  auto-instrumented via pod annotation (Layer 2)
   ▼
OTel Collector (DaemonSet)
   ├─► Jaeger              (trace storage + UI)
   └─► RCA OTLP ingest     (filtered error/latency spans → signals)
                                  │
Kubernetes API ──► RCAAgent ──────┤   (pod/event signals, Layer 1)
(pod/event/node watchers)         ▼
                          Correlation rules ──► IncidentReport CRs ──► Dashboard
```

- **Layer 1** needs only the `RCAAgent` watching `payments`.
- **Layer 2** needs the `Instrumentation` CR in `payments` (Step 1) **and** the
  per-service annotation (Step 3).

---

## Common adjustments

**Watch more namespaces later** — edit the agent and add to `watchNamespaces`:

```bash
kubectl edit rcaagent default -n rca-system
```

…and re-run `helm upgrade` with the namespace added to
`instrumentation.targetNamespaces` so those pods can be auto-instrumented too.

**Tune trace sampling** (default samples 100% of traces) — set on install/upgrade:

```bash
--set instrumentation.sampler.type=parentbased_traceidratio \
--set instrumentation.sampler.argument=0.1   # sample 10%
```

**Add notifications** (Slack / PagerDuty) — see
[RCAAgent CRD Reference](../reference/rcaagent-crd.md#specnotifications).

**Already run your own Collector/Jaeger?** Use the external-observability
profile instead — see [Installation](installation.md#3-external-observability).

---

## Cheat sheet

```bash
# 1. Install + bind to your namespace
helm upgrade --install rca-operator rca-operator/rca-operator \
  --namespace rca-system --create-namespace \
  --set 'defaultAgent.watchNamespaces={payments}' \
  --set 'instrumentation.targetNamespaces={payments}' \
  --wait --timeout 10m

# 2. (Layer 2) annotate each service for its language
kubectl patch deployment <svc> -n payments --type merge -p \
  '{"spec":{"template":{"metadata":{"annotations":{"instrumentation.opentelemetry.io/inject-<lang>":"true"}}}}}'

# 3. Watch incidents + open the dashboard
kubectl get incidentreports -n payments -w
kubectl port-forward -n rca-system svc/rca-operator-dashboard 9090:9090
```

---

Related: [Installation](installation.md) · [Quick Start](quickstart.md) ·
[OTLP Ingest](../features/otlp-ingest.md) ·
[Topology Graph](../features/topology-graph.md)
