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

## AI Provider

You need one of:
- An **OpenAI API key** (`gpt-4o` or similar)
- An **Anthropic API key** (Phase 2+)
- A running **Ollama** instance (Phase 2+)

The key is stored in a Kubernetes `Secret` — it is never embedded in the CRD directly.

---

Next: [Installation](installation.md)
