# Phase 3 Architecture: Strong Correlation Engine

## Overview

Phase 3 extends RCA Operator from **telemetry-attached incidents** (Phase 2) to **intelligent cross-signal incident analysis** — a deterministic pattern-matching engine that correlates logs, metrics, and traces together to pinpoint exact root causes without relying exclusively on LLM API calls.

### Design Principle: Deterministic First, LLM Second

Phase 3 introduces a **parallel RCA strategy**:

| Method | Speed | Cost | Accuracy | Transparency |
|--------|-------|------|----------|--------------|
| Pattern Matching | `<500ms` | `$0` | `≥90%` on known patterns | Full evidence chain |
| LLM (GPT-4) | `2–5s` | `~$0.01/call` | `~89%` | Model reasoning |

Both run side-by-side. Users choose which RCA to act on. Pattern matching becomes the primary fast path; LLM remains the fallback for unknown patterns.

---

## What Phase 2 Left Unsolved

Phase 2 attaches telemetry to incidents but **does not correlate signals intelligently**:

```
Phase 2 (Current):
  K8s Signal → Incident → Query traces + metrics + logs → Attach to incident → GPT-4 → RCA

Problem:
  - LLM is a black box — engineers cannot audit "why did it say memory leak?"
  - No historical knowledge — same incident pattern reprocessed as if new every time
  - Cost scales with incident volume — high incident rate = high API spend
  - Network dependency — LLM unavailable = no RCA
```

Phase 3 solves this by adding a deterministic pattern-matching stage **before** the LLM call.

---

## Architecture

```
                              K8s Signal
                                  │
          ┌───────────────────────▼──────────────────────────┐
          │ Stage 1: Incident Creation (unchanged)            │
          │ PodWatcher/NodeWatcher → Normalize → Enrich       │
          │ → Rule Engine → IncidentReport CR created         │
          └───────────────────────┬──────────────────────────┘
                                  │
          ┌───────────────────────▼──────────────────────────┐
          │ Stage 2: Telemetry Queries (Phase 2, unchanged)   │
          │ CrossSignalEnricher queries:                       │
          │   • Jaeger  → traces with errors in time window   │
          │   • Prometheus → metrics for service in window    │
          │   • SigNoz  → logs matching pod/service           │
          └───────────────────────┬──────────────────────────┘
                                  │
          ┌───────────────────────▼──────────────────────────┐
          │ Stage 3: Signal Aggregator (NEW)                  │
          │ Groups signals by 5-second time windows           │
          │ Normalises timestamps across systems              │
          │ Extracts error patterns + stack traces from logs  │
          └───────────────────────┬──────────────────────────┘
                                  │
          ┌───────────────────────▼──────────────────────────┐
          │ Stage 4: Anomaly Detector (NEW)                   │
          │ Computes 1-hour rolling baseline per metric       │
          │ Flags values > 2σ as anomalies                    │
          │ Assigns statistical confidence scores             │
          └───────────────────────┬──────────────────────────┘
                                  │
          ┌───────────────────────▼──────────────────────────┐
          │ Stage 5: Pattern Matcher (NEW)                    │
          │ Evaluates RCACorrelationRule CRs with pattern:    │
          │ section against detected anomalies                │
          │ Scores confidence, extracts evidence              │
          │ Queries fingerprint DB (also in CRDs)             │
          └───────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┴──────────┐
          │                          │
          ▼                          ▼
  Pattern RCA (<500ms)         LLM RCA (2–5s)
  Confidence: 0.92             Confidence: 0.89
  Evidence chain shown         Model reasoning shown
  Known issues shown           No history
          │                          │
          └──────────┬───────────────┘
                     ▼
          Dashboard: User sees both,
          selects preferred RCA
          Verified RCA → stored in
          RCACorrelationRule fingerprint
```

---

## New Package: `internal/correlation/`

