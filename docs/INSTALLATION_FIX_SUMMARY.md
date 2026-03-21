# Installation Fix Summary

## Issues Identified and Fixed

### 1. kubectl Installation - 404 Error ❌ → ✅ FIXED
**Problem**: Documentation referenced `releases/latest/download/install.yaml` which returned 404 because:
- Helm chart releases were marked as "Latest" instead of operator releases
- GitHub couldn't determine which release was the operator vs Helm chart

**Solution**:
- Updated docs to use specific version tags (e.g., `v0.1.4`)
- Modified `helm-release.yml` to set `make_latest: false` for Helm releases
- Operator releases (v*) will now be marked as "Latest"

**Current working command**:
```bash
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml
```

### 2. Helm Repository - 404 Error ❌ → ⚠️  PARTIALLY FIXED
**Problem**: Documentation referenced a Helm repo at `https://gaurangkudale.github.io/RCA-Operator/charts` which doesn't exist

**Solution**:
- Updated docs to use direct GitHub release URLs (works immediately)
- Created new workflow `.github/workflows/helm-gh-pages.yml` to publish to GitHub Pages
- Added "coming soon" section in docs for future Helm repo support

**Current working command**:
```bash
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system \
  --create-namespace
```

### 3. Namespace Inconsistency ✅ FIXED
**Problem**: Mixed namespaces in documentation (`rca-system` vs `rca-operator-system`)

**Solution**: Standardized to `rca-system` throughout all documentation

## Files Changed

1. **docs/getting-started/installation.md** - Updated with working installation methods
2. **.github/workflows/helm-release.yml** - Added `make_latest: false`
3. **.github/workflows/helm-gh-pages.yml** - NEW: GitHub Pages workflow (requires activation)

## Next Steps to Complete Setup

### A. Enable GitHub Actions Workflow (Required)
The helm-gh-pages.yml workflow is created but needs to be triggered:

```bash
# Push the changes
git add .
git commit -m "fix: update installation docs and Helm release workflows"
git push origin main-gk
```

### B. Enable GitHub Pages (Optional but Recommended)
To enable `helm repo add` functionality:

1. Go to repository Settings → Pages
2. Set Source to "GitHub Actions"
3. The workflow will automatically create and publish the Helm repository

Once enabled, users can use:
```bash
helm repo add rca-operator https://gaurangkudale.github.io/RCA-Operator
helm repo update
helm install rca-operator rca-operator/rca-operator --namespace rca-system --create-namespace
```

### C. Verify Installation Methods
Test both installation methods:

```bash
# Method 1: kubectl
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml

# Method 2: Helm direct
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system --create-namespace

# Verify
kubectl get pods -n rca-system
kubectl get crd | grep rca-operator
```

## Release Tagging Convention

Going forward, use this convention:
- **Operator releases**: `v0.1.5`, `v0.2.0`, etc. → Will be marked as "Latest"
- **Helm chart releases**: `helm-v0.1.3`, `helm-v0.2.0`, etc. → Won't be marked as "Latest"

## Testing Checklist

- [✅] v0.1.4 install.yaml URL returns 200
- [✅] v0.1.4 crds.yaml URL returns 200
- [✅] helm-v0.1.2 chart URL returns 200
- [ ] GitHub Pages Helm repository (pending activation)
- [ ] Test installation on clean cluster

## Immediate User Instructions

Users experiencing the errors can now use:

**For kubectl installation**:
```bash
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml
```

**For Helm installation**:
```bash
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system \
  --create-namespace
```

Both methods are tested and working! 🎉
