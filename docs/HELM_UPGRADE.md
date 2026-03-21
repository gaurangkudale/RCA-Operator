# Helm Chart Upgrade Guide

This guide explains how to upgrade the RCA Operator Helm chart, with special attention to CRD upgrades.

## Important: CRD Upgrade Limitation

**⚠️ Helm does not automatically upgrade CRDs.**

When you run `helm upgrade`, Helm will:
- ✅ Update deployments, services, configmaps
- ❌ **NOT** update or replace CRDs

This means if you have old CRDs (e.g., from a previous version with `.io` API group), they will remain until manually replaced.

## Quick Upgrade (Automated)

For users upgrading from versions with old `.io` API group CRDs:

```bash
# Download and run the upgrade script
curl -sSL https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main-gk/scripts/helm-upgrade-crds.sh | bash -s <release-name> <namespace>

# Example:
curl -sSL https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main-gk/scripts/helm-upgrade-crds.sh | bash -s rca-operator rca-system
```

The script will:
1. Backup existing resources
2. Delete old `.io` CRDs3. Install new `.tech` CRDs
4. Upgrade the Helm release
5. Restart the operator

## Manual Upgrade

### Step 1: Check Your Current Installation

```bash
# Check Helm release version
helm list -n rca-system

# Check installed CRDs
kubectl get crd | grep rca-operator
```

### Step 2: Identify CRD API Group

**If you see `.io` CRDs (old):**
```
rcaagents.rca.rca-operator.io
incidentreports.rca.rca-operator.io
```

**You need to upgrade to `.tech` CRDs (new):**
```
rcaagents.rca.rca-operator.tech
incidentreports.rca.rca-operator.tech
```

### Step 3: Backup Existing Resources (Optional but Recommended)

```bash
# Backup RCAAgents
kubectl get rcaagents.rca.rca-operator.io -A -o yaml > rcaagents-backup.yaml

# Backup IncidentReports
kubectl get incidentreports.rca.rca-operator.io -A -o yaml > incidentreports-backup.yaml
```

### Step 4: Delete Old CRDs

```bash
# Delete old .io CRDs
kubectl delete crd rcaagents.rca.rca-operator.io
kubectl delete crd incidentreports.rca.rca-operator.io
```

**Note**: Deleting CRDs will delete all custom resources of that type! That's why we backed them up in Step 3.

### Step 5: Install New CRDs

**Option A: From source repository (if you have it cloned):**
```bash
kubectl apply -f helm/crds/
```

**Option B: From GitHub directly:**
```bash
kubectl apply -f https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main-gk/helm/crds/rca.rca-operator.tech_rcaagents.yaml
kubectl apply -f https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main-gk/helm/crds/rca.rca-operator.tech_incidentreports.yaml
```

**Option C: From Helm chart package:**
```bash
# Download the chart
helm pull https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.3/rca-operator-0.1.3.tgz

# Extract CRDs
tar -xzf rca-operator-0.1.3.tgz
kubectl apply -f rca-operator/crds/
```

### Step 6: Upgrade Helm Release

```bash
# Method A: From Helm repository
helm repo update
helm upgrade rca-operator rca-operator/rca-operator -n rca-system

# Method B: From GitHub release
helm upgrade rca-operator \
  https://github.com/gaurangkudale/RCA-Operator/releases/download/helm-v0.1.3/rca-operator-0.1.3.tgz \
  -n rca-system

# Method C: From local chart (if you have the repo cloned)
helm upgrade rca-operator ./helm -n rca-system
```

### Step 7: Verify the Upgrade

```bash
# Check CRDs are correct (.tech API group)
kubectl get crd | grep rca-operator.tech

# Expected output:
# incidentreports.rca.rca-operator.tech
# rcaagents.rca.rca-operator.tech

# Check operator is running
kubectl get pods -n rca-system

# Check operator logs for errors
kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager
```

## Troubleshooting

### Error: "could not find the requested resource"

**Symptom:**
```
Failed to watch: the server could not find the requested resource (get rcaagents.rca.rca-operator.tech)
```

**Cause**: Old `.io` CRDs are still installed, but operator expects `.tech` CRDs.

**Fix**: Follow the manual upgrade steps to replace CRDs.

### Error: "CRD already exists"

**Symptom:**
```
Error: customresourcedefinitions.apiextensions.k8s.io "rcaagents.rca.rca-operator.io" already exists
```

**Cause**: You have both old and new CRDs installed.

**Fix**:
```bash
# Delete old CRDs
kubectl delete crd rcaagents.rca.rca-operator.io incidentreports.rca.rca-operator.io

# Verify only .tech CRDs remain
kubectl get crd | grep rca-operator
```

### Operator Pod CrashLooping After Upgrade

**Symptom**: Operator pod keeps restarting after upgrade.

**Diagnosis**:
```bash
# Check pod status
kubectl get pods -n rca-system

# Check logs
kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager
```

**Common causes**:
1. Wrong CRDs installed (check API group is `.tech`)
2. RBAC permissions outdated (shouldn't happen with Helm upgrade)
3. Configuration errors in values.yaml

**Fix**:
```bash
# Restart the deployment
kubectl rollout restart deployment -n rca-system rca-operator-controller-manager

# If still failing, delete and reinstall
helm uninstall rca-operator -n rca-system
helm install rca-operator rca-operator/rca-operator -n rca-system --create-namespace
```

## Version-Specific Upgrade Notes

### Upgrading to v0.1.3

- **CRD Change**: API group changed from `.io` to `.tech`
- **Action Required**: Must manually replace CRDs (Helm limitation)
- **Breaking Change**: Old RCAAgent and IncidentReport resources with `.io` API group will be deleted

### Upgrading from v0.1.2 to v0.1.3

```bash
# Use the automated script
./scripts/helm-upgrade-crds.sh rca-operator rca-system

# Or follow manual upgrade steps above
```

## Best Practices

1. **Always backup before upgrading**:
   ```bash
   kubectl get rcaagents -A -o yaml > backup-rcaagents.yaml
   kubectl get incidentreports -A -o yaml > backup-incidents.yaml
   ```

2. **Test in non-production first**:
   - Upgrade dev/staging environments first
   - Verify operator functionality
   - Then upgrade production

3. **Check release notes**:
   - Read [CHANGELOG](../CHANGELOG.md) before upgrading
   - Check for breaking changes
   - Review new features and bug fixes

4. **Monitor after upgrade**:
   ```bash
   # Watch operator logs
   kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager -f

   # Check for incident reports
   kubectl get incidentreports -A
   ```

5. **Keep charts in sync**:
   ```bash
   # Update Helm repo regularly
   helm repo update

   # Check for new versions
   helm search repo rca-operator --versions
   ```

## Rollback

If something goes wrong, you can rollback:

```bash
# List release history
helm history rca-operator -n rca-system

# Rollback to previous version
helm rollback rca-operator <revision> -n rca-system

# Example: rollback to revision 1
helm rollback rca-operator 1 -n rca-system
```

**Note**: Rollback **does not** rollback CRDs. If you upgraded CRDs, you may need to manually restore old CRDs from backup.

## Additional Resources

- [Installation Guide](docs/getting-started/installation.md)
- [Helm Chart Values](helm/values.yaml)
- [CRD Reference](docs/reference/rcaagent-crd.md)
- [Troubleshooting Guide](docs/troubleshooting.md)

## Support

If you encounter issues during upgrade:

1. Check operator logs: `kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager`
2. Verify CRDs: `kubectl get crd | grep rca-operator`
3. Open an issue: https://github.com/gaurangkudale/RCA-Operator/issues
