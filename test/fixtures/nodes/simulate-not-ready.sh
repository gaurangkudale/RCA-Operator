#!/usr/bin/env bash
# simulate-not-ready.sh
# ──────────────────────────────────────────────────────────────────────────────
# NodeNotReady scenario — Kind cluster only
#
# Expected watcher signal : NodeNotReady  (K8s Event reason=NodeNotReady emitted
#                           by kube-controller-manager after node-monitor-grace-period)
# Expected incident type  : NodeFailure
# Severity                : P1
# Resolution              : Automatic — run restore-not-ready.sh (or the unpause
#                           step below) to bring the node back; incident patches to
#                           Resolved once node returns to Ready.
#
# Prerequisites:
#   kind  ≥ 0.23   (https://kind.sigs.k8s.io/)
#   docker         (used to pause/unpause the Kind node container)
#   kubectl        (for observing events and IncidentReports)
#   RCA Operator running (make run or deployed)
#
# Usage:
#   ./test/fixtures/nodes/simulate-not-ready.sh [KIND_NODE_NAME] [OBSERVE_SECONDS]
#
# Arguments:
#   KIND_NODE_NAME    Name of the Kind worker node container to pause.
#                     Defaults to the first worker returned by `kind get nodes`.
#   OBSERVE_SECONDS   How long (in seconds) to keep the node paused for observation.
#                     Default: 90  (node-monitor-grace-period default is 40 s, so
#                     90 s gives the controller-manager time to fire the event)
#
# Example:
#   ./test/fixtures/nodes/simulate-not-ready.sh kind-worker 120
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

# ── Configurable defaults ──────────────────────────────────────────────────────
KIND_NODE_NAME="${1:-}"
OBSERVE_SECONDS="${2:-90}"

# ── Helpers ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()    { echo -e "${CYAN}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[OK]${NC}   $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
die()     { echo -e "${RED}[FAIL]${NC} $*" >&2; exit 1; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }

# ── Pre-flight checks ─────────────────────────────────────────────────────────
require_cmd docker
require_cmd kubectl
require_cmd kind

# ── Resolve node name ─────────────────────────────────────────────────────────
if [[ -z "$KIND_NODE_NAME" ]]; then
  KIND_NODE_NAME="$(kind get nodes 2>/dev/null | grep -v 'control-plane' | head -n1)"
fi

if [[ -z "$KIND_NODE_NAME" ]]; then
  die "Could not determine a Kind worker node. Pass the node name as the first argument."
fi

# Verify the container exists
if ! docker inspect "$KIND_NODE_NAME" >/dev/null 2>&1; then
  die "Docker container '$KIND_NODE_NAME' not found. Is the Kind cluster running?"
fi

# Verify it maps to a Kubernetes Node
K8S_NODE_NAME="$(kubectl get node "$KIND_NODE_NAME" -o jsonpath='{.metadata.name}' 2>/dev/null || true)"
if [[ -z "$K8S_NODE_NAME" ]]; then
  die "No Kubernetes Node named '$KIND_NODE_NAME'. Check the node name with: kubectl get nodes"
fi

# ── Watch namespace for IncidentReports ───────────────────────────────────────
# NodeNotReady K8s Events are in the 'default' namespace; RCAAgent rcaagent-development
# watches 'default', so IncidentReports appear there.
WATCH_NS="default"

info "Kind node container : $KIND_NODE_NAME"
info "Kubernetes node     : $K8S_NODE_NAME"
info "Observation window  : ${OBSERVE_SECONDS}s"
info "Watching namespace  : $WATCH_NS"
echo ""

# ── Step 1: pause the Kind worker ─────────────────────────────────────────────
info "Pausing Kind worker container '$KIND_NODE_NAME' (kubelet stops heartbeating)..."
docker pause "$KIND_NODE_NAME"
success "Container paused."

# ── Step 2: observe ───────────────────────────────────────────────────────────
echo ""
info "Waiting for node to appear NotReady (node-monitor-grace-period ≈ 40 s)..."
info "Press Ctrl-C to abort and unpause immediately."
echo ""

# Show node status changes in the background
{
  for i in $(seq 1 "$OBSERVE_SECONDS"); do
    sleep 1
    STATUS="$(kubectl get node "$K8S_NODE_NAME" --no-headers 2>/dev/null | awk '{print $2}')"
    if [[ "$STATUS" == *"NotReady"* ]]; then
      echo -e "${YELLOW}[t+${i}s]${NC} Node status: $STATUS — NodeNotReady K8s Event should fire shortly."
      break
    fi
    if (( i % 10 == 0 )); then
      echo -e "[t+${i}s] Node status: ${STATUS:-unknown}"
    fi
  done
} &
STATUS_PID=$!

# Wait for observe window (trap Ctrl-C to still unpause)
cleanup() {
  kill "$STATUS_PID" 2>/dev/null || true
  warn "Interrupted — restoring node..."
  docker unpause "$KIND_NODE_NAME" || true
  success "Node '$KIND_NODE_NAME' unpaused."
  exit 0
}
trap cleanup INT TERM

sleep "$OBSERVE_SECONDS"
kill "$STATUS_PID" 2>/dev/null || true

# ── Step 3: check for K8s NodeNotReady events ─────────────────────────────────
echo ""
info "K8s Events for node '$K8S_NODE_NAME':"
kubectl get events -n "$WATCH_NS" --field-selector "involvedObject.name=$K8S_NODE_NAME" \
  --sort-by='.lastTimestamp' 2>/dev/null | tail -10 || true

# ── Step 4: check for IncidentReports ────────────────────────────────────────
echo ""
info "IncidentReports in namespace '$WATCH_NS':"
kubectl get incidentreports -n "$WATCH_NS" \
  -l rca.rca-operator.io/incident-type=NodeFailure 2>/dev/null || true

# ── Step 5: unpause ───────────────────────────────────────────────────────────
echo ""
info "Unpausing Kind worker '$KIND_NODE_NAME'..."
docker unpause "$KIND_NODE_NAME"
success "Container unpaused. Node will recover in ~10–20 s."
echo ""
info "Watch node recovery:"
echo "  kubectl get nodes -w"
echo ""
info "Watch incident resolution:"
echo "  kubectl get incidentreports -n $WATCH_NS -w"
echo ""
info "Cleanup when done:"
echo "  kubectl delete incidentreports -n $WATCH_NS -l rca.rca-operator.io/incident-type=NodeFailure"
