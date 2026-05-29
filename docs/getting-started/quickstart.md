# Quick Start

Get from a fresh install to a real incident in the dashboard in about five
minutes. This assumes you completed [Installation](installation.md) — pods
in `rca-system` should already be `Running`.

---

## 1. Confirm the starter RCAAgent

The chart's post-install hook creates a starter `RCAAgent` named `default` in
the release namespace. It watches the release namespace out of the box.

```bash
kubectl get rcaagent -n rca-system   # READY should be True
```

To watch additional namespaces, edit the starter agent (or skip the starter
with `--set defaultAgent.enabled=false` and apply your own):

```bash
kubectl edit rcaagent default -n rca-system
# add namespaces under spec.watchNamespaces
```

Or define your own from scratch — one agent can cover multiple namespaces:

```yaml
# rca-agent.yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: app-agent
  namespace: rca-system
spec:
  watchNamespaces:
    - default
    - rca-demo
  incidentRetention: 7d
```

```bash
kubectl apply -f rca-agent.yaml
```

---

## 2. Open the dashboard

The chart installs a dashboard service on port 9090.

```bash
kubectl port-forward -n rca-system svc/rca-operator-dashboard 9090:9090
```

Open <http://localhost:9090>. The incidents list starts empty — we'll fill it
in the next step.

---

## 3. Trigger a test incident

The repo ships fixture pods that fail in known-correlatable ways.

```bash
# Clone fixtures if you installed via Helm repo
git clone --depth 1 https://github.com/gaurangkudale/RCA-Operator.git /tmp/rca

# The fixture deploys into the rca-demo namespace — create it first
kubectl create namespace rca-demo
kubectl apply -f /tmp/rca/test/fixtures/pods/crashloop.yaml

# Watch the incident get created
kubectl get incidentreports -n rca-demo -w
```

Within ~30 seconds you should see an `IncidentReport` appear in
`Detecting` → `Active` phase. Refresh the dashboard — the incident shows up
with its timeline, affected pod, and (if the OTLP stack is installed) a
topology graph.

All fixtures: [test/fixtures/README.md](../../test/fixtures/README.md).

---

## 4. What just happened

1. **Signal collection.** The pod's `BackOff` + container `CrashLoopBackOff`
   events reached the operator's watcher.
2. **Correlation.** The CrashLoopBackOff signal flowed through the correlator
   buffer. With only a single signal the multi-signal default rules don't fire,
   but the operator still records a `CrashLoopBackOff` incident from the
   single signal. To see a correlated multi-signal incident (e.g.
   `crashloop-plus-oom`), apply a fixture that produces both crash loops and
   OOM kills.
3. **Incident creation.** The reporter deduplicated signals into a single
   `IncidentReport` CR, set its phase to `Active`, and emitted a Kubernetes
   Event.
4. **Dashboard render.** The dashboard API reads `IncidentReport` CRs
   directly — no database, no scraping.

If you installed the full stack, OTLP spans from instrumented apps also flow
into the correlator. See [OTLP Ingest](../features/otlp-ingest.md).

---

## 5. Clean up the test

```bash
kubectl delete -f /tmp/rca/test/fixtures/pods/crashloop.yaml
# The IncidentReport will transition to Resolved automatically
```

---

## Next steps

- Add notifications → [RCAAgent CRD Reference](../reference/rcaagent-crd.md#specnotifications)
- Write your own correlation rule → [RCACorrelationRule Reference](../reference/rcacorrelationrule-crd.md)
- Auto-detect patterns → [Auto-Detection](../features/auto-detection.md)
- Stream traces from your apps → [OTLP Ingest](../features/otlp-ingest.md)
- Understand the topology graph → [Topology Graph](../features/topology-graph.md)
