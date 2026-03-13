#!/usr/bin/env bash
# simulate-cpu-throttle.sh
# ──────────────────────────────────────────────────────────────────────────────
# CPU throttling scenario — works on Kubernetes ≥ 1.28 where the kubelet no
# longer emits CPUThrottlingHigh events natively (deprecated 1.23, removed
# 1.28).  This script injects a synthetic CPUThrottlingHigh K8s Event that has
# exactly the same structure the kubelet used to produce, so the EventWatcher
# routes it through the same handleCPUThrottling() code path.
#
# Expected watcher signal : CPUThrottlingEvent
# Expected incident type  : ResourceSaturation  (severity P3)
# Detected by             : EventWatcher.handleCPUThrottling()
#
# Prerequisites:
#   - The cpu-throttle Deployment is running in namespace development:
#       kubectl apply -f test/fixtures/pods/cpu-throttle.yaml
#   - RCA Operator is running and watching the development namespace
#
# Usage:
#   ./test/fixtures/pods/simulate-cpu-throttle.sh [NAMESPACE] [DEPLOYMENT_NAME] [ITERATIONS]
#
# Arguments:
#   NAMESPACE        Namespace of the Deployment.   Default: development
#   DEPLOYMENT_NAME  Name of the Deployment.        Default: cpu-throttle-demo
#   ITERATIONS       How many synthetic events to inject (with 15 s gaps).
#                    Default: 1
#
# Examples:
#   ./test/fixtures/pods/simulate-cpu-throttle.sh
#   ./test/fixtures/pods/simulate-cpu-throttle.sh development cpu-throttle-demo 3
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

NAMESPACE="${1:-development}"
DEPLOYMENT_NAME="${2:-cpu-throttle-demo}"
ITERATIONS="${3:-1}"
CONTAINER_NAME="throttle-demo"   # must match spec.containers[].name in cpu-throttle.yaml

# ── Colour helpers ─────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'
info()    { echo -e "${CYAN}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[OK]${NC}   $*"; }
die()     { echo -e "${RED}[FAIL]${NC} $*" >&2; exit 1; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
require_cmd kubectl

# ── Verify the Deployment exists ─────────────────────────────────────────────
info "Checking Deployment $NAMESPACE/$DEPLOYMENT_NAME ..."
kubectl get deployment "$DEPLOYMENT_NAME" -n "$NAMESPACE" >/dev/null 2>&1 \
  || die "Deployment '$DEPLOYMENT_NAME' not found in namespace '$NAMESPACE'." \
         "Apply it first: kubectl apply -f test/fixtures/pods/cpu-throttle.yaml"

# ── Resolve the pod created by the Deployment ─────────────────────────────────
# The Deployment creates pods with label app=<DEPLOYMENT_NAME>.
# We pick the first Running pod — there is only one replica in the fixture.
info "Resolving pod created by Deployment '$DEPLOYMENT_NAME'..."

POD_NAME=""
for attempt in $(seq 1 12); do
  POD_NAME=$(kubectl get pods -n "$NAMESPACE" \
    -l "app=${DEPLOYMENT_NAME}" \
    --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)

  if [[ -n "$POD_NAME" && "$POD_NAME" != "null" ]]; then
    break
  fi

  info "  No Running pod yet (attempt $attempt/12) — waiting 5 s..."
  sleep 5
done

[[ -z "$POD_NAME" || "$POD_NAME" == "null" ]] \
  && die "Could not find a Running pod for Deployment '$DEPLOYMENT_NAME' after 60 s." \
         "Check: kubectl get pods -n $NAMESPACE -l app=$DEPLOYMENT_NAME"

# ── Confirm the pod is Ready ──────────────────────────────────────────────────
PHASE=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" \
  -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

if [[ "$PHASE" != "Running" ]]; then
  info "Pod phase is '$PHASE' — waiting up to 30 s for Ready..."
  kubectl wait --for=condition=Ready pod/"$POD_NAME" -n "$NAMESPACE" --timeout=30s 2>/dev/null \
    || die "Pod '$POD_NAME' is not Ready after 30 s (phase=$PHASE)." \
           "Check: kubectl describe pod $POD_NAME -n $NAMESPACE"
fi

# ── Gather pod metadata (uid + node) ──────────────────────────────────────────
POD_UID=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
NODE_NAME=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}')
[[ -z "$POD_UID" ]]   && die "Could not read pod UID"
[[ -z "$NODE_NAME" ]] && die "Could not read pod nodeName"

