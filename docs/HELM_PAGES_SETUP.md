# Setting Up Helm Chart Publishing to GitHub Pages

This guide explains how to configure the workflow to automatically publish Helm charts to your external GitHub Pages repository.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ RCA-Operator Repository (Source)                            │
│ https://github.com/gaurangkudale/RCA-Operator               │
│                                                              │
│  • Helm chart source (helm/)                                │
│  • Workflow: .github/workflows/helm-gh-pages.yml            │
│  • Triggered on: v* tags or manual dispatch                 │
└────────────┬────────────────────────────────────────────────┘
             │
             │ 1. Package chart
             │ 2. Clone Pages repo
             │ 3. Update index
             │ 4. Push to Pages repo
             ▼
┌─────────────────────────────────────────────────────────────┐
│ GitHub Pages Repository (Target)                            │
│ https://github.com/gaurangkudale/rca-operator.github.io     │
│                                                              │
│  charts/                                                     │
│  ├── rca-operator-0.1.2.tgz                                 │
│  └── index.yaml                                             │
│  index.yaml (symlink for convenience)                       │
│                                                              │
│  Published at:                                              │
│  https://gaurangkudale.github.io/rca-operator.github.io     │
└─────────────────────────────────────────────────────────────┘
```

## Prerequisites

- Admin access to both repositories
- GitHub Actions enabled on RCA-Operator repository

## Setup Instructions

### Step 1: Create Personal Access Token (PAT)

The workflow needs write access to your GitHub Pages repository. Create a PAT:

1. Go to GitHub → **Settings** → **Developer settings** → **Personal access tokens** → **Tokens (classic)**
2. Click **Generate new token** → **Generate new token (classic)**
3. Configure the token:
   - **Note**: `RCA Operator Helm Chart Publishing`
   - **Expiration**: Choose appropriate duration (90 days, 1 year, or no expiration)
   - **Select scopes**: Check `repo` (Full control of private repositories)
4. Click **Generate token**
5. **IMPORTANT**: Copy the token immediately (you won't see it again!)

### Step 2: Add Secret to RCA-Operator Repository

Add the PAT as a secret in the source repository:

1. Go to **RCA-Operator** repository → **Settings** → **Secrets and variables** → **Actions**
2. Click **New repository secret**
3. Configure:
   - **Name**: `PAGES_TOKEN`
   - **Value**: Paste the PAT you copied
4. Click **Add secret**

### Step 3: Configure GitHub Pages Repository

Ensure your Pages repository is properly configured:

1. Go to **rca-operator.github.io** repository → **Settings** → **Pages**
2. Under **Source**, select:
   - **Source**: `Deploy from a branch`
   - **Branch**: `main` (or `master`) and `/ (root)`
3. Click **Save**

### Step 4: Test the Workflow

Now test that the workflow can publish charts:

#### Option A: Manual Trigger

```bash
# From your RCA-Operator repository
gh workflow run helm-gh-pages.yml
```

Then check the Actions tab to see if the workflow succeeded.

#### Option B: Tag Trigger

```bash
# From your RCA-Operator repository
git tag helm-v0.1.2
git push origin helm-v0.1.2
```

This will automatically trigger the workflow.

### Step 5: Verify the Helm Repository

After the workflow completes successfully:

1. **Check the Pages repository**:
   ```bash
   # Clone the Pages repository to inspect
   git clone https://github.com/gaurangkudale/rca-operator.github.io
   cd rca-operator.github.io
   ls -la charts/
   ```

   You should see:
   - `charts/rca-operator-0.1.2.tgz`
   - `charts/index.yaml`
   - `index.yaml` (in root)

2. **Test the Helm repository URL**:
   ```bash
   # Check if index.yaml is accessible
   curl -sSL https://gaurangkudale.github.io/rca-operator.github.io/index.yaml

   # Add the Helm repository
   helm repo add rca-operator https://gaurangkudale.github.io/rca-operator.github.io
   helm repo update

   # Search for the chart
   helm search repo rca-operator
   ```

   Expected output:
   ```
   NAME                           CHART VERSION   APP VERSION   DESCRIPTION
   rca-operator/rca-operator      0.1.2           0.0.15         RCA Operator Helm Chart
   ```

3. **Test installation**:
   ```bash
   helm install rca-operator rca-operator/rca-operator \
     --namespace rca-system \
     --create-namespace \
     --dry-run --debug
   ```

## Workflow Details

### Trigger Conditions

The workflow runs when:
1. A tag matching `v*` is pushed (e.g., `v0.0.5`, `v1.0.0`)
2. Manually triggered via GitHub Actions UI or `gh workflow run`

### What the Workflow Does

1. **Checkout main repository**: Gets the Helm chart source from `helm/` directory
2. **Get chart version**: Extracts version from `helm/Chart.yaml`
3. **Package chart**: Creates `.tgz` file using `helm package`
4. **Checkout Pages repository**: Clones the external Pages repository
5. **Update Helm repository**:
   - Copies new chart to `charts/` directory
   - Updates `index.yaml` with `helm repo index`
   - Creates symlink to `index.yaml` in root
6. **Commit and push**: Commits changes and pushes to Pages repository

### Chart Versioning

- Chart version is defined in `helm/Chart.yaml`
- Tag should match chart version: `helm-v{version}`
- Each new version is added to the repository (old versions remain accessible)

## Troubleshooting

### Error: "refusing to allow a Personal Access Token"

**Problem**: The PAT doesn't have sufficient permissions.

**Solution**: Regenerate the PAT with `repo` scope and update the `PAGES_TOKEN` secret.

### Error: "remote: Permission to gaurangkudale/rca-operator.github.io.git denied"

**Problem**: The PAT isn't configured or has expired.

**Solution**:
1. Check if `PAGES_TOKEN` secret exists in repository settings
2. Verify the PAT hasn't expired
3. Regenerate and update the secret if needed

### Error: "index.yaml not found" when adding Helm repo

**Problem**: GitHub Pages hasn't published the files yet or is configured incorrectly.

**Solution**:
1. Wait 1-2 minutes for GitHub Pages to rebuild
2. Check Pages settings: Settings → Pages → ensure it's enabled from `main` branch
3. Verify the commit was pushed to the Pages repository

### Chart doesn't appear in `helm search repo`

**Problem**: Helm cache is stale.

**Solution**:
```bash
helm repo update
helm search repo rca-operator --versions
```

## Best Practices

1. **Keep chart versions in sync**: Tag should match `helm/Chart.yaml` version
2. **Semantic versioning**: Use semver for chart versions (0.1.2, 0.2.0, 1.0.0)
3. **Test before tagging**: Always test chart locally before creating tag
4. **Keep PAT secure**: Never commit PAT to repository, only store in Secrets
5. **PAT rotation**: Rotate PAT periodically for security
6. **Monitor workflow**: Check Actions tab after pushing tags to ensure success

## Publishing a New Chart Version

Complete workflow for publishing a new chart:

```bash
# 1. Update chart files
cd helm/
# ... make your changes ...

