# CLI Flags Reference

All flags accepted by the `rca-operator` binary. These are used directly when running with `make run` outside a cluster, or mapped to Helm values when deploying via chart.

```bash
./bin/manager [flags]
# or
make run ARGS="--telemetry-backend=signoz --signoz-endpoint=http://localhost:8080"
```

---

## Core

| Flag | Default | Description |
|---|---|---|
| `--dashboard-bind-address` | `:9090` | Address the incident dashboard HTTP server binds to |
| `--metrics-bind-address` | `0` | Address the Prometheus metrics endpoint binds to. Use `:8443` for HTTPS or `:8080` for HTTP. `0` disables the metrics service. |
| `--health-probe-bind-address` | `:8081` | Address the `/healthz` and `/readyz` probe endpoints bind to |
| `--leader-elect` | `true` | Enable leader election. Required when running multiple replicas. |
| `--leader-election-namespace` | `""` | Namespace for the leader election lease. Required when running outside a cluster (`make run`). When empty, the in-cluster namespace is used. |
| `--metrics-secure` | `true` | Serve the metrics endpoint over HTTPS. Set to `false` for plain HTTP. |
| `--enable-http2` | `false` | Enable HTTP/2 for metrics and webhook servers. Disabled by default to avoid HTTP/2 stream cancellation CVEs. |

---

## Webhooks

| Flag | Default | Description |
|---|---|---|
| `--enable-webhooks` | `false` | Enable admission webhooks for RCAAgent and RCACorrelationRule validation |
| `--webhook-cert-path` | `""` | Directory containing the webhook TLS certificate |
| `--webhook-cert-name` | `tls.crt` | Webhook certificate file name |
| `--webhook-cert-key` | `tls.key` | Webhook key file name |
| `--metrics-cert-path` | `""` | Directory containing the metrics server TLS certificate |
| `--metrics-cert-name` | `tls.crt` | Metrics certificate file name |
| `--metrics-cert-key` | `tls.key` | Metrics key file name |

---

## Auto-Detection

| Flag | Default | Description |
|---|---|---|
| `--enable-autodetect` | `false` | Enable automatic correlation rule detection from buffer patterns |
| `--autodetect-min-occurrences` | `5` | Minimum pattern occurrences before auto-creating a rule |
| `--autodetect-min-timespan` | `10m` | Minimum time span between first and last observation before auto-creating a rule |
| `--autodetect-max-rules` | `20` | Maximum number of auto-generated correlation rules |
| `--autodetect-interval` | `60s` | How often to analyze the buffer for patterns |
| `--autodetect-expiry` | `1h` | Duration without observation before an auto-generated rule expires |

---

## Telemetry Backend (Phase 2)

| Flag | Default | Description |
|---|---|---|
| `--telemetry-backend` | `""` | Backend type: `signoz`, `jaeger`, `composite`, or `full-composite`. Empty disables telemetry queries. |
| `--signoz-endpoint` | `""` | SigNoz query service URL (e.g. `http://signoz-query-service:8080`). Used when backend is `signoz` or `full-composite`. |
| `--jaeger-endpoint` | `""` | Jaeger query HTTP API URL (e.g. `http://jaeger-query:16686`). Used when backend is `jaeger`, `composite`, or `full-composite`. |
| `--prometheus-endpoint` | `""` | Prometheus HTTP API URL (e.g. `http://prometheus:9090`). Used when backend is `composite` or `full-composite`. |
| `--topology-refresh-interval` | `30s` | How often to refresh the service dependency graph cache |
| `--topology-dependency-window` | `15m` | Lookback window when querying service dependencies |

---

## AI Investigation (Phase 2)

| Flag | Default | Description |
|---|---|---|
| `--ai-endpoint` | `""` | OpenAI-compatible API endpoint (e.g. `https://api.openai.com/v1`). Empty disables AI investigation. Works with OpenAI, Azure OpenAI, Ollama, LiteLLM proxy. |
| `--ai-model` | `gpt-4o` | LLM model name for AI investigation |
| `--ai-secret-ref` | `""` | Name of Kubernetes Secret containing the AI API key (must have key `apiKey`) |
| `--ai-auto-investigate` | `false` | Automatically trigger AI investigation when incidents become Active |

---

## Logging

Logging flags are provided by the `sigs.k8s.io/controller-runtime/pkg/log/zap` package:

| Flag | Description |
|---|---|
| `--zap-devel` | Enable development mode (unstructured, color output) |
| `--zap-encoder` | Log encoding: `json` or `console` |
| `--zap-log-level` | Log level: `debug`, `info`, `error` |
| `--zap-stacktrace-level` | Level at which stack traces are added: `info`, `error`, `panic` |
| `--zap-time-encoding` | Time encoding format: `epoch`, `millis`, `nano`, `iso8601`, `rfc3339`, `rfc3339nano` |

---

## Helm Value Mapping

| CLI Flag | Helm Value |
|---|---|
| `--dashboard-bind-address` | `manager.args` (inline) |
| `--leader-elect` | `leaderElection.enabled` |
| `--leader-election-namespace` | `leaderElection.namespace` |
| `--telemetry-backend` | `telemetry.backend` |
| `--signoz-endpoint` | `telemetry.signoz.endpoint` |
| `--jaeger-endpoint` | `telemetry.jaeger.endpoint` |
| `--prometheus-endpoint` | `telemetry.prometheus.endpoint` |
| `--topology-refresh-interval` | `topology.refreshInterval` |
| `--topology-dependency-window` | `topology.dependencyWindow` |
| `--ai-endpoint` | `ai.endpoint` |
| `--ai-model` | `ai.model` |
| `--ai-secret-ref` | `ai.secretRef` |
| `--ai-auto-investigate` | `ai.autoInvestigate` |
| `--enable-autodetect` | `autoDetect.enabled` |
| `--enable-webhooks` | `webhooks.enabled` |

## Related

- [Helm Values reference](helm-values.md)
- [RCAAgent CRD reference](rcaagent-crd.md)