```
internal/correlation/
  aggregator.go         -- Groups telemetry signals by time window
  aggregator_test.go
  anomaly.go            -- Baseline computation + statistical spike detection
  anomaly_test.go
  patterns.go           -- Loads + evaluates RCACorrelationRule pattern definitions
  patterns_test.go
  fingerprinter.go      -- Queries CRDs for matching known-issue fingerprints
  fingerprinter_test.go
```

---

## Component Reference

### Signal Aggregator (`aggregator.go`)

Groups telemetry signals into a unified time-aligned structure. This is the input to all downstream stages.

**Key types:**

```go
// AggregatedSignals is the unified cross-signal structure produced from
// raw telemetry query results. Signals are grouped into 5-second windows.
type AggregatedSignals struct {
    Metrics     []MetricAnomaly   // CPU, memory, request rate, error rate
    ErrorLogs   []ErrorLog        // Extracted error + exception entries
    TraceErrors []TraceSpanError  // Span-level errors from distributed traces
    Latencies   []LatencySample   // P95, P99 latency samples

    TimeRange  [2]time.Time       // Earliest and latest signal timestamp
    Service    string             // Service name
    Pod        string             // Pod name (empty for service-scoped)
    Node       string             // Node name (empty for pod-scoped)
}

// MetricAnomaly is a single metric data point that has been classified.
type MetricAnomaly struct {
    Name       string    // "memory_usage_bytes", "cpu_usage_ratio", etc.
    Value      float64
    Baseline   float64   // 1-hour rolling average
    Sigma      float64   // How many standard deviations above baseline
    Time       time.Time
}

// ErrorLog is an error log entry with extracted structured fields.
type ErrorLog struct {
    Message    string
    Time       time.Time
    Severity   string       // ERROR, FATAL
    Exception  string       // e.g., "java.lang.OutOfMemoryError"
    StackTrace []StackFrame // Parsed stack frames (if extractStackTrace=true)
}

// StackFrame is a single frame extracted from an exception stack trace.
type StackFrame struct {
    Class    string // "PaymentReconciler"
    Method   string // "Reconcile"
    File     string // "PaymentReconciler.java"
    Line     int    // 123
}

// TraceSpanError is an erroring span from a distributed trace.
type TraceSpanError struct {
    TraceID   string
    SpanID    string
    Operation string   // e.g., "POST /checkout"
    Service   string
    Error     string   // Error message or exception type
    Duration  time.Duration
    Time      time.Time
}
```

**Clock skew handling:**

Jaeger, Prometheus, and SigNoz may have clock skew up to ±2 seconds in practice. The aggregator groups signals using **overlap windows** — a signal is included if it falls within the window extended by a configurable tolerance (default `5s`). Skew exceeding `30s` is logged as a warning.

---

### Anomaly Detector (`anomaly.go`)

Computes a **1-hour rolling baseline** for each metric series and flags deviations.

**Key types:**

```go
type AnomalyType string

const (
    MemorySpike    AnomalyType = "memory_spike"
    CPUSpike       AnomalyType = "cpu_spike"
    LatencySpike   AnomalyType = "latency_spike"
    ErrorRateHigh  AnomalyType = "error_rate_high"
    MemoryGradual  AnomalyType = "memory_gradual_increase" // Slow leak
    ConnectionLeak AnomalyType = "connection_count_spike"
)

type Anomaly struct {
    Type       AnomalyType
    Severity   float64    // 0.0–1.0: distance from baseline normalised
    Sigma      float64    // Raw σ count (e.g., 3.2σ above mean)
    StartTime  time.Time
    EndTime    time.Time
    Baseline   float64
    Observed   float64
    Confidence float64    // Statistical significance (p-value derived)
    Duration   time.Duration
}
```

**Algorithm:**

```
For each metric time-series M in AggregatedSignals:
  1. Compute baseline:
     - Query 1-hour historical window before incident start
     - Calculate mean(M[baseline]) and stddev(M[baseline])
  
  2. Flag anomaly if:
     - current_value > mean + 2.0 * stddev  (spike upward)
     - OR current_value < mean - 2.0 * stddev  (drop)
     - AND anomaly persists for ≥ 30 seconds  (false positive filter)
  
  3. Score:
     - sigma = (current - mean) / stddev
     - severity = min(1.0, (sigma - 2.0) / 6.0)  // Normalised 2σ–8σ → 0.0–1.0
     - confidence = 1 - p_value(sigma)  // Two-tailed z-test
```