# 2. Update version in Chart.yaml
# version: 0.1.3  # Bump version

# 3. Test locally
helm lint .
helm package . -d /tmp
helm template test /tmp/rca-operator-0.1.3.tgz

# 4. Commit changes
git add helm/
git commit -m "chore: bump Helm chart to v0.1.3"
git push origin main-gk

# 5. Create and push tag
git tag helm-v0.1.3
git push origin helm-v0.1.3

# 6. Wait for workflow to complete
gh run watch

# 7. Verify publication
helm repo update
helm search repo rca-operator --versions
```

## Security Considerations

- **PAT scope**: Use fine-grained PATs instead of classic tokens when possible
- **PAT expiration**: Set reasonable expiration (90 days recommended)
- **Repository access**: PAT should only have access to required repositories
- **Secret rotation**: Update PAT in secrets before expiration
- **Audit logs**: Monitor Actions audit logs for unauthorized runs

## Alternative: Using Deploy Keys

If you prefer SSH deploy keys instead of PAT:

1. Generate SSH key:
   ```bash
   ssh-keygen -t ed25519 -C "helm-chart-deploy" -f ~/.ssh/helm_deploy_key
   ```

2. Add public key to Pages repo:
   - Go to Pages repo → Settings → Deploy keys
   - Add key with **write access** enabled

3. Add private key to RCA-Operator repo:
   - Go to Settings → Secrets and variables → Actions
   - Add secret `DEPLOY_KEY` with private key content

4. Update workflow to use SSH:
   ```yaml
   - name: Checkout GitHub Pages repository
     uses: actions/checkout@v4
     with:
       repository: gaurangkudale/rca-operator.github.io
       ssh-key: ${{ secrets.DEPLOY_KEY }}
       path: gh-pages
   ```

## References

- [GitHub Actions documentation](https://docs.github.com/en/actions)
- [GitHub Pages documentation](https://docs.github.com/en/pages)
- [Helm repository documentation](https://helm.sh/docs/topics/chart_repository/)
- [Managing GitHub Actions secrets](https://docs.github.com/en/actions/security-guides/encrypted-secrets)
