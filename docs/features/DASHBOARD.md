# Dashboard

RCA Operator includes a built-in dashboard for Phase 1 incident visibility.

## Data Contract

The dashboard reads only:

- `IncidentReport`
- `RCAAgent`

It does not query Pods, Nodes, Events, Deployments, or any external datastore directly. This keeps the UI consistent with the operator’s durable incident model.

## What It Shows

- current incident phase and severity
- summary, reason, and message
- first seen, active, last seen, and resolved timestamps
- affected resources and scope
- incident timeline
- monitored namespaces and configured agents

## Access

The dashboard is enabled by default in the Helm chart.

### Port-forward

```bash
kubectl port-forward -n rca-system service/rca-operator-dashboard 9090:9090
```

Open `http://localhost:9090`.

### Ingress

Use the example values files in [examples/dashboard](/Users/gaurangkudale/gk-github/RCA-Operator/examples/dashboard) if you want to expose the dashboard through an ingress or load balancer.

## Operational Notes

- The dashboard is best treated as an operator-facing UI, not a multi-user portal.
- Authentication should be handled at the ingress or network boundary.
- If the dashboard looks wrong, check the underlying `IncidentReport` objects first since they are the source of truth.
