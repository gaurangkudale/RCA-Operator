# test/fixtures

Local testing fixtures for RCA Operator — one file per scenario.  
These are **not** part of the automated test suite (no `go test` file references them). They exist to let you trigger specific watcher signals and verify the end-to-end incident lifecycle against a live cluster.

> **Prerequisite:** The operator must be running (`make run` or deployed) and the `development` / `default` namespaces must exist.

---

## Directory Layout

```
test/fixtures/
├── agents/
│   ├── rcaagent-sample.yaml        ← watches: development
│   └── rcaagent-development.yaml   ← watches: default
├── deployments/
│   └── deployment1.yaml            ← StalledRollout P2
├── nodes/
│   ├── simulate-not-ready.sh       ← NodeNotReady simulation (Kind only)
│   └── simulate-pressure.sh        ← DiskPressure / MemoryPressure / PIDPressure (any cluster)
└── pods/
    ├── crashloop.yaml              ← CrashLoopBackOff signal
    ├── oomkill.yaml                ← OOMKilled signal
    ├── image-pull-backoff.yaml     ← ImagePullBackOff signal
    ├── exit-code.yaml              ← CrashLoopBackOff with exit-code context (exit 127 = CommandNotFound)
    ├── grace-period-violation.yaml ← GracePeriodViolation signal
    ├── retention.yaml              ← Full create → resolve → prune lifecycle
    ├── probe-failure.yaml          ← ProbeFailure signal   (event_watcher)
    └── pod-eviction.yaml           ← PodEvicted signal     (event_watcher)
```

---

## Quick Start

```bash
# 1. Create namespaces (if they don't already exist)
kubectl create namespace development --dry-run=client -o yaml | kubectl apply -f -

# 2. Create notification secrets
kubectl create secret generic slack-webhook \
  --from-literal=webhookURL=https://hooks.slack.com/placeholder \
  -n development --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic pd-api-key \
  --from-literal=apiKey=pd-placeholder \
  -n development --dry-run=client -o yaml | kubectl apply -f -

# 3. Apply agents
kubectl apply -f test/fixtures/agents/

# 4. Apply the scenario pod you want to test (see table below)
kubectl apply -f test/fixtures/pods/crashloop.yaml
```

---

## Scenario Reference

| File | Namespace | Signal | Incident Type | Severity | Auto-resolved? |
|---|---|---|---|---|---|
| `pods/crashloop.yaml` | `default` | `CrashLoopBackOff` | `CrashLoopBackOff` | P3 | Yes — after pod recovers |
| `pods/oomkill.yaml` | `development` | `OOMKilled` | `OOMKilled` | P2 | Yes — after pod recovers |
| `pods/image-pull-backoff.yaml` | `development` | `ImagePullBackOff` | `ImagePullBackOff` | P3 | Manual (delete pod) |
| `pods/exit-code.yaml` | `development` | `CrashLoopBackOff` + exit-code context (exit 127) | `CrashLoopBackOff` | P3 | Yes — after pod recovers |
| `pods/grace-period-violation.yaml` | `development` | `GracePeriodViolation` | `GracePeriodViolation` | P2 | On pod delete |
| `pods/retention.yaml` | `default` | `CrashLoopBackOff` → `PodHealthy` | `CrashLoopBackOff` | P3 | Yes → pruned after `incidentRetention` |
| `pods/probe-failure.yaml` | `development` | `ProbeFailure` (Unhealthy event) | `ProbeFailure` | P3 | Yes — after pod restarts and becomes Ready |
| `pods/pod-eviction.yaml` | `development` | `PodEvicted` (Eviction API) | `PodEvicted` | P2 | Manual (delete pod + IncidentReport) |
| `deployments/deployment1.yaml` | `development` | `StalledRollout` (ProgressDeadlineExceeded) | `StalledRollout` | P2 | Manual (fix image or delete) |
| `nodes/simulate-not-ready.sh` | `default` | `NodeNotReady` (Kind node pause) | `NodeNotReady` | P1 | Automatic — after node unpauses |
| `nodes/simulate-pressure.sh disk` | `default` | `DiskPressure=True` (status patch) | `NodePressure` | P2 | Automatic — on script exit |
| `nodes/simulate-pressure.sh memory` | `default` | `MemoryPressure=True` (status patch) | `NodePressure` | P2 | Automatic — on script exit |
| `nodes/simulate-pressure.sh pid` | `default` | `PIDPressure=True` (status patch) | `NodePressure` | P3 | Automatic — on script exit |

> **event_watcher signals** (ProbeFailure, PodEvicted, NodeNotReady) originate from K8s Event objects
> rather than Pod/Node object state, so they are detected by `event_watcher.go` independently of `pod_watcher.go`.
> **node_watcher signals** (DiskPressure, MemoryPressure, PIDPressure, NodeNotReady) are detected by watching `corev1.Node`
> conditions directly — independent of K8s Event delivery.
> **deployment_watcher signals** (StalledRollout) are detected by watching `apps/v1 Deployment` conditions directly.

### Which agent watches which namespace

