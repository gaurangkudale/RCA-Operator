# Architecture

This document describes the production target for RCA Operator Phase 1 only.

## Goal

Phase 1 exists to answer four questions reliably:

1. What is broken right now?
2. Which resources are affected?
3. Is this a new incident or the same incident repeating?
4. Have humans been notified and given enough context to act?

Phase 1 does not include AI analysis, autonomous remediation, or external incident databases.

## Runtime Topology

```text
Kubernetes API Server
        |
        v
+-----------------------------+
| controller-runtime Manager  |
|  - shared cache             |
|  - leader election          |
|  - health endpoints         |
+-------------+---------------+
              |
      +-------+-------+-------------------+
      |               |                   |
      v               v                   v
+-------------+ +-------------+ +-------------------+
| Pod Watcher  | | Event Watcher| | Deployment Watcher|
+-------------+ +-------------+ +-------------------+
      |               |                   |
      +---------------+---------+---------+
                                |
                                v
                      +-------------------+
                      | Node Watcher      |
                      +---------+---------+
                                |
                                v
                      +-------------------+
                      | Correlator        |
                      | - deduplication   |
                      | - grouping        |
                      | - severity        |
                      +---------+---------+
                                |
                                v
                      +-------------------+
                      | IncidentReport CR |
                      | durable state     |
                      +----+---------+----+
                           |         |
                           v         v
                 +---------------+  +----------------+
                 | Notifications |  | Dashboard API  |
                 | Slack/PD/K8s  |  | + static UI    |
                 +---------------+  +----------------+
```

## Source of Truth

`IncidentReport` is the durable record for Phase 1.

The operator may use in-memory correlation buffers and watcher state, but dashboards, notifications, restart recovery, and human investigation all center on the `IncidentReport` resource written into the cluster.

## Layer Responsibilities

### Watchers

Watchers convert raw Kubernetes object changes into normalized operator events.

Current Phase 1 watcher coverage:

- pods
- events
- nodes
- deployments

### Correlator

The correlator:

- suppresses duplicate signals
- groups related symptoms into one incident
- assigns severity
- decides whether to create, update, reopen, or resolve an `IncidentReport`

### Incident Lifecycle

`IncidentReport` resources move through:

- `Detecting`
- `Active`
- `Resolved`

This lets the operator distinguish noisy first observations from incidents that remain present long enough to matter.

### Notification Layer

The incident controller sends notifications from `IncidentReport` state:

- Slack
- PagerDuty
- Kubernetes events on the `IncidentReport`

This keeps outbound alerting tied to the durable incident lifecycle rather than transient watcher events.

### Dashboard

The dashboard serves a static UI and JSON API from the operator process. It reads only `IncidentReport` and `RCAAgent` resources, never raw cluster objects, so the UI stays cheap to run and consistent with the operator’s incident model.

## Production Properties

Phase 1 is considered production-ready when these properties hold:

- one active incident per fingerprint
- deterministic incident lifecycle
- safe restart behavior using CR-backed state
- notification deduplication
- dashboard driven entirely from CR data
- least-privilege RBAC

## Related

- [Phase 1 Plan](../phases/PHASE1.md)
- [Phase 1 Architecture](../phases/PHASE1_ARCHITECTURE.md)
- [RCAAgent Reference](../reference/rcaagent-crd.md)
