# RCA Operator Web UI Dashboard

The RCA Operator includes a built-in web dashboard for visualizing incidents, agents, and cluster insights in real-time.

## Features

- **Real-time Incident Dashboard**: View all active, resolved, and historical incidents
- **RCAAgent Management**: Monitor configured agents and their watch namespaces
- **Timeline Views**: Detailed incident timelines with correlations
- **Resource Health**: Cluster-wide pod, node, and deployment health overview
- **Metrics & Analytics**: Incident patterns, resolution times, and trends

## Quick Start

### 1. Enable Dashboard in Helm Values

```yaml
# values.yaml
dashboard:
  enabled: true
  port: 9090
  service:
    type: ClusterIP
    port: 9090
  ingress:
    enabled: false  # Set to true for external access
```

### 2. Install/Upgrade the Helm Chart

```bash
# For new installations
helm install rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --create-namespace \
  --set dashboard.enabled=true

# For existing installations
helm upgrade rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --set dashboard.enabled=true
```

### 3. Access the Dashboard

**Option A: Port Forwarding (quick access)**
```bash
kubectl port-forward -n rca-system service/rca-operator-dashboard 9090:9090
```
Then open: http://localhost:9090

**Option B: Ingress (production access)**
```bash
# Enable ingress in values.yaml
helm upgrade rca-operator rca-operator/rca-operator \
  --namespace rca-system \
  --set dashboard.ingress.enabled=true \
  --set dashboard.ingress.hosts[0].host=rca.yourdomain.com \
  --set dashboard.ingress.hosts[0].paths[0].path=/ \
  --set dashboard.ingress.hosts[0].paths[0].pathType=Prefix
```

## Configuration Options

### Basic Configuration

```yaml
dashboard:
  enabled: true                    # Enable/disable the dashboard
  port: 9090                      # Dashboard server port
```

### Service Configuration

```yaml
dashboard:
  service:
    type: ClusterIP              # Service type: ClusterIP, NodePort, LoadBalancer
    port: 9090                   # Service port
    targetPort: 9090             # Container port
    # For NodePort access:
    # type: NodePort
    # nodePort: 30090            # External port (30000-32767)
```

### Ingress Configuration

```yaml
dashboard:
  ingress:
    enabled: true
    className: "nginx"           # Ingress class (nginx, traefik, etc.)
    annotations:
      nginx.ingress.kubernetes.io/rewrite-target: /
      cert-manager.io/cluster-issuer: letsencrypt-prod
    hosts:
      - host: rca-operator.yourdomain.com
        paths:
          - path: /
            pathType: Prefix
    tls:
      - secretName: rca-operator-tls
        hosts:
          - rca-operator.yourdomain.com
```

## Production Setup Examples

### 1. Internal Access Only (Corporate Network)

```yaml
# values-internal.yaml
dashboard:
  enabled: true
  service:
    type: LoadBalancer
    port: 9090
  ingress:
    enabled: false
```

Install:
```bash
helm install rca-operator rca-operator/rca-operator \
  -f values-internal.yaml \
  --namespace rca-system \
  --create-namespace
```

### 2. External Access with HTTPS

```yaml
# values-external.yaml
dashboard:
  enabled: true
  service:
    type: ClusterIP
    port: 9090
  ingress:
    enabled: true
    className: "nginx"
    annotations:
      cert-manager.io/cluster-issuer: "letsencrypt-prod"
      nginx.ingress.kubernetes.io/ssl-redirect: "true"
    hosts:
      - host: rca.company.com
        paths:
          - path: /
            pathType: Prefix
    tls:
      - secretName: rca-operator-tls
        hosts:
          - rca.company.com
```

Install:
```bash
helm install rca-operator rca-operator/rca-operator \
  -f values-external.yaml \
  --namespace rca-system \
  --create-namespace
```

### 3. Development/Testing Setup

```yaml
# values-dev.yaml
dashboard:
  enabled: true
  service:
    type: NodePort
    nodePort: 30090
  ingress:
    enabled: false
```

Access via: `http://<node-ip>:30090`

## Security Considerations

### 1. Authentication & Authorization

Currently, the dashboard does not include built-in authentication. For production use:

**Option A: Ingress-level Authentication**
```yaml
annotations:
  nginx.ingress.kubernetes.io/auth-type: basic
  nginx.ingress.kubernetes.io/auth-secret: basic-auth
  nginx.ingress.kubernetes.io/auth-realm: "RCA Operator Dashboard"
```

**Option B: OAuth2 Proxy**
```yaml
annotations:
  nginx.ingress.kubernetes.io/auth-url: "https://oauth2-proxy.yourdomain.com/oauth2/auth"
  nginx.ingress.kubernetes.io/auth-signin: "https://oauth2-proxy.yourdomain.com/oauth2/start"
```

