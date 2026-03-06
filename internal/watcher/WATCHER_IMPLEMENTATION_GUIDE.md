# Watcher Layer Implementation Guide
## RCA Operator - Phase 1

---

## 📋 Overview

This guide walks you through implementing the **2.2 Watcher Layer** for the RCA Operator. The watcher layer is the "eyes" of the operator - it continuously monitors Kubernetes resources and detects anomalies in real-time.

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Watcher Manager                         │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Orchestrates all watchers & aggregates events         │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │
│  │ Pod Watcher  │  │ Event Watcher│  │ Node Watcher │       │
│  │              │  │              │  │              │       │
│  │ • CrashLoop  │  │ • K8s Events │  │ • NotReady   │       │
│  │ • OOMKilled  │  │ • Dedup      │  │ • Pressure   │       │
│  │ • ImagePull  │  │              │  │              │       │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘       │
│         │                 │                 │               │
│         └─────────────────┼─────────────────┘               │
│                           ▼                                 │
│               ┌────────────────────┐                        │
│               │   Event Buffer     │                        │
│               │   (Ring Buffer)    │                        │
│               │   + Deduplication  │                        │
│               └─────────┬──────────┘                        │
│                         ▼                                   │
│                ┌─────────────────┐                          │
│                │  Event Channel   │────► To Correlator      │
│                └─────────────────┘                          │
└─────────────────────────────────────────────────────────────┘
```
---

### Phase 2: Watcher Manager (Week 2)

#### 2.1 Manager Responsibilities

The `Manager` is the central coordinator:

1. **Register Watchers**: Add pod, event, node, deployment watchers
2. **Aggregate Events**: Collect events from all watchers into unified channel
3. **Deduplicate**: Use ring buffer to filter duplicate events
4. **Distribute**: Send deduplicated events to subscribers (correlator)

---

### Phase 3: Event Buffer (Week 2)

#### 3.1 Ring Buffer Design

**Purpose:** Prevent event storms and duplicate alerts

**Features:**
- Fixed-size circular buffer (configurable, default 1000 events)
- Time-based deduplication window (default 2 minutes)
- Hash-based duplicate detection
- Thread-safe operations
---

### Phase 4: Pod Watcher Implementation (Week 3)

#### 4.1 What Pod Watcher Detects

**Phase 1 Requirements:**
1. **CrashLoopBackOff** - Restart count > threshold (default: 3)
2. **OOMKilled** - Exit code 137
3. **ImagePullBackOff** - Image pull failures
4. **Pod Pending Too Long** - Stuck in Pending > 5 minutes
5. **Exit Code Intelligence** - All exit codes classified
6. **Grace Period Violation** - Container runs past termination grace period


---

## ✅ Phase 1 Completion Criteria

Watcher layer is complete when:

- [ ] PodWatcher detects CrashLoopBackOff
- [ ] OOMKilled events are captured
- [ ] ImagePullBackOff is detected
- [ ] Pending pods > 5min trigger events
- [ ] Exit codes are classified correctly
- [ ] Grace period violations detected
- [ ] Event deduplication working (no duplicates within 2min window)
- [ ] Manager statistics available
- [ ] Unit tests pass (>80% coverage)
- [ ] E2E test passes (CrashLoop → Incident → Slack)

---

## 📚 Additional Resources

- [Kubernetes API Concepts](https://kubernetes.io/docs/reference/using-api/api-concepts/)
- [Controller Runtime Client](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client)
- [Exit Codes Reference](https://tldp.org/LDP/abs/html/exitcodes.html)

---