**Memory gradual increase detection** (for slow leaks):

Instead of a point spike, this pattern requires a monotonically increasing trend over the observation window. The detector uses linear regression; a positive slope with R² > 0.8 indicates a leak.

---

### Pattern Matcher (`patterns.go`)

The pattern matcher loads all `RCACorrelationRule` CRs that contain a `spec.pattern` block and evaluates them against the detected anomalies and aggregated signals.

**Evaluation algorithm:**

```
For each rule R with spec.pattern defined (sorted by spec.priority desc):
  
  1. Check requiredMetrics:
     - For each required metric M:
       - Is there a matching Anomaly of type M.anomalyType?
       - Does severity ≥ M.minSeverity?
       - Does duration ≥ M.duration?
     - If any required metric absent → rule does not match
  
  2. Check requiredLogPatterns:
     - For each pattern P:
       - Does any ErrorLog message match P.regex?
       - If P.extractStackTrace=true, extract stack frames
     - If any required pattern absent → rule does not match
  
  3. Check requiredTraceErrors:
     - For each trace error type T:
       - Does any TraceSpanError.error match T.errorType?
     - If any required trace error absent → rule does not match
  
  4. Check timeAlignment:
     - Are all matched signals within spec.pattern.timeAlignment window?
     - Default window: 5 seconds
  
  5. Compute confidence:
     score = (matchedMetrics/totalRequired * 0.4)
           + (matchedLogs/totalRequired * 0.35)
           + (matchedTraces/totalRequired * 0.25)
     confidence = score * sigma_multiplier  // Higher σ = higher confidence
  
  6. If confidence ≥ spec.pattern.minConfidence:
     - Rule matches
     - Generate RCA using spec.fires.summary template
     - Inject extracted evidence (stack frames, metric values, etc.)
     - Attach fingerprint if spec.fingerprint.knownIssue=true
```

**Pattern confidence scoring breakdown:**

| Signal Weight | Reason |
|---|---|
| Metrics: 40% | Most reliable; continuous and precise |
| Logs: 35% | Direct error messages; occasionally noisy |
| Traces: 25% | Latency corroborates but may lag actual failure |

**First match wins** — patterns are evaluated in `spec.priority` order. A pattern with `priority: 500` is evaluated before `priority: 100`.

---

### Fingerprinter (`fingerprinter.go`)

Queries the Kubernetes API for `RCACorrelationRule` CRs where `spec.fingerprint.knownIssue: true` and returns matching known issues.

**Matching criteria:**

- Incident type matches the rule's `spec.fires.incidentType`
- Pattern matched with confidence ≥ `spec.pattern.minConfidence`
- Service name matches (if `spec.fingerprint.serviceFilter` set)

**Output appended to pattern match:**

```go
type FingerprintMatch struct {
    IssueID       string
    IssueTitle    string
    Occurrences   int
    LastOccurred  *metav1.Time
    Resolution    FingerprintResolution
    Impact        FingerprintImpact
    RelatedIssues []string
}
```

---

## CRD Extension: `RCACorrelationRule`

The `RCACorrelationRule` spec is extended with two optional blocks: `pattern` and `fingerprint`. Existing rules without these blocks continue to work exactly as before (backward compatible).

### New Fields in `spec`

