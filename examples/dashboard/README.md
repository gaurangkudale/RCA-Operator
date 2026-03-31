# RCA Operator Dashboard Examples

This directory contains example Helm values configurations for different dashboard deployment scenarios.

## Files

- `values-internal.yaml` - Internal corporate access with LoadBalancer
- `values-external.yaml` - External access with Ingress and HTTPS
- `values-development.yaml` - Development/testing with NodePort
- `values-minimal.yaml` - Minimal dashboard setup for quick testing

## Quick Start

```bash
# Choose an example and install
helm install rca-operator rca-operator/rca-operator \
  -f examples/dashboard/values-internal.yaml \
  --namespace rca-system \
  --create-namespace
```

## Usage Instructions

1. Copy the example that best fits your use case
2. Modify domain names, certificates, and other settings
3. Apply with Helm using `-f your-values.yaml`

For detailed explanations, see: [Dashboard Documentation](../../docs/features/DASHBOARD.md)
