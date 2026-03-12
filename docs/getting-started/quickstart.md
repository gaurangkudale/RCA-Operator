# Quick Start

Get your first `RCAAgent` running in under five minutes.

---

## 1. Create the required Secret

The operator validates that this Secret exists before marking the agent `Available=True`.

```bash
kubectl create secret generic rca-agent-openai-secret \
  --from-literal=apiKey=<YOUR_OPENAI_KEY> \
  -n default
```

## 2. Apply the minimal RCAAgent

```yaml
# rca-agent.yaml
apiVersion: rca.rca-operator.io/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
    - staging
  aiProviderConfig:
    type: openai
    model: gpt-4o
    secretRef: rca-agent-openai-secret
  incidentRetention: 30d
```

```bash
kubectl apply -f rca-agent.yaml
```

## 3. Verify the agent is ready

```bash
# STATUS column should show True
kubectl get rcaagent -n default

# Full status detail
kubectl describe rcaagent sre-agent -n default
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

## Add Notifications (optional)

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
