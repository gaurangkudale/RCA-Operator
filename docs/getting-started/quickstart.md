# Quick Start

Get your first `RCAAgent` running in under five minutes.

---

## 1. Apply the minimal RCAAgent

```yaml
# rca-agent.yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
    - staging
  incidentRetention: 30d
```

```bash
kubectl apply -f rca-agent.yaml
```

## 2. Verify the agent is ready

```bash
# STATUS column should show True
kubectl get rcaagent -n default

# Full status detail
kubectl describe rcaagent sre-agent -n default
```

## 3. Verify correlation rules are loaded

```bash
# The Helm chart installs 4 default rules
kubectl get rcacorrelationrules
```

## 4. Trigger a test incident

```bash
# Apply one of the pre-built fixture pods
kubectl apply -f test/fixtures/pods/crashloop.yaml

# Watch incidents appear
kubectl get incidentreports -n default -w
```

See [test/fixtures/README.md](../../test/fixtures/README.md) for all available test scenarios.

---

## 5. Add Notifications (optional)

```bash
# Slack
kubectl create secret generic slack-webhook \
  --from-literal=webhookURL=https://hooks.slack.com/... \
  -n default

# PagerDuty
kubectl create secret generic pd-api-key \
  --from-literal=apiKey=<PD_KEY> \
  -n default
```

Then add the `notifications` block to your `RCAAgent` spec — see the [RCAAgent CRD reference](../reference/rcaagent-crd.md) for all fields.

---

Next: [RCAAgent CRD Reference](../reference/rcaagent-crd.md)
