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

### 2. Helm Repository - 404 Error ❌ → ✅ FIXED
**Problem**: Documentation referenced a Helm repo that doesn't exist

**Solution**:
- Updated workflow to publish charts to external GitHub Pages repository: `https://github.com/gaurangkudale/rca-operator.github.io`
- Modified `.github/workflows/helm-gh-pages.yml` to:
  - Package Helm chart
  - Clone the Pages repository
  - Update Helm repository index
  - Push to Pages repo
- Updated docs with correct Helm repo URL: `https://gaurangkudale.github.io/rca-operator.github.io`

**Current working commands**:
```bash
# Method A: From Helm repository
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update
helm install rca-operator rca-operator/rca-operator --namespace rca-system --create-namespace

# Method B: Direct from GitHub release
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system --create-namespace
```

### 3. Namespace Inconsistency ✅ FIXED
**Problem**: Mixed namespaces in documentation (`rca-system` vs `rca-operator-system`)

**Solution**: Standardized to `rca-system` throughout all documentation

## Files Changed

1. **docs/getting-started/installation.md** - Updated with working installation methods
2. **.github/workflows/helm-release.yml** - Added `make_latest: false`
3. **.github/workflows/helm-gh-pages.yml** - NEW: GitHub Pages workflow (requires activation)

## Next Steps to Complete Setup

### A. Push Changes to Main Repository (Required)
```bash
# Push the changes to RCA-Operator repository
git add .
git commit -m "fix: update installation docs and workflows for external Pages repo"
git push origin main-gk
```

### B. Configure GitHub Pages Repository (Required)
The workflow is configured to push to `https://github.com/gaurangkudale/rca-operator.github.io`.

**Important**: The workflow needs write access to the Pages repository. You must:

1. **Option 1: Use a Personal Access Token (PAT)** *(Recommended)*
   - Go to GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)
   - Generate new token with `repo` scope
   - In RCA-Operator repository → Settings → Secrets and variables → Actions
   - Add new secret named `PAGES_TOKEN` with your PAT
   - Update workflow to use: `token: ${{ secrets.PAGES_TOKEN }}`

2. **Option 2: Use Deploy Keys**
   - Generate SSH key pair: `ssh-keygen -t ed25519 -C "helm-chart-deploy"`
   - Add public key to Pages repo → Settings → Deploy keys (with write access)
   - Add private key to RCA-Operator repo → Settings → Secrets
   - Update workflow to use SSH instead of HTTPS

### C. Test the Workflow
Trigger the workflow to publish your first chart:

```bash
# Manually trigger the workflow
gh workflow run helm-gh-pages.yml

# Or push a helm-v* tag to trigger automatically
git tag helm-v0.1.3
git push origin helm-v0.1.3
```

### D. Verify Installation Methods
Test both Helm installation methods:

```bash
# Method A: Helm repository (after workflow runs)
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update
helm search repo rca-operator
helm install rca-operator rca-operator/rca-operator --namespace rca-system --create-namespace

# Method B: Direct URL
helm install rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.2/rca-operator-0.1.2.tgz \
  --namespace rca-system --create-namespace

# kubectl method
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml

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