| Agent file | `watchNamespaces` | Scenario pods it covers |
|---|---|---|
| `agents/rcaagent-sample.yaml` | `development` | oomkill, image-pull-backoff, exit-code, grace-period-violation, probe-failure, pod-eviction, deployment1 |
| `agents/rcaagent-development.yaml` | `default` | crashloop, retention, node-not-ready, simulate-pressure |

---

## Watching Incidents

```bash
# Tail all incidents across namespaces
kubectl get incidentreports -A -w

# Inspect a specific incident
kubectl describe incidentreport <name> -n <namespace>

# Check operator logs for watcher signals
kubectl logs -n rca-system deploy/rca-operator-controller-manager -c manager -f \
  | grep -E 'watcher|incident|CrashLoop|OOM|ImagePull|GracePeriod|exitCode'

# Filter event_watcher-specific signals
kubectl logs -n rca-system deploy/rca-operator-controller-manager -c manager -f \
  | grep -E 'event-watcher|NodeNotReady|PodEvicted|ProbeFailure|Unhealthy|Evicted'

# Filter deployment watcher signals
kubectl logs -n rca-system deploy/rca-operator-controller-manager -c manager -f \
  | grep -E 'deployment-watcher|StalledRollout|ProgressDeadlineExceeded'

# Filter node watcher signals (pressure conditions)
kubectl logs -n rca-system deploy/rca-operator-controller-manager -c manager -f \
  | grep -E 'node-watcher|DiskPressure|MemoryPressure|PIDPressure|NodePressure'

```

---

## Cleanup

```bash
# Remove all fixture pods
kubectl delete -f test/fixtures/pods/ --ignore-not-found

# Remove fixture deployments
kubectl delete -f test/fixtures/deployments/ --ignore-not-found

# Remove fixture agents
kubectl delete -f test/fixtures/agents/ --ignore-not-found

# Remove node/eviction/probe incidents created by event_watcher scenarios
kubectl delete incidentreports -n development -l rca.rca-operator.tech/incident-type=NodeNotReady --ignore-not-found
kubectl delete incidentreports -n development -l rca.rca-operator.tech/incident-type=PodEvicted --ignore-not-found
kubectl delete incidentreports -n development -l rca.rca-operator.tech/incident-type=ProbeFailure --ignore-not-found
kubectl delete incidentreports -n default -l rca.rca-operator.tech/incident-type=NodeNotReady --ignore-not-found
kubectl delete incidentreports -n default -l rca.rca-operator.tech/incident-type=NodePressure --ignore-not-found

# Remove StalledRollout incidents created by deployment stall scenario
kubectl delete incidentreports -n development -l rca.rca-operator.tech/incident-type=StalledRollout --ignore-not-found
```

---

## Notes

- `incidentRetention: 5m` in both agent fixtures makes the retention scenario complete quickly. Change to a longer value if you need more observation time.
- In-memory watcher state (pending-alerted, healthy-alerted sets) resets on operator restart. A re-deployed operator re-fires suppressed events once via the bootstrap scan.
- **Dedup window**: `event_watcher` suppresses duplicate signals within a 2-minute window. If you re-apply the same scenario immediately after cleanup, wait 2+ minutes or restart the operator.
- **probe-failure** — the pod restarts once automatically (liveness kills it after failing), then serves immediately and the incident self-resolves. Total lifecycle: ~60–90 s.
- **pod-eviction** — the Eviction API (`kubectl evict`) terminates the pod without rescheduling (`restartPolicy: Never`). Delete the pod and IncidentReport manually when done.
- **node-not-ready** — requires a Kind cluster. The `simulate-not-ready.sh` script pauses the Docker container backing a Kind worker node; after the uninterrupted 40 s `node-monitor-grace-period` the kube-controller-manager fires a `NodeNotReady` K8s Event. Run `kubectl get nodes -w` in a separate terminal to watch the status change.
- **simulate-pressure** — works against any Kubernetes cluster (no Docker required). Uses `kubectl patch --subresource=status` to inject the pressure condition directly on the Node object. The kubelet heartbeat overwrites the patch every ~10 s, so the script re-patches every 8 s during the observation window. On exit (or Ctrl-C), the condition is restored to `False`. The `NodeWatcher` picks up the change via the informer within ~1 reconcile period (30 s scan or informer push, whichever fires first). Requires `python3` for the JSON condition array manipulation.
- **deployment1** — after `kubectl apply`, the pods enter `ImagePullBackOff` (nonexistent image). After `progressDeadlineSeconds: 60` Kubernetes sets `Progressing=False/ProgressDeadlineExceeded`. The `DeploymentWatcher` detects this and emits a `StalledRolloutEvent`, creating a `StalledRollout` P2 incident.
- **exit-code** — a non-zero exit no longer creates a separate `ExitCode` incident. If the pod enters `CrashLoopBackOff`, the `CrashLoopBackOff` incident summary includes `exitCode`, `category`, and `description` fields.
- See [docs/concepts/Architecture.md](../../docs/concepts/Architecture.md) for the Phase 1 runtime model.
