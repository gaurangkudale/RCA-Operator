# Installation

Two supported installation paths: Helm (recommended for production) and raw kubectl manifests.

---

## Option 1 — Helm *(recommended)*

```bash
# Add the RCA Helm repository (hosted on GitHub Pages)
helm repo add rca-operator https://gaurangkudale.github.io/RCA-Operator/charts
helm repo update

# Install into its own namespace
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-operator-system \
  --create-namespace
```

### Customizing the installation

```bash
# Install with custom values
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-operator-system \
  --create-namespace \
  --set replicaCount=2 \
  --set resources.limits.memory=256Mi
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
kubectl get pods -n rca-operator-system

# CRDs should be registered
kubectl get crd rcaagents.rca.rca-operator.tech
kubectl get crd incidentreports.rca.rca-operator.tech
```

---

Next: [Quick Start](quickstart.md)
