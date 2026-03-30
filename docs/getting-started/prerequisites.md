# Prerequisites

Before installing RCA Operator, ensure your environment meets the following requirements.

---

## Cluster

| Requirement | Minimum version |
|---|---|
| Kubernetes | 1.26+ |
| `kubectl` | matching your cluster version |

## Local Tools

```bash
# Go 1.22+
go version

# kubebuilder (for development only)
curl -L -o kubebuilder "https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)"
chmod +x kubebuilder && sudo mv kubebuilder /usr/local/bin/

# Helm v3+ (recommended installation method)
helm version

# kind (for local development cluster)
go install sigs.k8s.io/kind@latest
```

## Optional Notification Secrets

If you enable notifications, create Kubernetes `Secret` objects for:

- Slack webhook URL
- PagerDuty Events API routing key

These secrets live in the same namespace as the `RCAAgent`.

---

Next: [Installation](installation.md)
