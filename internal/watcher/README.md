# Watcher Layer - Quick Start
## RCA Operator Phase 1

This package contains everything you need to implement the **2.2 Watcher Layer** for your RCA Operator.

---

## 📦 What's Included

| File | Purpose |
|------|---------|
| `watcher_types.go` | Core types, interfaces, and exit code mappings |
| `watcher_manager.go` | Orchestrates all watchers and aggregates events |
| `watcher_buffer.go` | Ring buffer with time-based deduplication |
| `pod_watcher.go` | Complete pod monitoring implementation |
| `example_usage.go` | Integration examples and patterns |
| `WATCHER_IMPLEMENTATION_GUIDE.md` | Comprehensive implementation guide |

---


```bash
# Build and install
make manifests
make install
make docker-build docker-push IMG=your-registry/rca-operator:v0.1.0
make deploy IMG=your-registry/rca-operator:v0.1.0

# Create a test RCAAgent
kubectl apply -f config/samples/rcaagent-sample.yaml

# Create a crashing pod to test detection
kubectl run crash-test --image=busybox --restart=Always -- /bin/sh -c "exit 1"

# Check operator logs
kubectl logs -n rca-operator-system deployment/rca-operator-controller-manager -f

# You should see:
# "Starting watcher manager" watcherCount=1
# "Starting watcher" name="pod-watcher"
# "Watch event detected" type="CrashLoopBackOff" severity="High"
```

---

## 📋 What Gets Detected (Phase 1)

✅ **Pod Issues:**
- CrashLoopBackOff (restart count > threshold)
- OOMKilled (exit code 137)
- ImagePullBackOff
- Pod pending too long (> 5 minutes)
- All exit codes classified (1, 2, 126, 127, 130, 134, 137, 139, 143, 255)
- Grace period violations

CrashLoopBackOff timing note:

- Detection is not based on a fixed number of seconds.
- Event is emitted when pod state is `CrashLoopBackOff` and restart count reaches the configured threshold (default: 3).
- Real-world detection time depends on how fast the container crashes and kubelet restart backoff.
- For quick local testing, use a short crash loop (for example `sleep 5; exit 1`) rather than long sleeps.

CrashLoop resolve timing note:

- CrashLoop incidents are marked resolved when a `PodHealthy` signal is emitted and correlator confirms the pod is currently `Running` + `Ready`.
- `PodHealthy` is emitted after ready stability window (default: 60s).
- Ready scan runs every 30s, so practical resolve latency is typically about 60-90s after pod recovery.

---

## 🎯 Next Steps (Phase 1 Continuation)

After the watcher layer is working:

**Week 4:**
1. Implement `event_watcher.go` - Watch Kubernetes Event stream
2. Implement `node_watcher.go` - Monitor node conditions
3. Implement `deployment_watcher.go` - Track deployments and rollouts

**Week 5:**
4. Build the **Correlator** - Aggregate watch events into incidents
5. Implement 5 correlation rules (as per phase 1 plan)

**Week 6:**
6. Auto-create **IncidentReport** CRs
7. Implement Slack notifications
8. Implement PagerDuty notifications

**Week 7:**
9. E2E testing
10. Documentation
11. Release v0.1.0

---

## 📊 Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                RCA Agent Controller                          │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Reconcile Loop                                        │ │
│  │  - Manages WatcherManager lifecycle                    │ │
│  │  - Processes watch events                              │ │
│  └───────────────────┬────────────────────────────────────┘ │
│                      │                                       │
│                      ▼                                       │
│  ┌─────────────────────────────────────────────────────────┐│
│  │            Watcher Manager                              ││
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    ││
│  │  │ Pod Watcher │  │Event Watcher│  │Node Watcher │    ││
│  │  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘    ││
│  │         │                │                │            ││
│  │         └────────────────┼────────────────┘            ││
│  │                          ▼                             ││
│  │              ┌──────────────────────┐                  ││
│  │              │   Event Buffer       │                  ││
│  │              │  (Deduplication)     │                  ││
│  │              └──────────┬───────────┘                  ││
│  │                         ▼                              ││
│  │                 ┌──────────────┐                       ││
│  │                 │Event Channel │───► Correlator       ││
│  │                 └──────────────┘                       ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
```

---

## 🐛 Troubleshooting

### Watcher not detecting events

1. Check manager is started:
   ```bash
   kubectl logs -n rca-operator-system deployment/rca-operator-controller-manager | grep "Starting watcher"
   ```

2. Verify RBAC permissions:
   ```bash
   kubectl auth can-i list pods --as=system:serviceaccount:rca-operator-system:rca-operator-controller-manager
   ```

3. Check watcher statistics (add to status subresource):
   ```go
   status.WatcherStats = r.WatcherManager.GetStats()
   ```

### Events are duplicated

- Check `EventDeduplicationWindow` is set (default: 2 minutes)
- Verify buffer size is adequate for your cluster size
- Review logs for "Event deduplicated" messages

### High memory usage

- Reduce `EventBufferSize` (try 500 for small clusters)
- Limit `Namespaces` to specific namespaces only
- Use informers instead of List operations (Phase 2 optimization)

---

## 📖 Full Documentation

See `WATCHER_IMPLEMENTATION_GUIDE.md` for:
- Detailed architecture explanation
- Step-by-step implementation walkthrough
- Performance considerations
- Testing strategies
- Integration patterns
- Best practices

---

## ✅ Phase 1 Checklist

Use this to track your implementation progress:

- [ ] Files copied to `internal/watcher/`
- [ ] Imports updated with your module path
- [ ] WatcherManager integrated into RCAAgentReconciler
- [ ] RCAAgent CRD spec updated with watcher config
- [ ] Unit tests written for PodWatcher
- [ ] E2E test created (CrashLoop detection)
- [ ] Tested with real crashing pod
- [ ] Events flowing to correlator (stub)
- [ ] Ready for Week 4 (EventWatcher, NodeWatcher, DeploymentWatcher)

---

## Need Help?

Refer to:
1. `WATCHER_IMPLEMENTATION_GUIDE.md` - Comprehensive guide
2. `example_usage.go` - Integration examples
3. Phase 1 Plan in your project docs
4. Kubernetes client-go documentation

---