```yaml
spec:
  # ...existing trigger, conditions, fires fields unchanged...

  # NEW: Pattern definition for intelligent cross-signal matching
  pattern:
    name: string                   # Unique pattern identifier
    description: string            # Human-readable description

    requiredMetrics:
      - type: string               # Prometheus metric name or alias
        anomalyType: string        # memory_spike | cpu_spike | latency_spike | error_rate_high | memory_gradual_increase | connection_count_spike
        minSeverity: float64       # 0.0–1.0 (how far from baseline required)
        duration: duration         # How long the anomaly must persist
    
    requiredLogPatterns:
      - regex: string              # Go RE2 regex applied to log message
        minOccurrences: int        # Minimum occurrences required (default 1)
        extractStackTrace: bool    # If true, parse stack frames from this log
    
    requiredTraceErrors:
      - errorType: string          # Error type to match in span error field
        serviceFilter: string      # Restrict to specific service (empty = all)

    timeAlignment: duration        # Max time difference between signals (default 5s)
    minConfidence: float64         # Minimum confidence to fire (default 0.80)

  # NEW: Known issue fingerprint for historical tracking
  fingerprint:
    knownIssue: bool               # true = show as "known issue" in dashboard
    issueId: string                # Unique stable ID (e.g., "PAYMENT-OOM-v1.3.0")
    issueTitle: string             # Short human-readable title
    firstObserved: datetime
    lastObserved: datetime
    occurrences: int               # Manually or auto-incremented

    resolution:
      type: string                 # version_update | config_change | code_review | scaling
      action: string               # Human-readable remediation action
      verified: bool
      verifiedAt: datetime
      verifiedBy: string           # Email or username

    impact:
      description: string
      affectedServices: []string
      estimatedMttr: duration      # Mean time to resolution

    rootCause:
      component: string            # Class/module name
      file: string                 # Source file
      line: int                    # Line number
      function: string             # Method name
      issue: string                # Plain-language description

    relatedIssues: []string        # IssueIDs of similar known issues
```

### CRD Extension Example

```yaml
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCACorrelationRule
metadata:
  name: memory-leak-oom-pattern
  labels:
    pattern-type: memory_leak
    known-issue: "true"
spec:
  priority: 500

  # Standard K8s signal correlation (unchanged)
  trigger:
    eventType: OOMKilled
  conditions:
    - eventType: CrashLoopBackOff
      scope: samePod
  fires:
    incidentType: MemoryLeak
    severity: P2
    summary: "Memory leak detected on pod {{.PodName}} in {{.Namespace}}"

  # Cross-signal pattern matching (new)
  pattern:
    name: "jvm_heap_memory_leak"
    description: "JVM heap gradually exhausted until OOMKill"
    requiredMetrics:
      - type: "memory_usage_bytes"
        anomalyType: "memory_spike"
        minSeverity: 0.7
        duration: "2m"
    requiredLogPatterns:
      - regex: "OutOfMemoryError|java heap space"
        minOccurrences: 1
      - regex: "at \\w+\\.\\w+\\(.*\\.java:\\d+\\)"
        extractStackTrace: true
    requiredTraceErrors:
      - errorType: "allocation_failed"
    timeAlignment: "10s"
    minConfidence: 0.85

  # Known issue fingerprint (filled after first verified incident)
  fingerprint:
    knownIssue: true
    issueId: "JVM-HEAP-LEAK-UNBOUNDED-COLLECTION"
    issueTitle: "JVM heap leak from unbounded collection in reconciliation loop"
    occurrences: 47
    resolution:
      type: "code_review"
      action: "Bound the collection in the reconciliation loop (PR #123)"
      verified: true
    rootCause:
      component: "PaymentReconciler"
      file: "PaymentReconciler.java"
      line: 123
      function: "Reconcile"
      issue: "HashMap never cleared between reconcile iterations"
```

---

## IncidentReport Status Extension

Two new fields added to `IncidentReportStatus` (backward compatible, both optional):

```go
// PatternMatches lists all cross-signal pattern matches evaluated for this incident.
// Populated asynchronously by the pattern matcher after telemetry enrichment.
// +optional
// +listType=atomic
PatternMatches []PatternMatchResult `json:"patternMatches,omitempty"`

// SelectedRCA is which RCA the user or auto-selector chose: "pattern" or "ai".
// +optional
SelectedRCA string `json:"selectedRca,omitempty"`
```

