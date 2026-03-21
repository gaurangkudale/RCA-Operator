# Installation

Two supported installation paths: Helm (recommended for production) and raw kubectl manifests.

---

## Option 1 — Helm *(recommended)*

### Method A: Install from Helm repository

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

### Method B: Install directly from GitHub release

```bash
# Install a specific version directly from GitHub releases
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system \
  --create-namespace
```

> **Note**: Check [releases](https://github.com/gaurangkudale/RCA-Operator/releases) for the latest Helm chart version (tags starting with `helm-v*`).

### Customizing the installation

```bash
# With Method A (Helm repository):
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --create-namespace \
  --set replicaCount=2 \
  --set resources.limits.memory=256Mi

# With Method B (direct URL):
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system \
  --create-namespace \
  --set replicaCount=2 \
  --set resources.limits.memory=256Mi
```

See [Helm chart values](../../helm/values.yaml) for the full set of configurable parameters.

---

## Option 2 — kubectl

```bash
# Install CRDs and operator (all-in-one) - use specific version
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml

# Or install CRDs separately (optional)
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/crds.yaml
```

> **Note**: Check [releases](https://github.com/gaurangkudale/RCA-Operator/releases) for the latest operator version (tags starting with `v*`). Replace `v0.1.4` with the latest version tag.

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

---

Next: [Quick Start](quickstart.md)
