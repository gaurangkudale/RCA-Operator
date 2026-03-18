# Installation

Two supported installation paths: Helm (recommended for production) and raw kubectl manifests.

---

## Option 1 — Helm *(recommended)*

```bash
# Add the RCA Helm repository
helm repo add rca https://charts.rca-operator.tech
helm repo update

# Install into its own namespace
helm install rca rca/rca-operator \
  --namespace rca-system \
  --create-namespace
```

See [Helm chart values](../../helm/values.yaml) for the full set of configurable parameters.

---

## Option 2 — kubectl

```bash
# Install CRDs and operator (all-in-one)
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/latest/download/install.yaml

# Or install CRDs separately (optional)
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/latest/download/crds.yaml
```

---

## Verify the Installation

```bash
# Operator pod should be Running
kubectl get pods -n rca-system

# CRDs should be registered
kubectl get crd rcaagents.rca.rca-operator.tech
kubectl get crd incidentreports.rca.rca-operator.tech
```

---

Next: [Quick Start](quickstart.md)