```go
// PatternMatchResult is the outcome of evaluating one RCACorrelationRule pattern
// against the aggregated telemetry signals for an incident.
type PatternMatchResult struct {
    // RuleRef is the name of the RCACorrelationRule that produced this match.
    RuleRef    string  `json:"ruleRef"`
    // PatternName is spec.pattern.name from the rule.
    PatternName string `json:"patternName"`
    // Confidence is 0.0–1.0 pattern match confidence.
    Confidence  string `json:"confidence"`
    // RootCause is the rendered root cause string from the rule template.
    RootCause   string `json:"rootCause,omitempty"`
    // Evidence lists the matched signals that confirmed this pattern.
    Evidence    []string `json:"evidence,omitempty"`
    // Playbook is the recommended remediation steps.
    Playbook    []string `json:"playbook,omitempty"`
    // FingerprintMatch is populated when spec.fingerprint.knownIssue=true.
    // +optional
    FingerprintMatch *FingerprintMatchResult `json:"fingerprintMatch,omitempty"`
    // EvaluatedAt is when this pattern was evaluated.
    EvaluatedAt *metav1.Time `json:"evaluatedAt,omitempty"`
}

// FingerprintMatchResult is a matched known issue from a rule's fingerprint block.
type FingerprintMatchResult struct {
    IssueID      string   `json:"issueId"`
    IssueTitle   string   `json:"issueTitle"`
    Occurrences  int      `json:"occurrences"`
    LastOccurred *metav1.Time `json:"lastOccurred,omitempty"`
    Resolution   string   `json:"resolution,omitempty"`
    RelatedIssues []string `json:"relatedIssues,omitempty"`
}
```

---

## Built-in Patterns (Phase 3 MVP)

Five patterns ship with the operator in the default `RCACorrelationRule` set (enabled by `defaultRules.enabled: true`).

### Pattern 1: Memory Leak

**Signals required:**

| Signal | Requirement |
|---|---|
| Metric | `memory_usage_bytes`: spike or gradual increase ≥ 0.7 severity, persisting ≥ 2m |
| Log | Matches `OutOfMemoryError\|heap space\|java heap` |
| Log | Stack trace extractable |
| Trace | Span error containing `allocation_failed` or `heap` |

**RCA template:** `Memory leak in {{.StackFrame.Class}}.{{.StackFrame.Method}}() at {{.StackFrame.File}}:{{.StackFrame.Line}}`

**Playbook:**
1. `kubectl rollout undo deployment/{{.Deployment}} -n {{.Namespace}}`
2. Review code changes in `{{.StackFrame.Class}}` since last deployment
3. Check for unbounded collections or missing `close()` calls
4. Temporarily increase heap: `kubectl set resources deployment/{{.Deployment}} --limits=memory=2Gi`

---

### Pattern 2: Connection Pool Exhaustion

**Signals required:**

| Signal | Requirement |
|---|---|
| Metric | `connection_count` or `active_connections`: spike ≥ 0.8 severity |
| Metric | `latency_p99`: spike ≥ 0.6 severity |
| Log | Matches `connection refused\|socket timeout\|pool exhausted\|too many connections` |
| Trace | Span error on database or remote call spans |

**RCA template:** `Connection pool exhausted — active connections spiked from {{.Baseline}} to {{.Peak}}`

**Playbook:**
1. Check connection pool configuration: `kubectl describe configmap {{.Service}}-config`
2. Verify connection cleanup in application code (look for leaked connections)
3. Temporarily scale service: `kubectl scale deployment/{{.Deployment}} --replicas={{.CurrentReplicas + 2}}`
4. Increase pool size via config if traffic is legitimate growth

---

### Pattern 3: CPU Exhaustion

**Signals required:**

| Signal | Requirement |
|---|---|
| Metric | `cpu_usage_ratio`: spike ≥ 0.85 severity, persisting ≥ 1m |
| Metric | `latency_p99`: elevated ≥ 0.5 severity (CPU contention causes latency) |
| Log | Matches `cpu throttled\|rate limit\|timeout\|deadline exceeded` |
| Trace | Slow spans (duration > 5× baseline average) |

