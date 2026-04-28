# Helm Chart Upgrade Guide

How to upgrade an installed RCA Operator Helm release safely, with attention to the parts Helm doesn't handle automatically.

> **Current chart version:** see `helm/Chart.yaml`. Releases are published at <https://github.com/gaurangkudale/RCA-Operator/releases>.

---

## Important: CRDs Need Special Handling

**`helm upgrade` does not update existing CRDs.** It will:

- ✅ Update Deployments, Services, ConfigMaps, RBAC, etc.
- ❌ **NOT** update or replace existing `CustomResourceDefinition` objects

If a chart version ships CRD schema changes (new fields, validation rules, enum values), you have to update the CRDs out-of-band before or alongside the `helm upgrade`. Otherwise the operator may try to write fields the cluster's CRD doesn't yet validate.

The chart ships CRD manifests as templates at `helm/templates/crd-*.yaml`. They are rendered on first install but **not** updated by Helm on subsequent upgrades.

---

## Upgrade Workflow

### 1. Backup current state

```bash
kubectl get rcaagents -A -o yaml > backup-rcaagents.yaml
kubectl get incidentreports -A -o yaml > backup-incidents.yaml
kubectl get rcacorrelationrules -o yaml > backup-rules.yaml
```

### 2. Inspect what's installed

```bash
helm list -n rca-system
kubectl get crd | grep rca-operator
```

Record the current chart version — you'll need it for rollback.

### 3. Pull the new chart and update CRDs first

```bash
helm repo update

# Render only the CRD templates from the new chart and apply them
helm template rca-operator rca-operator/rca-operator \
  --show-only templates/crd-rcaagents.yaml \
  --show-only templates/crd-incidentreports.yaml \
  --show-only templates/crd-rcacorrelationrules.yaml \
  | kubectl apply -f -
```

`kubectl apply` performs a structural diff so existing data is preserved; the schema is merged in. If you prefer to pin to a specific version:

```bash
helm template rca-operator rca-operator/rca-operator --version <X.Y.Z> \
  --show-only templates/crd-rcaagents.yaml \
  --show-only templates/crd-incidentreports.yaml \
  --show-only templates/crd-rcacorrelationrules.yaml \
  | kubectl apply -f -
```

### 4. Run the Helm upgrade

```bash
helm upgrade rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --reuse-values \
  --wait --timeout 10m
```

`--wait` is required — the OpenTelemetry post-install hooks expect the operator webhooks to be Ready before they apply.

### 5. Verify

```bash
# Operator pod is healthy
kubectl get pods -n rca-system

# Existing custom resources still validate against the new schema
kubectl get rcaagents -A
kubectl get incidentreports -A
kubectl get rcacorrelationrules

# Watch the operator logs for schema or reconcile errors
kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager -f
```

---

## Rollback

```bash
# Show release history
helm history rca-operator -n rca-system

# Roll back to a previous revision
helm rollback rca-operator <revision> -n rca-system
```

**Caveat:** `helm rollback` does **not** roll back CRDs. If the new version added CRD fields and you must roll back, restore the older CRDs explicitly from backup (e.g. `kubectl apply -f backup-crds.yaml`).

---

## Troubleshooting

### `helm upgrade` fails with `field is immutable`

You changed a Helm value that maps to an immutable Kubernetes field (most often Deployment selectors or Service spec). Resolution:

```bash
# Inspect the offending object
kubectl describe deployment -n rca-system rca-operator-controller-manager

# If the change is genuinely needed, uninstall and reinstall
helm uninstall rca-operator -n rca-system
helm install rca-operator rca-operator/rca-operator -n rca-system --create-namespace
```

(`helm uninstall` does **not** delete CRDs or your existing IncidentReport / RCAAgent CRs by default — they survive the reinstall.)

### Existing CRs fail validation after upgrade

A new chart version may add `Required` fields or stricter enums. If `kubectl get` complains about an existing CR:

```bash
kubectl get rcaagent <name> -n <ns> -o yaml > broken.yaml
# Edit broken.yaml to satisfy the new schema
kubectl apply -f broken.yaml
```

### Operator logs show "no matches for kind"

The operator pod started before the new CRDs were applied, or the CRDs were not updated. Update CRDs (step 3 above) and restart the deployment:

```bash
kubectl rollout restart deployment/rca-operator-controller-manager -n rca-system
```

---

## Best Practices

1. **Test in non-production first.** Upgrade a dev/staging cluster, run a representative workload, then upgrade production.
2. **Read the changelog** before upgrading: [CHANGELOG.md](../CHANGELOG.md). Look for entries under `### Changed` and `### Removed`.
3. **Pin chart versions in CI.** Use `helm upgrade --version <X.Y.Z>` rather than tracking the latest tag.
4. **Update CRDs first**, then upgrade the chart. Operators that talk to fields not yet validated by the CRD can produce confusing errors.
5. **Watch the operator logs** for the first few minutes after upgrade — reconcile errors, missing RBAC, or schema mismatches surface there first.

---

## Related

- [Installation Guide](getting-started/installation.md)
- [Helm Reference](helm-installation.md) — values, flags, troubleshooting
- [RCAAgent CRD Reference](reference/rcaagent-crd.md)
- [Changelog](../CHANGELOG.md)

If you get stuck, open an issue: <https://github.com/gaurangkudale/RCA-Operator/issues>.
