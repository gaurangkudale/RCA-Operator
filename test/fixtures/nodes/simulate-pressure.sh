#!/usr/bin/env bash
# simulate-pressure.sh
# ──────────────────────────────────────────────────────────────────────────────
# Node pressure scenario — works with any Kubernetes cluster (Kind, k3d, etc.)
#
# Supported pressure types (pass as first argument):
#   disk      Simulates DiskPressure   → NodeFailure / P2
#   memory    Simulates MemoryPressure → NodeFailure / P2
#   pid       Simulates PIDPressure    → NodeFailure / P3
#
# Expected watcher signal : NodePressureEvent (NodeWatcher watches corev1.Node
#                           conditions directly — no K8s Event required)
# Expected incident type  : NodeFailure
# Detection method        : NodeWatcher.handlePressureCondition() detects
#                           condition.Status == True on the named condition type
#
# How it works:
#   kubectl patch --subresource=status writes directly to the Node status
#   conditions array, overriding the kubelet-managed condition exactly as an
#   operator running integration tests would.  The real kubelet will overwrite
#   the faked condition after its next status heartbeat (default ~10 s).
#   The script therefore keeps re-patching the condition every 8 s during the
#   observation window so the NodeWatcher has time to see and emit the event.
#
# Prerequisites:
#   kubectl ≥ 1.24   (subresource status patch is stable)
#   curl             (health-check only — optional)
#   RCA Operator running (make run or deployed in a Kind/k3d cluster)
#
# Usage:
#   ./test/fixtures/nodes/simulate-pressure.sh <disk|memory|pid> [NODE_NAME] [OBSERVE_SECONDS]
#
# Arguments:
#   TYPE              One of: disk | memory | pid
#   NODE_NAME         Kubernetes Node to patch. Defaults to the first worker node
#                     (or the control-plane node if no workers exist).
#   OBSERVE_SECONDS   How long to hold the faked condition (seconds). Default: 60
#
# Examples:
#   ./test/fixtures/nodes/simulate-pressure.sh disk
#   ./test/fixtures/nodes/simulate-pressure.sh memory kind-worker 90
#   ./test/fixtures/nodes/simulate-pressure.sh pid kind-worker2 45
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

# ── Args ──────────────────────────────────────────────────────────────────────
PRESSURE_TYPE="${1:-}"
TARGET_NODE="${2:-}"
OBSERVE_SECONDS="${3:-60}"

# ── Colour helpers ─────────────────────────────────────────────────────────────
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

# ── Validate type ─────────────────────────────────────────────────────────────
case "${PRESSURE_TYPE}" in
  disk)
    CONDITION_TYPE="DiskPressure"
    SEVERITY="P2"
    FAKE_MSG="Simulated — imagefs.available(0%) threshold exceeded"
    ;;
  memory)
    CONDITION_TYPE="MemoryPressure"
    SEVERITY="P2"
    FAKE_MSG="Simulated — memory.available(100Mi) threshold exceeded"
    ;;
  pid)
    CONDITION_TYPE="PIDPressure"
    SEVERITY="P3"
    FAKE_MSG="Simulated — pid.available(10) threshold exceeded"
    ;;
  *)
    die "Unknown pressure type '${PRESSURE_TYPE}'. Use: disk | memory | pid"
    ;;
esac

# ── Pre-flight ─────────────────────────────────────────────────────────────────
require_cmd kubectl

# ── Resolve node ──────────────────────────────────────────────────────────────
if [[ -z "$TARGET_NODE" ]]; then
  # Prefer a worker node over the control-plane
  TARGET_NODE="$(kubectl get nodes --no-headers \
    | grep -v 'control-plane\|master' \
    | awk '{print $1}' \
    | head -n1)"
fi

if [[ -z "$TARGET_NODE" ]]; then
  # Fallback: use the control-plane node (single-node clusters)
  TARGET_NODE="$(kubectl get nodes --no-headers | awk '{print $1}' | head -n1)"
fi

[[ -z "$TARGET_NODE" ]] && die "Could not determine a Node to patch. Is your cluster running?"

# Verify the node exists
kubectl get node "$TARGET_NODE" >/dev/null 2>&1 \
  || die "Node '$TARGET_NODE' not found. Run: kubectl get nodes"

# ── Watch namespace for IncidentReports ───────────────────────────────────────
# NodeWatcher stores incidents in the RCAAgent's own namespace.
# The development agent in test/fixtures/agents/rcaagent.yaml uses namespace=default.
WATCH_NS="${WATCH_NS:-default}"

info "Node        : $TARGET_NODE"
info "Condition   : $CONDITION_TYPE (Status → True)"
info "Severity    : $SEVERITY"
info "Hold for    : ${OBSERVE_SECONDS}s"
info "Watch ns    : $WATCH_NS"
echo ""
warn "NOTE: The real kubelet will overwrite faked conditions every ~10 s."
warn "      This script re-patches the condition every 8 s to keep it active."
echo ""

# ── Timestamp helper ──────────────────────────────────────────────────────────
rfc3339() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }

