# Installation

Two supported installation paths: Helm (recommended for production) and raw kubectl manifests.

---

## Option 1 — Helm *(recommended)*

```bash
# Add the RCA Operator Helm repository
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io

# Update your local Helm repositories
helm repo update

# Install the operator
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --create-namespace
```

## Option 2 — kubectl

```bash
# Install CRDs and operator (all-in-one) - use specific version
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.0.4/install.yaml

# Or install CRDs separately (optional)
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.0.4/crds.yaml
```

> **Note**: Check [releases](https://github.com/gaurangkudale/RCA-Operator/releases) for the latest operator version (tags starting with `v*`). Replace `v0.0.4` with the latest version tag.

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