info "Deployment : $NAMESPACE/$DEPLOYMENT_NAME"
info "Pod        : $NAMESPACE/$POD_NAME"
info "Pod UID    : $POD_UID"
info "Node       : $NODE_NAME"
info "Container  : $CONTAINER_NAME"
info "Iterations : $ITERATIONS"
echo ""

# ── Inject synthetic CPUThrottlingHigh K8s Event(s) ──────────────────────────
# The Event structure mirrors what the kubelet emitted pre-1.28:
#   involvedObject.kind      = "Pod"
#   involvedObject.fieldPath = "spec.containers{<containerName>}"   ← parsed by
#                               parseContainerFromFieldPath()
#   reason                   = "CPUThrottlingHigh"
#
# A unique suffix on event name lets us inject multiple events (each starts
# fresh in the EventWatcher informer's onEventAdd handler).  The EventWatcher
# dedup key is: namespace + podUID + reason + "/" + containerName, so a
# repeated event within the dedup window is silently dropped.  If you need to
# re-fire within 2 minutes, restart the operator to clear in-memory state.

for i in $(seq 1 "$ITERATIONS"); do
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  SUFFIX="$(date +%s)-${i}"
  EVENT_NAME="cpu-throttle-synthetic-${SUFFIX}"

  info "Injecting CPUThrottlingHigh event [$i/$ITERATIONS] (name=$EVENT_NAME)..."

  kubectl apply -f - <<EOF >/dev/null
apiVersion: v1
kind: Event
metadata:
  name: ${EVENT_NAME}
  namespace: ${NAMESPACE}
reason: CPUThrottlingHigh
message: "45% throttling of CPU in namespace ${NAMESPACE}/${POD_NAME} on container ${CONTAINER_NAME} (synthetic demo event)"
type: Warning
involvedObject:
  apiVersion: v1
  kind: Pod
  name: ${POD_NAME}
  namespace: ${NAMESPACE}
  uid: ${POD_UID}
  fieldPath: "spec.containers{${CONTAINER_NAME}}"
source:
  component: kubelet
  host: ${NODE_NAME}
firstTimestamp: ${NOW}
lastTimestamp: ${NOW}
count: 1
EOF

  success "Event injected: $EVENT_NAME"

  if [[ "$i" -lt "$ITERATIONS" ]]; then
    info "Waiting 15 s before next injection..."
    sleep 15
  fi
done

echo ""
info "Synthetic event(s) created. The EventWatcher will process them within seconds."
echo ""
info "Verify the K8s Event is visible:"
echo "  kubectl get events -n $NAMESPACE --field-selector reason=CPUThrottlingHigh"
echo ""
info "Watch for the IncidentReport (ResourceSaturation / P3):"
echo "  kubectl get incidentreports -n $NAMESPACE -w"
echo ""
info "Check operator logs for the CPUThrottling signal:"
echo "  kubectl logs -n rca-operator-system deploy/rca-operator-controller-manager -c manager \\"
echo "    | grep -i 'CPUThrottling\\|ResourceSaturation'"
echo ""
info "Clean up:"
echo "  kubectl delete deploy $DEPLOYMENT_NAME -n $NAMESPACE"
echo "  kubectl delete incidentreports -n $NAMESPACE -l rca.rca-operator.io/incident-type=ResourceSaturation"
