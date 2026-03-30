# ADR-0001: Use a Kubernetes-native Incident Pipeline for Phase 1

## Status
Accepted

## Context

The project needs a production-ready Phase 1 architecture that stays tightly scoped:

- no AI analysis
- no autonomous remediation
- no external incident database
- no dashboard dependency on raw cluster queries

The operator still needs to be useful in production. That means the system must detect failures, correlate repeated signals, persist durable incident state, notify humans, and present the same incident story in the UI after restarts.

## Decision

Phase 1 uses this flow:

`kube-apiserver -> watchers -> correlator -> IncidentReport -> notifications + dashboard`

Key architectural choices:

1. Kubernetes remains the only operational data source.
2. `IncidentReport` is the durable incident record.
3. Watchers emit operator events from pods, events, nodes, and deployments.
4. The correlator owns deduplication, severity, grouping, and lifecycle transitions.
5. Notifications are triggered from durable incident state, not directly from watcher events.
6. The dashboard reads only `IncidentReport` and `RCAAgent` resources.

## Consequences

### Positive

- Keeps the runtime simple enough to operate and debug
- Makes restart recovery straightforward because incident state lives in the cluster
- Preserves a stable UI and notification story because both come from the same CRD
- Avoids over-designing the API around features that are not implemented in Phase 1

### Trade-offs

- Root-cause analysis remains human-assisted in Phase 1
- The dashboard shows structured evidence and timelines rather than generated RCA narratives
- Future analysis and remediation features should be introduced later behind separate design decisions

## Follow-up Rules

- Do not require unused configuration such as AI provider secrets
- Keep `RCAAgent` focused on watcher scope, notifications, and retention
- Prefer removing future-facing placeholders over keeping speculative API fields
