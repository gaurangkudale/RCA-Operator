# Notifications

RCA Operator sends incident alerts to Slack and PagerDuty when an incident transitions from `Detecting` to `Active`.

## How It Works

1. The reconciler detects that an `IncidentReport` has transitioned to `Active` and `status.notified` is `false`.
2. The `Dispatcher` sends notifications to all configured channels.
3. On success, `status.notified` is set to `true` to suppress duplicate alerts.

Notifications are **one-shot per incident lifecycle**. If an incident reopens (transitions from `Resolved` back to `Active`), a new notification is sent.

---

## Slack

### Configuration

Add a `slack` block to `spec.notifications` in your `RCAAgent`:

```yaml
spec:
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
```

### Secret

Create the Slack incoming webhook secret in the same namespace as the `RCAAgent`:

```bash
kubectl create secret generic slack-webhook \
  --from-literal=webhookURL=https://hooks.slack.com/services/xxx/yyy/zzz \
  -n default
```

The secret must have a key named `webhookURL`.

### Message Format

Slack notifications use Block Kit formatting:

```
🔴 [P1] NodeFailure — node-01 not ready
Namespace: production
Summary: Node not ready reason=KubeletNotReady
First seen: 2026-04-01 10:00 UTC
```

P1 incidents with `mentionOnP1` configured prepend the mention: `@oncall 🔴 [P1] ...`

---

## PagerDuty

### Configuration

Add a `pagerduty` block to `spec.notifications` in your `RCAAgent`:

```yaml
spec:
  notifications:
    pagerduty:
      secretRef: pagerduty-key
      severity: P2   # Only page on P2 and above
```

### Secret

Create the PagerDuty Events API v2 key secret:

```bash
kubectl create secret generic pagerduty-key \
  --from-literal=apiKey=xxxxxxxxxxxxxxxxxxxxxxxx \
  -n default
```

The secret must have a key named `apiKey`. This is the **Events API v2** integration key (not the REST API key).

### Severity Threshold

The `severity` field sets the minimum severity for PagerDuty pages:

| Value | Pages for |
|---|---|
| `P1` | P1 only |
| `P2` | P1 and P2 (default) |
| `P3` | P1, P2, and P3 |
| `P4` | All incidents |

Incidents below the threshold are created as `IncidentReport` CRs but do not trigger a PagerDuty alert.

### Dedup Key

PagerDuty alerts use the `IncidentReport` name as the dedup key. If the incident resolves and PagerDuty is configured, a resolve event is automatically sent.

---

## Testing Notifications

### Test Slack

Trigger a test notification by creating an `IncidentReport` manually in a watched namespace:

```bash
# Port-forward the dashboard to check incident status
kubectl port-forward -n rca-system svc/rca-operator-dashboard 9090:9090 &

# Create a test pod that crash-loops
kubectl run test-crash --image=busybox --restart=Always -n production -- /bin/false
```

### Check Notification Status

```bash
kubectl get incidentreport -n production -o jsonpath='{.items[*].status.notified}'
```

### Check Operator Logs

```bash
kubectl logs -n rca-system deployment/rca-operator-controller-manager | grep -i "notification\|slack\|pagerduty"
```

---

## Notification Conditions

Notifications are sent when:

- Incident phase transitions from `Detecting` → `Active`
- `status.notified` is `false`
- At least one notification channel is configured on the `RCAAgent` that owns the incident

Notifications are **not** sent for:

- Incidents that are already `Active` when the operator restarts (already notified)
- Incidents below the configured PagerDuty severity threshold
- `Resolved` incidents (resolution events are only sent to PagerDuty for dedup tracking)

---

## Both Channels Together

```yaml
spec:
  notifications:
    slack:
      webhookSecretRef: slack-webhook
      channel: "#incidents"
      mentionOnP1: "@oncall"
    pagerduty:
      secretRef: pagerduty-key
      severity: P2
```

Both channels are independent. Slack receives all incidents; PagerDuty only receives those at or above the configured severity.

---

## Related

- [RCAAgent CRD reference](../reference/rcaagent-crd.md) — `spec.notifications`
- [IncidentReport CRD reference](../reference/incidentreport-crd.md) — `status.notified`
- [RBAC permissions](../reference/rbac.md) — Secret read permissions required