### 2. Network Policies

Restrict dashboard access with NetworkPolicies:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: rca-operator-dashboard
  namespace: rca-system
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: rca-operator
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: ingress-nginx  # Only allow ingress controller access
    ports:
    - protocol: TCP
      port: 9090
```

## Troubleshooting

### Dashboard Not Accessible

1. **Check if dashboard is enabled:**
   ```bash
   kubectl get pods -n rca-system -o jsonpath='{.items[0].spec.containers[0].args}'
   ```
   Should include `--dashboard-addr=:9090`

2. **Verify service exists:**
   ```bash
   kubectl get svc -n rca-system rca-operator-dashboard
   ```

3. **Check operator logs:**
   ```bash
   kubectl logs -n rca-system deployment/rca-operator-controller-manager -c manager | grep dashboard
   ```
   Should show: `INFO dashboard Starting dashboard server {"addr": ":9090"}`

4. **Test internal access:**
   ```bash
   kubectl exec -n rca-system deployment/rca-operator-controller-manager -- curl localhost:9090
   ```

### Port Forward Issues

```bash
# Kill existing port-forwards
pkill -f "kubectl.*port-forward.*9090"

# Start new port-forward with verbose output
kubectl port-forward -n rca-system service/rca-operator-dashboard 9090:9090 -v=6
```

### Ingress Issues

1. **Check ingress status:**
   ```bash
   kubectl get ingress -n rca-system rca-operator-dashboard
   kubectl describe ingress -n rca-system rca-operator-dashboard
   ```

2. **Verify ingress controller logs:**
   ```bash
   kubectl logs -n ingress-nginx deployment/ingress-nginx-controller
   ```

3. **Check DNS resolution:**
   ```bash
   nslookup rca-operator.yourdomain.com
   ```

### Performance Tuning

For large clusters with many incidents:

```yaml
# Increase resources for dashboard responsiveness
resources:
  limits:
    cpu: 1000m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 256Mi

# Tune manager args for dashboard performance
manager:
  args:
    - --leader-elect
    - --health-probe-bind-address=:8081
    - --dashboard-addr=:9090
    - --dashboard-refresh-interval=30s  # Adjust refresh rate
```

## Dashboard API Endpoints

The dashboard exposes a REST API for integration:

- `GET /api/incidents` - List incidents
- `GET /api/stats` - Aggregate dashboard stats, namespaces, and agents

Supported query parameters for `GET /api/incidents`:

- `namespace`
- `phase`
- `severity`
- `type`
- `query`
- `sort` with `newest`, `oldest`, or `severity`
- `limit`
- `offset`

**Example:**
```bash
# Get incidents via API
curl "http://localhost:9090/api/incidents?phase=Active&severity=P1&sort=severity&limit=50"

# Fetch dashboard stats
curl http://localhost:9090/api/stats
```

## Integration Examples

### Grafana Dashboard

Create Grafana dashboards using the API endpoints:

```json
{
  "targets": [
    {
      "url": "http://rca-operator-dashboard.rca-system:9090/api/incidents",
      "format": "table"
    }
  ]
}
```

### Custom Monitoring

```bash
#!/bin/bash
# Check for critical incidents
CRITICAL_COUNT=$(curl -s http://rca-operator-dashboard.rca-system:9090/api/incidents | jq '.[] | select(.severity=="P1") | length')

if [ "$CRITICAL_COUNT" -gt 0 ]; then
    echo "ALERT: $CRITICAL_COUNT critical incidents found"
    # Send alert to Slack/PagerDuty
fi
```

## Changelog

### v0.1.3
- ✅ Added dashboard Service and Ingress support
- ✅ Configurable dashboard settings in Helm values
- ✅ Production-ready security examples
- ✅ Comprehensive troubleshooting guide

### Future Features (Roadmap)
- 🔮 Built-in OAuth2/OIDC authentication
- 🔮 Role-based access control (RBAC)
- 🔮 Real-time WebSocket updates
- 🔮 Export/import incident data
- 🔮 Custom dashboard themes
- 🔮 Multi-cluster dashboard support

## Support

For dashboard-specific issues:
1. Check the troubleshooting section above
2. Review operator logs: `kubectl logs -n rca-system deployment/rca-operator-controller-manager`
3. Open an issue: https://github.com/gaurangkudale/RCA-Operator/issues
4. Include your Helm values and describe the access method you're trying to use

## Examples Repository

Find more dashboard configuration examples at:
https://github.com/gaurangkudale/RCA-Operator/tree/main-gk/examples/dashboard/
