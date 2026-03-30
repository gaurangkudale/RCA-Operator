# RCAAgent CRD Reference

`RCAAgent` is the primary configuration resource. One agent can watch multiple namespaces. The operator validates Secret references and marks `Available=True` when the agent is fully operational.

The new Phase 2 architecture fields in this document are intentionally backend-agnostic and Kubernetes-native: signal processing is modeled explicitly, decision policy is separate from analysis, and observability uses generic OTLP export instead of a SigNoz-specific API.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

---

## Minimal Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
  aiProviderConfig:
    type: openai
    model: gpt-4o
    secretRef: rca-agent-openai-secret  # Secret key: "apiKey"
  incidentRetention: 30d
```

## Advanced Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: sre-agent
  namespace: default
spec:
  watchNamespaces:
    - production
    - staging
  aiProviderConfig:
    type: openai
    model: gpt-4o
    secretRef: rca-agent-openai-secret
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
    pagerduty:
      secretRef: pagerduty-key
      severity: P2
  incidentRetention: 30d
  signalProcessing:
    dedupWindow: 2m
    correlationWindow: 5m
    meaningfulIncidentWindow: 5m
    enableOwnerEnrichment: true
  decision:
    defaultAutonomy: 1
    namespaceAutonomy:
      - namespace: staging
        level: 2
      - namespace: production
        level: 1
    requireHumanApprovalFor:
      - rollbackWorkload
      - drainNode
    allowPlaybooks:
      - restart-pod
      - scale-workload
  observability:
    otlp:
      endpoint: http://signoz-otel-collector.observability.svc.cluster.local:4317
      insecure: true
      resourceAttributes:
        service.name: rca-operator
        deployment.environment: production
```

---

## Full Field Reference

### spec.watchNamespaces

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `watchNamespaces` | `[]string` | Yes | `["default"]` | Namespaces to monitor for pod failures |

If a namespace does not exist at reconcile time the operator logs a warning and continues. The watcher receives events for that namespace once it is created.

---

### spec.aiProviderConfig

Required. Stored in Phase 1 but not yet used by the RCA engine (Phase 2+).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `type` | `string` | Yes | `openai` | LLM provider. Currently: `openai` |
| `model` | `string` | Yes | `gpt-4o` | Model identifier (e.g. `gpt-4o`, `gpt-4-turbo`) |
| `secretRef` | `string` | Yes | — | Name of a Secret in the same namespace with key `apiKey` |

---

### spec.notifications

Optional. Remove the entire block if you do not need alerting.

#### spec.notifications.slack

| Field | Type | Required | Description |
|---|---|---|---|
| `webhookSecretRef` | `string` | Yes | Name of a Secret with key `webhookURL` |
| `channel` | `string` | Yes | Slack channel (e.g. `#incidents`) |
| `mentionOnP1` | `string` | No | Slack handle to mention on P1 incidents (e.g. `@oncall`) |

#### spec.notifications.pagerduty

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `secretRef` | `string` | Yes | — | Name of a Secret with key `apiKey` |
| `severity` | `string` | No | `P2` | Minimum severity to page. One of: `P1`, `P2`, `P3`, `P4` |

---

### spec.incidentRetention

| Field | Type | Required | Default | Pattern |
|---|---|---|---|---|
| `incidentRetention` | `string` | No | `30d` | `^[1-9][0-9]*(m\|h\|d)$` |

How long to keep `Resolved` `IncidentReport` CRs before the operator prunes them. Supported suffixes: `m` (minutes), `h` (hours), `d` (days).

Examples: `5m`, `12h`, `30d`

---

### spec.signalProcessing *(Phase 2+ schema)*

Stored now so the CRD can model the target incident pipeline. These fields are not fully enforced by the runtime yet.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `dedupWindow` | `string` | No | `2m` | Suppress repeated equivalent raw signals before reprocessing |
| `correlationWindow` | `string` | No | `5m` | Window used to group related normalized signals into one incident candidate |
| `meaningfulIncidentWindow` | `string` | No | `5m` | Minimum continuous observation before a detecting incident is promoted as meaningful |
| `enableOwnerEnrichment` | `bool` | No | `true` | Enrichs pod-level signals with top-level workload ownership before correlation |

All duration-like fields use the pattern `^[1-9][0-9]*(s\|m\|h)$`.

---

### spec.decision *(Phase 2+ schema)*

Stored now so the CRD can model autonomy and safety policy separately from analysis and reporting.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `defaultAutonomy` | `int` | No | `1` | Baseline autonomy level when no namespace override matches |
| `namespaceAutonomy[].namespace` | `string` | No | — | Namespace override target |
| `namespaceAutonomy[].level` | `int` | No | — | Autonomy level for that namespace |
| `requireHumanApprovalFor` | `[]string` | No | — | Action categories that must never execute automatically |
| `allowPlaybooks` | `[]string` | No | — | Allow-list of playbook identifiers the operator may execute |

Autonomy levels:

| Level | Mode | Behaviour |
|---|---|---|
| `0` | **Observe** | Monitors and records only |
| `1` | **Suggest** | Reports findings and recommended actions; no auto-remediation |
| `2` | **Safe Auto** | Executes pre-approved safe playbooks, escalates risky actions |
| `3` | **Full Auto** | Executes all allowed remediations autonomously |

Supported action categories in `requireHumanApprovalFor`:

- `restartPod`
- `rollbackWorkload`
- `scaleWorkload`
- `cordonNode`
- `drainNode`

---

### spec.observability *(Phase 2+ schema)*

Stored now so the CRD can model a CNCF-friendly observability pipeline. The API is OTLP-based and backend-agnostic. SigNoz is a recommended deployment target, not a hardcoded dependency.

#### spec.observability.otlp

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | `string` | No | OTLP collector endpoint, for example `http://otel-collector.observability.svc.cluster.local:4317` |
| `headersSecretRef` | `string` | No | Secret in the same namespace containing OTLP headers such as `Authorization` |
| `insecure` | `bool` | No | Disables transport security for in-cluster development collectors |
| `resourceAttributes` | `map[string]string` | No | Static OpenTelemetry resource attributes added to exported telemetry |

---

## Status Conditions

The operator sets three standard conditions on `status.conditions`:

| Type | Meaning |
|---|---|
| `Available` | `True` when the agent is configured and the watcher is running |
| `Progressing` | `True` during initial setup (Phase 2+) |
| `Degraded` | `True` when a required Secret is missing or another error blocks operation |

```bash
# Check conditions
kubectl get rcaagent sre-agent -n default -o jsonpath='{.status.conditions}' | jq .
```

---

## kubectl Cheatsheet

```bash
# List all agents
kubectl get rcaagent -A

# Describe a specific agent (shows conditions, events)
kubectl describe rcaagent sre-agent -n default

# Edit live
kubectl edit rcaagent sre-agent -n default

# Delete (triggers watcher cleanup via finalizer)
kubectl delete rcaagent sre-agent -n default
```

---

## Related

- [IncidentReport CRD reference](incidentreport-crd.md)
- [Watcher event catalog](watcher.md)
- [RBAC permissions](rbac.md)
- [ADR-0001: Signal-first incident pipeline](../development/architecture-decisions/ADR-0001-signal-first-incident-pipeline.md)
- [Quick Start](../getting-started/quickstart.md)
