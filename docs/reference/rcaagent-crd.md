# RCAAgent CRD Reference

`RCAAgent` is the primary configuration resource. One agent can watch multiple namespaces. The operator validates Secret references and marks `Available=True` when the agent is fully operational.

```bash
kubectl get rcaagent -A
kubectl describe rcaagent <name> -n <namespace>
```

---

## Minimal Example

```yaml
apiVersion: rca.rca-operator.io/v1alpha1
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

## Autonomy Levels *(Phase 2+)*

Controls how much the operator acts on its own. Configurable per namespace.

| Level | Mode | Behaviour |
|---|---|---|
| `0` | **Observe** | Monitors and logs only — no alerts, no action |
| `1` | **Suggest** | Sends RCA + recommended fix to notification channels. Human approves all actions |
| `2` | **Semi-Auto** | Auto-fixes safe actions (restart pod, scale up). Alerts human for risky actions (rollback, delete) |
| `3` | **Full-Auto** | Executes all remediations autonomously. Sends post-incident report afterward |

```yaml
spec:
  namespaceAutonomy:         # Phase 2+ field
    production: 1            # suggest-only in prod
    staging: 2               # semi-auto in staging
    dev: 3                   # fully autonomous in dev
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
- [Quick Start](../getting-started/quickstart.md)
