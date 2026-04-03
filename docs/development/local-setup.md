# Local Development Setup

---

## Prerequisites

See [getting-started/prerequisites.md](../getting-started/prerequisites.md) for the full tool list.

## Clone and Bootstrap

```bash
git clone https://github.com/gaurangkudale/rca-operator.git
cd rca-operator

# Install Go module dependencies
go mod tidy

# Start a local Kind cluster
kind create cluster --name rca-dev

# Install CRDs into the cluster
make install

# Apply default correlation rules
kubectl apply -f config/rules/
```

## Run the Operator Locally

```bash
# Runs with your current kubeconfig context (no Docker image needed)
make run
```

The operator will reconcile existing `RCAAgent` CRs immediately on startup and load `RCACorrelationRule` CRDs for multi-signal correlation. Controller logs go to stdout.

### Enable Auto-Detection (Optional)

To test automatic correlation rule detection, pass the flags via `ARGS`:

```bash
make run ARGS="--enable-autodetect --autodetect-min-occurrences=3 --autodetect-max-rules=10 --autodetect-interval=30s --autodetect-expiry=30m"
```

The operator will now periodically snapshot the correlation buffer and mine for recurring signal patterns. When patterns exceed the occurrence threshold, it will auto-create `RCACorrelationRule` CRDs labeled `rca.rca-operator.tech/auto-generated: "true"`.

Configuration flags:

| Flag | Default | Description |
|---|---|---|
| `--enable-autodetect` | `false` | Master toggle |
| `--autodetect-min-occurrences` | `5` | Min co-occurrences before creating a rule |
| `--autodetect-max-rules` | `20` | Hard cap on auto-generated rules |
| `--autodetect-interval` | `60s` | Analysis frequency |
| `--autodetect-expiry` | `1h` | Delete rules unseen for this long |

Watch auto-generated rules:

```bash
kubectl get rcacorrelationrules -l rca.rca-operator.tech/auto-generated=true -w
```

See [Auto-Detection](../features/auto-detection.md) for full details.

## Apply Sample Resources

```bash
# Minimal sample RCAAgent (config/samples/)
kubectl apply -f config/samples/rca_v1alpha1_rcaagent.yaml

# Or use the test fixtures for specific scenarios
kubectl apply -f test/fixtures/agents/
kubectl apply -f test/fixtures/pods/crashloop.yaml
```

The checked-in sample is intentionally minimal and does not require Slack or PagerDuty secrets. Use `test/fixtures/agents/` if you want notification-enabled examples.

## Watch Operator Logs

```bash
# When running with make run, logs go directly to your terminal.
# When deployed to a cluster:
kubectl logs -n rca-system deploy/rca-operator-controller-manager -c manager -f
```

## Rebuild After Type Changes

```bash
# After editing *_types.go or kubebuilder markers
make manifests   # regenerate CRDs / RBAC
make generate    # regenerate DeepCopy methods
make install     # apply updated CRDs to cluster
```

## Teardown

```bash
kind delete cluster --name rca-dev
```
