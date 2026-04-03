# Automatic Correlation Rule Detection

RCA Operator can automatically detect recurring signal co-occurrence patterns and create `RCACorrelationRule` CRDs from them. This makes the rule engine self-improving: the longer it runs, the more patterns it learns.

## How It Works

1. Every 60 seconds (configurable), the auto-detector snapshots the correlation buffer
2. It mines for co-occurring event pairs grouped by scope (samePod, sameNode, sameNamespace)
3. Patterns are tracked in an in-memory accumulator across ticks
4. When a pattern exceeds the confidence and occurrence thresholds, an `RCACorrelationRule` CRD is created
5. Stale auto-generated rules are expired and deleted if the pattern is not observed within the expiry window

```text
Correlation Buffer (5-min sliding window)
         |
         |  Snapshot() every 60s
         v
+------------------------------------------+
|  Auto-Detector Goroutine                 |
|                                          |
|  1. MinePatterns(entries)                |
|     -> extract co-occurring event pairs  |
|     -> detect scope (samePod/Node/NS)    |
|                                          |
|  2. Accumulator.Record(pair)             |
|     -> track frequency, confidence, span |
|                                          |
|  3. Creator.EnsureRule(pattern)           |
|     -> create RCACorrelationRule CRD     |
|     -> labeled auto-generated: "true"    |
|     -> priority 10-50 (below user rules) |
|                                          |
|  4. Creator.ExpireStaleRules()            |
|     -> delete rules unseen for 1h        |
+------------------------------------------+
         |
         |  CRD created/updated/deleted
         v
RCACorrelationRuleReconciler (existing)
         |  LoadRules() triggered
         v
CRDRuleEngine (existing) -- auto-generated
rules participate in normal evaluation
```

## Enabling Auto-Detection

### CLI Flags

```bash
/manager --enable-autodetect \
  --autodetect-min-occurrences=5 \
  --autodetect-confidence=0.7 \
  --autodetect-max-rules=20 \
  --autodetect-interval=60s \
  --autodetect-expiry=1h
```

### Local Development (make run)

```bash
make run ARGS="--enable-autodetect --autodetect-min-occurrences=3 --autodetect-confidence=0.6 --autodetect-max-rules=10 --autodetect-interval=30s --autodetect-expiry=30m"
```

### Helm Values

```yaml
autoDetect:
  enabled: true
  minOccurrences: 5
  confidence: 0.7
  maxRules: 20
  interval: 60s
  expiry: 1h
```

## Configuration

| Parameter | Default | Description |
|---|---|---|
| `enabled` | `false` | Master toggle for auto-detection |
| `minOccurrences` | `5` | Minimum co-occurrence count before creating a rule |
| `confidence` | `0.7` | P(B\|A) conditional probability threshold (0.0-1.0) |
| `maxRules` | `20` | Maximum number of auto-generated rules |
| `interval` | `60s` | How often to analyze the buffer |
| `expiry` | `1h` | Delete auto-rules if the pattern is unseen for this duration |

## Auto-Generated Rule Example

When the detector observes `CrashLoopBackOff` and `OOMKilled` co-occurring on the same pod 5+ times with confidence >= 0.7:

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: auto-crashloopbackoff-oomkilled-samepod
  labels:
    rca.rca-operator.tech/auto-generated: "true"
  annotations:
    rca.rca-operator.tech/pattern-key: "CrashLoopBackOff:OOMKilled:samePod"
    rca.rca-operator.tech/confidence: "0.85"
    rca.rca-operator.tech/occurrences: "12"
    rca.rca-operator.tech/first-seen: "2026-04-02T10:00:00Z"
    rca.rca-operator.tech/last-seen: "2026-04-02T10:45:00Z"
