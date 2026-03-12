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
└── pods/
    ├── crashloop.yaml              ← CrashLoopBackOff signal
    ├── oomkill.yaml                ← OOMKilled signal
    ├── image-pull-backoff.yaml     ← ImagePullBackOff signal
    ├── exit-code.yaml              ← ContainerExitCode (exit 127 = CommandNotFound)
    ├── grace-period-violation.yaml ← GracePeriodViolation signal
    └── retention.yaml              ← Full create → resolve → prune lifecycle
```

---

## Quick Start

```bash
# 1. Create namespaces (if they don't already exist)
kubectl create namespace development --dry-run=client -o yaml | kubectl apply -f -

# 2. Create secrets (operator requires these before it marks Available=True)
kubectl create secret generic rca-agent-openai-secret \
  --from-literal=apiKey=sk-test-placeholder \
  -n development --dry-run=client -o yaml | kubectl apply -f -

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

| File | Namespace | Signal | Incident Type | Auto-resolved? |
|---|---|---|---|---|
| `pods/crashloop.yaml` | `default` | `CrashLoopBackOff` | `CrashLoopBackOff` | Yes — after pod recovers |
| `pods/oomkill.yaml` | `development` | `OOMKilled` | `OOMKilled` | Yes — after pod recovers |
| `pods/image-pull-backoff.yaml` | `development` | `ImagePullBackOff` | `ImagePullBackOff` | Manual (delete pod) |
| `pods/exit-code.yaml` | `development` | `ContainerExitCode` (exit 127) | `ExitCode` | Yes — after pod recovers |
| `pods/grace-period-violation.yaml` | `development` | `GracePeriodViolation` | `GracePeriodViolation` | On pod delete |
| `pods/retention.yaml` | `default` | `CrashLoopBackOff` → `PodHealthy` | `CrashLoopBackOff` | Yes → pruned after `incidentRetention` |

### Which agent watches which namespace

| Agent file | `watchNamespaces` | Scenario pods it covers |
|---|---|---|
| `agents/rcaagent-sample.yaml` | `development` | oomkill, image-pull-backoff, exit-code, grace-period-violation |
| `agents/rcaagent-development.yaml` | `default` | crashloop, retention |

---

## Watching Incidents

```bash
# Tail all incidents across namespaces
kubectl get incidentreports -A -w

# Inspect a specific incident
kubectl describe incidentreport <name> -n <namespace>

# Check operator logs for watcher signals
kubectl logs -n rca-operator-system deploy/rca-operator-controller-manager -c manager -f \
  | grep -E 'watcher|incident|CrashLoop|OOM|ImagePull|ExitCode|GracePeriod'
```

---

## Cleanup

```bash
# Remove all fixture pods
kubectl delete -f test/fixtures/pods/ --ignore-not-found

# Remove fixture agents
kubectl delete -f test/fixtures/agents/ --ignore-not-found
```

---

## Notes

- `incidentRetention: 5m` in both agent fixtures makes the retention scenario complete quickly. Change to a longer value if you need more observation time.
- In-memory watcher state (pending-alerted, healthy-alerted sets) resets on operator restart. A re-deployed operator re-fires suppressed events once via the bootstrap scan.
- See [docs/reference/watcher.md](../../docs/reference/watcher.md) for the full event catalog and signal trigger conditions.
