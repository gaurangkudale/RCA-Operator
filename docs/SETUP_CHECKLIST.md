# Helm Chart Publishing Setup Checklist

Use this checklist to complete the Helm chart publishing setup.

## ✅ Completed (Already Done)

- [x] Fixed kubectl installation URLs
- [x] Fixed Helm direct installation URLs
- [x] Standardized namespace to `rca-system`
- [x] Created workflow to publish to GitHub Pages
- [x] Updated all documentation
- [x] Tested all installation URLs (200 OK)

## 🔧 To Do (Setup Helm Repository)

### Step 1: Create Personal Access Token (5 minutes)

- [ ] Go to https://github.com/settings/tokens
- [ ] Click "Generate new token" → "Generate new token (classic)"
- [ ] Configure:
  - **Note**: `RCA Operator Helm Chart Publishing`
  - **Expiration**: 90 days (or your preference)
  - **Scopes**: Check `repo` (Full control of private repositories)
- [ ] Click "Generate token"
- [ ] **COPY THE TOKEN** (you won't see it again!)

### Step 2: Add Secret to Repository (2 minutes)

- [ ] Go to https://github.com/gaurangkudale/RCA-Operator/settings/secrets/actions
- [ ] Click "New repository secret"
- [ ] Configure:
  - **Name**: `PAGES_TOKEN`
  - **Value**: Paste the token from Step 1
- [ ] Click "Add secret"

### Step 3: Push Changes (1 minute)

```bash
cd /Users/gaurangkudale/gk-github/RCA-Operator
git status
git add .
git commit -m "fix: update installation docs and workflows for external Pages repo"
git push origin fix/installation
```

- [ ] Changes committed and pushed

### Step 4: Test the Workflow (5 minutes)

**Option A: Manual trigger**
```bash
gh workflow run helm-gh-pages.yml
```

**Option B: Create a test tag**
```bash
git tag helm-v0.1.2-test
git push origin helm-v0.1.2-test
```

- [ ] Workflow triggered
- [ ] Check Actions tab: https://github.com/gaurangkudale/RCA-Operator/actions
- [ ] Workflow completed successfully (green checkmark)

### Step 5: Verify Helm Repository (2 minutes)

```bash
# Check if index.yaml exists
curl -sSL https://gaurangkudale.github.io/rca-operator.github.io/index.yaml

# Add the Helm repository
helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
helm repo update

# Search for charts
helm search repo rca-operator --versions
```

- [ ] `index.yaml` accessible
- [ ] Helm repository added successfully
- [ ] Charts found in search results

### Step 6: Test Installation (Optional - 5 minutes)

```bash
# Test with kind cluster
kind create cluster --name test-rca

# Install from Helm repository
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --create-namespace

# Verify
kubectl get pods -n rca-system
kubectl get crd | grep rca-operator

# Cleanup
kind delete cluster --name test-rca
```

- [ ] Installation successful
- [ ] Operator pod running
- [ ] CRDs registered

## 📝 Notes

**If Step 4 fails with permission error:**
- Double-check that `PAGES_TOKEN` secret is correctly set
- Verify the PAT has `repo` scope
- Regenerate PAT if needed

**If Step 5 fails with 404:**
- Wait 1-2 minutes for GitHub Pages to update
- Check that the Pages repo received the commit
- Verify Pages settings: https://github.com/gaurangkudale/rca-operator.github.io/settings/pages

**Cleanup test tag:**
```bash
git tag -d helm-v0.1.2-test
git push origin :refs/tags/helm-v0.1.2-test
```

## 🎉 Success Criteria

You've successfully completed the setup when:

1. ✅ Workflow completes without errors
2. ✅ Charts appear in Pages repository: https://github.com/gaurangkudale/rca-operator.github.io
3. ✅ `helm repo add` command works
4. ✅ `helm search repo rca-operator` shows charts
5. ✅ Installation from Helm repository succeeds

## 📖 Reference Documents

- **Full Setup Guide**: docs/HELM_PAGES_SETUP.md
- **Fix Summary**: docs/INSTALLATION_FIX_SUMMARY.md
- **Quick Reference**: docs/INSTALLATION_FIXES_README.md

## ⏱️ Estimated Time

- **Total setup time**: ~15 minutes
- **Actual work time**: ~8 minutes
- **Waiting time**: ~5-7 minutes (workflow execution, Pages rebuild)