# ── Build the JSON patch ──────────────────────────────────────────────────────
# For DiskPressure: set the pre-existing condition Status to True.
# Using json-merge-patch (--type=merge) on status sub-resource replaces the
# matching condition item.  Because of strategic-merge rules on the list,
# we must use json6902 to target the exact element by type key.
patch_condition_true() {
  local cond_type="$1"
  local msg="$2"
  local ts
  ts="$(rfc3339)"
  # Build a JSON6902 patch that locates the existing condition by type and
  # replaces Status, Reason, Message, and LastHeartbeatTime.
  kubectl patch node "$TARGET_NODE" --subresource=status --type=json \
    -p "[
      {\"op\":\"replace\",\"path\":\"/status/conditions\",\"value\":
        $(kubectl get node "$TARGET_NODE" -o jsonpath='{.status.conditions}' \
            | python3 -c "
import json, sys
conds = json.load(sys.stdin)
for c in conds:
    if c['type'] == '${cond_type}':
        c['status'] = 'True'
        c['reason'] = 'KubeletHasSufficient${cond_type}Simulated'
        c['message'] = '${msg}'
        c['lastHeartbeatTime'] = '${ts}'
        c['lastTransitionTime'] = '${ts}'
conds_str = json.dumps(conds)
print(conds_str)
"
        )
      }
    ]" >/dev/null 2>&1 || true
}

patch_condition_false() {
  local cond_type="$1"
  local ts
  ts="$(rfc3339)"
  kubectl patch node "$TARGET_NODE" --subresource=status --type=json \
    -p "[
      {\"op\":\"replace\",\"path\":\"/status/conditions\",\"value\":
        $(kubectl get node "$TARGET_NODE" -o jsonpath='{.status.conditions}' \
            | python3 -c "
import json, sys
conds = json.load(sys.stdin)
for c in conds:
    if c['type'] == '${cond_type}':
        c['status'] = 'False'
        c['reason'] = 'KubeletHasSufficient${cond_type}Restored'
        c['message'] = 'Condition restored by simulate-pressure.sh'
        c['lastHeartbeatTime'] = '${ts}'
        c['lastTransitionTime'] = '${ts}'
conds_str = json.dumps(conds)
print(conds_str)
"
        )
      }
    ]" >/dev/null 2>&1 || true
}

# ── Cleanup on exit ───────────────────────────────────────────────────────────
cleanup() {
  echo ""
  warn "Restoring ${CONDITION_TYPE} condition to False on node '${TARGET_NODE}'..."
  patch_condition_false "$CONDITION_TYPE"
  success "${CONDITION_TYPE} condition restored to False (kubelet will also self-heal)."
  echo ""
  info "Watch incident resolution:"
  echo "  kubectl get incidentreports -n $WATCH_NS -w"
  echo ""
  info "Clean up after done:"
  echo "  kubectl delete incidentreports -n $WATCH_NS -l rca.rca-operator.io/incident-type=NodeFailure"
}
trap cleanup EXIT INT TERM

# ── Step 1: inject the faked condition ────────────────────────────────────────
info "Injecting ${CONDITION_TYPE}=True on node '$TARGET_NODE'..."
patch_condition_true "$CONDITION_TYPE" "$FAKE_MSG"
success "Condition patched."
echo ""

# ── Step 2: re-patch loop so kubelet heartbeat doesn't overwrite it ──────────
info "Entering re-patch loop (every 8 s) for ${OBSERVE_SECONDS}s..."
info "The NodeWatcher scans every 30 s and also reacts to informer change events."
info "Press Ctrl-C at any time to restore the condition and exit."
echo ""

END_TIME=$(( $(date +%s) + OBSERVE_SECONDS ))
iteration=0
while (( $(date +%s) < END_TIME )); do
  sleep 8
  iteration=$(( iteration + 1 ))
  REMAINING=$(( END_TIME - $(date +%s) ))
  patch_condition_true "$CONDITION_TYPE" "$FAKE_MSG" || true
  printf "  [t+%ds] ${CONDITION_TYPE}=True maintained  (${REMAINING}s remaining)\n" \
    "$(( iteration * 8 ))"
done

# ── Step 3: verify K8s condition ──────────────────────────────────────────────
echo ""
info "Current node conditions:"
kubectl get node "$TARGET_NODE" -o jsonpath='{.status.conditions}' \
  | python3 -c "
import json, sys
conds = json.load(sys.stdin)
for c in conds:
    print(f\"  {c['type']:<20} status={c['status']:<10} reason={c.get('reason','')}\")
"

# ── Step 4: show IncidentReports ──────────────────────────────────────────────
echo ""
info "IncidentReports (NodeFailure) in namespace '$WATCH_NS':"
kubectl get incidentreports -n "$WATCH_NS" \
  -l rca.rca-operator.io/incident-type=NodeFailure 2>/dev/null \
  || echo "  (none yet — operator may need a moment)"
echo ""

# cleanup() fires automatically on EXIT