**RCA template:** `CPU exhaustion — {{.Service}} throttled at {{.CPUPeak}}% of limit`

**Playbook:**
1. `kubectl top pods -n {{.Namespace}} --sort-by=cpu` — identify top consumers
2. Check for infinite loops or N+1 query patterns in recent changes
3. Increase CPU limit: `kubectl set resources deployment/{{.Deployment}} --limits=cpu=2000m`
4. Add horizontal autoscaling: `kubectl autoscale deployment/{{.Deployment}} --cpu-percent=70 --min=2 --max=10`

---

### Pattern 4: Database Latency Propagation

**Signals required:**

| Signal | Requirement |
|---|---|
| Metric | Upstream service `latency_p99`: spike ≥ 0.6 severity |
| Metric | Database service `latency_p99`: spike ≥ 0.8 severity (higher than upstream) |
| Log | Matches `query timeout\|slow query\|deadlock\|lock wait timeout` |
| Trace | Span with `db.system` attribute and duration > 2× baseline |
| Topology | Upstream → Database dependency edge present |

**RCA template:** `Database latency propagated upstream — DB P99 is {{.DBLatency}}ms causing {{.Service}} P99 to reach {{.ServiceLatency}}ms`

**Playbook:**
1. Identify slow queries: `SHOW PROCESSLIST` or check slow query log
2. Review recent schema migrations or index changes
3. Temporarily increase query timeout in `{{.Service}}` config
4. Scale database read replicas if read-heavy workload

---

### Pattern 5: Cascading Service Failure

**Signals required:**

| Signal | Requirement |
|---|---|
| Incident | Incident on service A (upstream dependency) active in window |
| Metric | Service B `error_rate`: elevated ≥ 0.7 severity **after** service A incident |
| Metric | Service B `latency_p99`: elevated ≥ 0.6 severity |
| Topology | Service B → Service A dependency edge present |
| Time | Service B incident start is after service A incident start |

**RCA template:** `Cascading failure — {{.ServiceB}} degraded after {{.ServiceA}} incident at {{.ServiceAIncidentTime}}`

**Playbook:**
1. Resolve service A incident first: investigate separately
2. Implement circuit breaker in {{.ServiceB}} for calls to {{.ServiceA}}
3. Add timeout + retry with exponential backoff for {{.ServiceA}} calls
4. Check `kubectl get incidentreport -n {{.Namespace}} -l phase=Active` for service A status

---

## Dashboard Integration

### Incidents Tab — Enhanced RCA Section

