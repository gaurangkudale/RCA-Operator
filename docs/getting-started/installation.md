# Installation

Two supported installation paths: Helm (recommended for production) and raw kubectl manifests.

---

## Option 1 — Helm *(recommended)*

```bash
# Add the RCA Operator Helm repository
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io/charts

# Update your local Helm repositories
helm repo update

# Install the operator
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --create-namespace
```

This installs CRDs, the operator deployment, RBAC, and 4 default `RCACorrelationRule` resources.

## Option 2 — kubectl

```bash
# Install CRDs and operator (all-in-one) - use the latest release
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/latest/download/install.yaml

# Or install CRDs separately (optional)
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/latest/download/crds.yaml
```

> **Note**: Check [releases](https://github.com/gaurangkudale/RCA-Operator/releases) for all available versions. To pin a specific version, replace `latest` with a version tag (e.g. `v0.0.5`).

When using kubectl, apply the default correlation rules manually:

```bash
kubectl apply -f config/rules/
```

---

## Verify the Installation

```bash
# Operator pod should be Running
kubectl get pods -n rca-system

# CRDs should be registered
kubectl get crd rcaagents.rca.rca-operator.tech
kubectl get crd incidentreports.rca.rca-operator.tech
kubectl get crd rcacorrelationrules.rca.rca-operator.tech

# Default correlation rules should be loaded
kubectl get rcacorrelationrules
```

---

Next: [Quick Start](quickstart.md)