spec:
  priority: 44   # floor(10) + int((50-10) * 0.85)
  trigger:
    eventType: CrashLoopBackOff
  conditions:
    - eventType: OOMKilled
      scope: samePod
  fires:
    incidentType: CrashLoopBackOff-OOMKilled
    severity: P2
    summary: "Auto-detected: {{.EventType}} correlated with OOMKilled on {{.PodName}} in {{.Namespace}}"
```

## Priority Range

Auto-generated rules use priority 10-50, scaled by confidence:

```
priority = 10 + int((50 - 10) * confidence)
```

This ensures auto-generated rules always lose to user-created rules (which typically use priority 100-500). Within auto-rules, higher confidence means higher priority.

## Safeguards

| Safeguard | Description |
|---|---|
| **MaxAutoRules cap** | Hard limit on auto-generated CRDs (default 20) |
| **Pattern dedup** | Same trigger+condition+scope = one pattern, one rule |
| **Tightest scope wins** | samePod preferred over sameNamespace for the same pair |
| **User rule conflict check** | Skips creation if a manual rule already covers the pattern |
| **Expiry** | Rules auto-delete if pattern unseen for the configured duration |
| **MinTimeSpan** | Prevents transient bursts from creating rules |
| **Confidence threshold** | Requires statistical significance (P(B|A) >= threshold) |

## Labels and Annotations

Auto-generated rules are identified by:

| Metadata | Key | Description |
|---|---|---|
| Label | `rca.rca-operator.tech/auto-generated` | Always `"true"` for auto-generated rules |
| Annotation | `rca.rca-operator.tech/pattern-key` | Dedup key: `TriggerType:ConditionType:Scope` |
| Annotation | `rca.rca-operator.tech/confidence` | Current confidence score (0.00-1.00) |
| Annotation | `rca.rca-operator.tech/occurrences` | Total co-occurrence count |
| Annotation | `rca.rca-operator.tech/first-seen` | RFC3339 timestamp of first observation |
| Annotation | `rca.rca-operator.tech/last-seen` | RFC3339 timestamp of most recent observation |

## Metrics

| Metric | Type | Description |
|---|---|---|
| `rca_autodetect_patterns_tracked` | Gauge | Current patterns in the accumulator |
| `rca_autodetect_rules_active` | Gauge | Current auto-generated rule count |
| `rca_autodetect_rules_created_total` | Counter | Total auto-rules created |
| `rca_autodetect_rules_expired_total` | Counter | Total auto-rules expired |
| `rca_autodetect_analysis_duration_seconds` | Histogram | Time per analysis tick |

## Dashboard

The rules API (`GET /api/rules`) includes two additional fields for auto-generated rules:

```json
{
  "name": "auto-crashloopbackoff-oomkilled-samepod",
  "priority": 44,
  "triggerEvent": "CrashLoopBackOff",
  "conditions": ["OOMKilled on samePod"],
  "firesType": "CrashLoopBackOff-OOMKilled",
  "firesSeverity": "P2",
  "autoGenerated": true,
  "confidence": "0.85"
}
```

## Startup Recovery

On first tick after startup, the detector seeds its accumulator from existing auto-generated rules in the cluster. This prevents:

- Re-creating rules that already exist
- Expiring rules prematurely before the accumulator has warmed up

## kubectl Cheatsheet

```bash
# List auto-generated rules
kubectl get rcacorrelationrules -l rca.rca-operator.tech/auto-generated=true

# View confidence and occurrences
kubectl get rcacorrelationrules -l rca.rca-operator.tech/auto-generated=true -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations.rca\.rca-operator\.tech/confidence}{"\t"}{.metadata.annotations.rca\.rca-operator\.tech/occurrences}{"\n"}{end}'

# Delete all auto-generated rules
kubectl delete rcacorrelationrules -l rca.rca-operator.tech/auto-generated=true
```

## Related

- [RCACorrelationRule CRD Reference](../reference/rcacorrelationrule-crd.md)
- [Architecture](../concepts/Architecture.md)
- [Dashboard](DASHBOARD.md)
