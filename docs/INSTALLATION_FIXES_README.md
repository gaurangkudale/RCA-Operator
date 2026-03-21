# Installation Fix Documentation

This directory contains documentation about fixing installation issues and setting up Helm chart publishing.

## Documents

### [INSTALLATION_FIX_SUMMARY.md](./INSTALLATION_FIX_SUMMARY.md)
**Summary of all installation issues and fixes**

- Lists all errors that were fixed
- Shows before/after comparisons
- Provides working installation commands
- Quick reference for troubleshooting

### [HELM_PAGES_SETUP.md](./HELM_PAGES_SETUP.md)
**Complete guide for setting up Helm chart publishing**

- Step-by-step setup instructions
- GitHub Pages configuration
- Personal Access Token (PAT) creation
- Workflow explanation
- Troubleshooting guide
- Best practices

## Quick Links

- [Installation Guide](./getting-started/installation.md) - User-facing installation documentation
- [GitHub Pages Repo](https://github.com/gaurangkudale/rca-operator.github.io) - External Pages repository
- [Helm Chart Source](../helm/) - Helm chart source files
- [Workflows](../.github/workflows/) - GitHub Actions workflows

## Quick Setup

For administrators setting up Helm chart publishing:

1. **Create PAT**: GitHub → Settings → Developer settings → Personal access tokens → Generate new token (classic) with `repo` scope
2. **Add Secret**: RCA-Operator repo → Settings → Secrets → New secret → Name: `PAGES_TOKEN`, Value: your PAT
3. **Test**: `gh workflow run helm-gh-pages.yml` or push a `helm-v*` tag
4. **Verify**: `helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io`

For full details, see [HELM_PAGES_SETUP.md](./HELM_PAGES_SETUP.md).

## Quick Installation (Users)

### Helm Method (Recommended)
```bash
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update
helm install rca-operator rca-operator/rca-operator --namespace rca-system --create-namespace
```

### kubectl Method
```bash
kubectl apply -f https://github.com/gaurangkudale/RCA-Operator/releases/download/v0.1.4/install.yaml
```

## Issues Fixed

✅ **kubectl 404 Error** - Fixed by using specific version tags instead of `/latest/`
✅ **Helm Repository 404** - Fixed by setting up external GitHub Pages repository
✅ **Namespace Inconsistency** - Standardized to `rca-system` across all docs

## Status

- **kubectl installation**: ✅ Working (tested)
- **Helm direct URL**: ✅ Working (tested)
- **Helm repository**: ⚠️ Requires PAT setup (see HELM_PAGES_SETUP.md)