```
┌─────────────────────────────────────────────────────────────────────┐
│ Root Cause Analysis                                                 │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌──────────────────────────────┐  ┌────────────────────────────┐  │
│  │ Pattern Match   [92% conf]   │  │ AI Investigation  [89%]    │  │
│  │ ● RECOMMENDED                │  │                            │  │
│  │                              │  │                            │  │
│  │ Memory leak in               │  │ Memory leak in             │  │
│  │ PaymentReconciler.java:123   │  │ payment-svc v1.3.0         │  │
│  │                              │  │ (unbounded HashMap)        │  │
│  │ ⚠ KNOWN ISSUE (47 times)    │  │                            │  │
│  │ Last: 2026-03-15             │  │ Playbook:                  │  │
│  │ Fix: Rollout v1.3.5 ✓       │  │ kubectl rollout undo ...   │  │
│  │                              │  │                            │  │
│  │ Evidence:                    │  │ Evidence:                  │  │
│  │ • Memory: 490MB (3.2σ above) │  │ • Heap OOM at 10:17:35    │  │
│  │ • OOMError at 10:17:35      │  │ • Trace: 15 heap allocs   │  │
│  │ • Trace: heap alloc failed  │  │ • Memory 490MB peak        │  │
│  │                              │  │                            │  │
│  │ [Copy Playbook]              │  │ [Copy Playbook]            │  │
│  └──────────────────────────────┘  └────────────────────────────┘  │
│                                                                     │
│  [Verify Pattern Match as Correct] [Mark as Different Root Cause]  │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

User actions:
- **Verify Pattern Match** — marks the rule's `fingerprint.verified=true`, increments `fingerprint.occurrences`
- **Mark as Different Root Cause** — flags pattern as false positive, decreases confidence weighting for this service

---

## New API Endpoints

### GET /api/patterns/{namespace}/{name}

Returns both pattern-matched and LLM RCA for an incident.

**Response:**

```json
{
  "incident": {
    "name": "oom-payment-abc123",
    "namespace": "production",
    "phase": "Active"
  },
  "patterns": [
    {
      "ruleId": "memory-leak-oom-pattern",
      "patternName": "jvm_heap_memory_leak",
      "confidence": 0.92,
      "method": "pattern_matching",
      "rootCause": "Memory leak in PaymentReconciler.Reconcile() at PaymentReconciler.java:123",
      "evidence": [
        "Memory spike: 490MB (3.2σ above 1h baseline of 195MB, severity: 0.95)",
        "Log: java.lang.OutOfMemoryError: Java heap space at 10:17:35",
        "Trace span: allocation_failed in heap ops at 10:17:32",
        "Stack frame: PaymentReconciler.Reconcile() PaymentReconciler.java:123"
      ],
      "playbook": [
        "kubectl rollout undo deployment/payment-svc -n production",
        "Review code changes in PaymentReconciler.java since last deploy",
        "Check for unbounded HashMap in reconciliation loop"
      ],
      "fingerprintMatch": {
        "issueId": "JVM-HEAP-LEAK-UNBOUNDED-COLLECTION",
        "issueTitle": "JVM heap leak from unbounded collection",
        "occurrences": 47,
        "lastOccurred": "2026-03-15T14:30:00Z",
        "resolution": "Bound the collection in the reconciliation loop (PR #123)",
        "relatedIssues": ["PAYMENT-OOM-v1.2.5"]
      }
    }
  ],
  "llmRca": {
    "method": "ai_gpt4",
    "rootCause": "Memory leak in PaymentReconciler — unbounded HashMap never cleared between iterations",
    "confidence": "0.89",
    "playbook": [
      "kubectl rollout undo deployment/payment-svc -n production",
      "kubectl scale deployment/payment-svc --replicas=6"
    ],
    "evidence": [
      "Trace: 15 allocations in HeapAllocator between 10:15-10:17",
      "Memory metric: 490MB peak vs 200MB baseline",
      "Log: OutOfMemoryError on Java heap space"
    ]
  }
}
```

### POST /api/patterns/{namespace}/{name}/verify

Marks a pattern match as verified by a human operator. Updates the `RCACorrelationRule` fingerprint metadata.

**Request:**

```json
{
  "ruleId": "memory-leak-oom-pattern",
  "verifiedBy": "sre@example.com",
  "resolution": "Rolled back to v1.3.4, investigating v1.3.5 regression",
  "notes": "Found unbounded HashMap at line 123, fix in PR #456"
}
```

---

## CLI Flags (Phase 3 Additions)

| Flag | Default | Description |
|---|---|---|
| `--pattern-matching-enabled` | `true` | Enable cross-signal pattern matching |
| `--pattern-baseline-window` | `1h` | Rolling window for metric baseline computation |
| `--pattern-time-alignment` | `5s` | Max time skew between correlated signals |
| `--pattern-min-confidence` | `0.80` | Global minimum confidence threshold |
| `--pattern-anomaly-sigma` | `2.0` | Standard deviations above baseline to flag anomaly |
| `--pattern-anomaly-duration` | `30s` | Minimum anomaly persistence before flagging |

---

## Helm Values (Phase 3 Additions)

```yaml
# Correlation Pattern Matching (Phase 3)
patternMatching:
  enabled: true
  
  baselineWindow: 1h
  timeAlignment: 5s
  minConfidence: 0.80
  
  anomaly:
    sigmaThreshold: 2.0    # Flag values > 2σ from baseline
    minDuration: 30s        # Ignore transient spikes shorter than this
  
  # Ship 5 default patterns via defaultRules
  defaultPatterns:
    enabled: true            # Creates RCACorrelationRule CRs for all 5 patterns
    memoryLeak: true
    connectionLeak: true
    cpuExhaustion: true
    dbLatency: true
    cascadingFailure: true
