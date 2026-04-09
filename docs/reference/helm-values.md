# Helm Values Reference

All configurable values for the `rca-operator` Helm chart. Values can be set via `--set` or in a custom `values.yaml`.

```bash
helm install rca-operator oci://ghcr.io/gaurangkudale/helm-charts/rca-operator \
  --namespace rca-system \
  --create-namespace \
  -f my-values.yaml
```

---

## Image

| Value | Type | Default | Description |
|---|---|---|---|
| `image.repository` | string | `ghcr.io/gaurangkudale/rca-operator` | Container image repository |
| `image.tag` | string | chart appVersion | Container image tag |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `imagePullSecrets` | list | `[]` | Image pull secrets |
| `replicaCount` | int | `1` | Number of controller manager replicas |

---

## CRDs

| Value | Type | Default | Description |
|---|---|---|---|
| `crds.install` | bool | `true` | Install CRDs with the chart |
| `crds.keep` | bool | `false` | Keep CRDs when the chart is uninstalled (prevents deletion of custom resources) |

---

## Service Account

| Value | Type | Default | Description |
|---|---|---|---|
| `serviceAccount.create` | bool | `true` | Create a service account |
| `serviceAccount.automount` | bool | `true` | Automount service account token |
| `serviceAccount.annotations` | object | `{}` | Annotations to add to the service account |
| `serviceAccount.name` | string | `""` | Override the service account name (auto-generated when empty) |

---

## Pod Configuration

| Value | Type | Default | Description |
|---|---|---|---|
| `podAnnotations` | object | `{}` | Annotations for the operator pod |
| `podLabels` | object | `{}` | Labels for the operator pod |
| `podSecurityContext.runAsNonRoot` | bool | `true` | Run as non-root |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` | Seccomp profile type |
| `securityContext.readOnlyRootFilesystem` | bool | `true` | Read-only root filesystem |
| `securityContext.allowPrivilegeEscalation` | bool | `false` | Disallow privilege escalation |
| `securityContext.capabilities.drop` | list | `["ALL"]` | Linux capabilities to drop |

---

## Resources

| Value | Type | Default | Description |
|---|---|---|---|
| `resources.limits.cpu` | string | `500m` | CPU limit |
| `resources.limits.memory` | string | `512Mi` | Memory limit |
| `resources.requests.cpu` | string | `100m` | CPU request |
| `resources.requests.memory` | string | `128Mi` | Memory request |

---

## Scheduling

| Value | Type | Default | Description |
|---|---|---|---|
| `nodeSelector` | object | `{}` | Node selector for scheduling |
| `tolerations` | list | `[]` | Tolerations for scheduling |
| `affinity` | object | `{}` | Affinity rules for scheduling |

---

## Leader Election

| Value | Type | Default | Description |
|---|---|---|---|
| `leaderElection.enabled` | bool | `true` | Enable leader election (required for multi-replica) |
| `leaderElection.namespace` | string | `""` | Namespace for the leader election lease. When empty, uses the in-cluster namespace. |

---

## Probes

| Value | Type | Default | Description |
|---|---|---|---|
| `livenessProbe.httpGet.path` | string | `/healthz` | Liveness probe path |
| `livenessProbe.httpGet.port` | int | `8081` | Liveness probe port |
| `livenessProbe.initialDelaySeconds` | int | `15` | Liveness initial delay |
| `livenessProbe.periodSeconds` | int | `20` | Liveness probe period |
| `readinessProbe.httpGet.path` | string | `/readyz` | Readiness probe path |
| `readinessProbe.httpGet.port` | int | `8081` | Readiness probe port |
| `readinessProbe.initialDelaySeconds` | int | `5` | Readiness initial delay |
| `readinessProbe.periodSeconds` | int | `10` | Readiness probe period |

---

## Metrics

| Value | Type | Default | Description |
|---|---|---|---|
| `metrics.enabled` | bool | `true` | Enable Prometheus metrics endpoint |
| `metrics.service.port` | int | `8443` | Metrics service port |
| `metrics.service.type` | string | `ClusterIP` | Metrics service type |

---

## Dashboard

| Value | Type | Default | Description |
|---|---|---|---|
| `dashboard.enabled` | bool | `true` | Enable the dashboard HTTP server |
| `dashboard.port` | int | `9090` | Dashboard port |
| `dashboard.service.type` | string | `ClusterIP` | Dashboard service type |
| `dashboard.service.port` | int | `9090` | Dashboard service port |
| `dashboard.service.targetPort` | int | `9090` | Dashboard service target port |
| `dashboard.ingress.enabled` | bool | `false` | Enable ingress for dashboard access |
| `dashboard.ingress.className` | string | `""` | Ingress class name |
| `dashboard.ingress.annotations` | object | `{}` | Ingress annotations |
| `dashboard.ingress.hosts` | list | `[{host: rca-operator.local, paths: [{path: /, pathType: Prefix}]}]` | Ingress hosts |
| `dashboard.ingress.tls` | list | `[]` | Ingress TLS configuration |

---

## OpenTelemetry (Self-Instrumentation)

The operator emits its own traces and metrics via OTLP gRPC. This is independent of the telemetry backend used to query external systems.

| Value | Type | Default | Description |
|---|---|---|---|
| `otel.enabled` | bool | `false` | Enable OTel self-instrumentation |
| `otel.endpoint` | string | `""` | OTLP gRPC collector address (e.g. `signoz-otel-collector.platform:4317`). No `http://` prefix. |
| `otel.serviceName` | string | `rca-operator` | `service.name` resource attribute |
| `otel.samplingRate` | string | `"1.0"` | Trace sampling ratio |
| `otel.insecure` | bool | `true` | Disable TLS (typical for in-cluster collectors) |