```

---

## Implementation Milestones

| Milestone | Status | Description |
|---|---|---|
| M1: Signal Aggregator | Planned | `aggregator.go` — time alignment, clock skew handling, log pattern extraction |
| M2: Anomaly Detector | Planned | `anomaly.go` — 1-hour rolling baseline, statistical scoring, 6 anomaly types |
| M3: Pattern Matcher (5 patterns) | Planned | `patterns.go` — evaluate RCACorrelationRule pattern blocks, generate RCA |
| M4: CRD Extension + Fingerprinter | Planned | Extend `RCACorrelationRule` spec, `fingerprinter.go`, verify API |
| M5: Dashboard UI + Parallel RCA | Planned | Side-by-side pattern + LLM display, verify button, known issue badge |

---

## Testing Strategy

### Unit Tests (per component)

| File | Key Test Cases |
|---|---|
| `aggregator_test.go` | Time window grouping, ±5s clock skew tolerance, log regex extraction, stack trace parsing |
| `anomaly_test.go` | Baseline computation from 60-minute window, σ calculation, gradual increase detection via linear regression |
| `patterns_test.go` | Each of 5 patterns fires correctly, partial match returns correct confidence, time alignment enforcement, template rendering |
| `fingerprinter_test.go` | CRD lookup, known issue matching by incident type + service, fingerprint metadata returned |

### Integration Tests

```
Scenario: Memory leak end-to-end
1. Start mock SigNoz + Prometheus + Jaeger
2. Inject: memory spike metric + OOM log + heap allocation trace
3. Submit OOMKilled event to correlator
4. Assert: PatternMatches[0].PatternName == "jvm_heap_memory_leak"
5. Assert: PatternMatches[0].Confidence >= 0.85
6. Assert: PatternMatches[0].Evidence contains stack frame
```

### Manual Verification

1. Deploy OTel Demo (has payment service that can be configured to leak memory)
2. Enable `patternMatching.enabled: true` in Helm
3. Trigger memory pressure: `kubectl exec -n otel-demo -it payment-... -- java -Xmx128m -jar /dev/null`
4. Observe pattern match in dashboard within 30 seconds
5. Click "Verify Pattern Match" → check `kubectl get rcacorrelationrule memory-leak-oom-pattern -o jsonpath='{.spec.fingerprint}'`

---

## Backward Compatibility

Phase 3 is **100% backward compatible**:

- Existing `RCACorrelationRule` CRs without `pattern:` or `fingerprint:` blocks continue to work unchanged
- Pattern matching is opt-in at the rule level (add `pattern:` block to enable)
- Dashboard falls back gracefully to LLM-only RCA when no pattern matches
- `patternMatching.enabled: false` in Helm disables Stage 3+4 entirely
- No changes to Phase 1 signal detection or Phase 2 telemetry queries

---

## Related Documentation

- [Phase 2 Architecture](PHASE2_ARCHITECTURE.md) — Cross-signal enrichment and telemetry query layer
- [RCACorrelationRule CRD reference](../reference/rcacorrelationrule-crd.md) — Full field reference (update needed after CRD extension)
- [Correlation Rules feature](../features/CORRELATION-RULES.md) — How rule evaluation works
- [AI Investigation feature](../features/AI-INVESTIGATION.md) — LLM RCA (parallel path)
- [Dashboard API reference](../reference/dashboard-api.md) — `/api/patterns` endpoint
- [Signal Pipeline](../features/SIGNALS.md) — Upstream stages (Normalize → Enrich)
- [Topology feature](../features/TOPOLOGY.md) — Blast radius used in cascading failure pattern