---

## Telemetry Backend (Phase 2)

Configures connections to external observability backends for cross-signal correlation, topology visualization, and the Metrics/Logs/Traces dashboard tabs.

| Value | Type | Default | Description |
|---|---|---|---|
| `telemetry.enabled` | bool | `false` | Enable telemetry backend integration |
| `telemetry.backend` | string | `""` | Backend type: `signoz`, `jaeger`, `composite`, or `full-composite`. Empty disables telemetry queries. |
| `telemetry.signoz.endpoint` | string | `""` | SigNoz query service URL (e.g. `http://signoz-query-service:8080`) |
| `telemetry.jaeger.endpoint` | string | `""` | Jaeger query HTTP API URL (e.g. `http://jaeger-query:16686`) |
| `telemetry.prometheus.endpoint` | string | `""` | Prometheus HTTP API URL (e.g. `http://prometheus:9090`) |

**Backend selection guide:**

| Scenario | Backend |
|---|---|
| Running SigNoz (unified stack) | `signoz` |
| Running Jaeger only | `jaeger` |
| Running Jaeger + Prometheus | `composite` |
| Running Jaeger + Prometheus + SigNoz | `full-composite` |

---

## Topology (Phase 2)

| Value | Type | Default | Description |
|---|---|---|---|
| `topology.refreshInterval` | string | `30s` | How often to refresh the service dependency graph cache |
| `topology.dependencyWindow` | string | `15m` | Lookback window when querying service dependencies |

---

## AI Investigation (Phase 2)

| Value | Type | Default | Description |
|---|---|---|---|
| `ai.enabled` | bool | `false` | Enable AI-powered root cause analysis |
| `ai.endpoint` | string | `""` | OpenAI-compatible API endpoint (e.g. `https://api.openai.com/v1`) |
| `ai.model` | string | `gpt-4o` | LLM model name |
| `ai.secretRef` | string | `""` | Kubernetes Secret name containing `apiKey` |
| `ai.autoInvestigate` | bool | `false` | Auto-trigger investigation when incidents become Active |

---

## Default Rules

| Value | Type | Default | Description |
|---|---|---|---|
| `defaultRules.enabled` | bool | `false` | Install built-in `RCACorrelationRule` CRs on chart install |

---

## Auto-Detection

| Value | Type | Default | Description |
|---|---|---|---|
| `autoDetect.enabled` | bool | `true` | Enable automatic correlation rule detection |
| `autoDetect.minOccurrences` | int | `5` | Minimum pattern occurrences before auto-creating a rule |
| `autoDetect.maxRules` | int | `20` | Maximum number of auto-generated rules |
| `autoDetect.interval` | string | `60s` | How often to analyze the buffer for patterns |
| `autoDetect.expiry` | string | `1h` | Duration without observation before an auto-generated rule expires |

---

## Webhooks

| Value | Type | Default | Description |
|---|---|---|---|
| `webhooks.enabled` | bool | `false` | Enable admission webhooks for CRD validation |

---

## Pod Disruption Budget

| Value | Type | Default | Description |
|---|---|---|---|
| `podDisruptionBudget.enabled` | bool | `true` | Enable PodDisruptionBudget |
| `podDisruptionBudget.minAvailable` | int | `1` | Minimum available pods during disruptions |

---

## Example: SigNoz with AI

```yaml
telemetry:
  enabled: true
  backend: signoz
  signoz:
    endpoint: "http://signoz-query-service.platform:8080"

otel:
  enabled: true
  endpoint: "signoz-otel-collector.platform:4317"
  insecure: true

ai:
  enabled: true
  endpoint: "https://api.openai.com/v1"
  model: "gpt-4o"
  secretRef: "openai-api-key"
  autoInvestigate: true
```

## Example: Jaeger + Prometheus (Composite)

```yaml
telemetry:
  enabled: true
  backend: composite
  jaeger:
    endpoint: "http://jaeger-query.tracing:16686"
  prometheus:
    endpoint: "http://prometheus-server.monitoring:9090"

topology:
  refreshInterval: 30s
  dependencyWindow: 15m
```

## Related

- [RCAAgent CRD reference](rcaagent-crd.md)
- [CLI Flags reference](cli-flags.md)
- [SigNoz integration](../integrations/signoz.md)
- [Jaeger integration](../integrations/jaeger.md)
- [Prometheus integration](../integrations/prometheus.md)
