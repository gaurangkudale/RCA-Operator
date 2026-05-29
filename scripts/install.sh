#!/usr/bin/env bash
# RCA Operator one-line installer.
#
# Usage (recommended):
#   curl -fsSL https://raw.githubusercontent.com/gaurangkudale/RCA-Operator/main/scripts/install.sh | bash
#
# Or from a checkout:
#   ./scripts/install.sh
#
# Environment variables (all optional):
#   RCA_NAMESPACE     Namespace to install into (default: rca-system)
#   RCA_RELEASE       Helm release name           (default: rca-operator)
#   RCA_CHART_VERSION Pin a chart version         (default: latest)
#   RCA_VALUES_FILE   Extra --values file         (default: none)
#   RCA_PROFILE       minimal | full              (default: full)
#   RCA_DRY_RUN       Print commands and exit if non-empty
#
# The installer:
#   1. Verifies kubectl + helm are present
#   2. Adds the rca-operator Helm repo (idempotent)
#   3. Installs / upgrades the chart with `helm upgrade --install --wait`
#   4. Prints how to view the dashboard and the starter RCAAgent

set -euo pipefail

readonly RCA_REPO_NAME="rca-operator"
readonly RCA_REPO_URL="https://gaurangkudale.github.io/rca-operator.github.io/charts"

readonly NAMESPACE="${RCA_NAMESPACE:-rca-system}"
readonly RELEASE="${RCA_RELEASE:-rca-operator}"
readonly CHART_VERSION="${RCA_CHART_VERSION:-}"
readonly VALUES_FILE="${RCA_VALUES_FILE:-}"
readonly PROFILE="${RCA_PROFILE:-full}"
readonly DRY_RUN="${RCA_DRY_RUN:-}"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

run() {
  log "$*"
  [ -n "$DRY_RUN" ] || "$@"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Required command '$1' not found in PATH"
}

main() {
  require_cmd kubectl
  require_cmd helm

  log "Checking cluster connectivity"
  kubectl cluster-info >/dev/null 2>&1 || die "kubectl cannot reach a cluster. Set KUBECONFIG and retry."

  log "Adding Helm repo '${RCA_REPO_NAME}' (idempotent)"
  if ! helm repo list 2>/dev/null | awk '{print $1}' | grep -qx "${RCA_REPO_NAME}"; then
    run helm repo add "${RCA_REPO_NAME}" "${RCA_REPO_URL}"
  else
    log "Repo '${RCA_REPO_NAME}' already added; skipping"
  fi
  run helm repo update "${RCA_REPO_NAME}"

  helm_args=(
    upgrade --install "${RELEASE}" "${RCA_REPO_NAME}/rca-operator"
    --namespace "${NAMESPACE}"
    --create-namespace
    --wait
    --timeout 10m
  )

  if [ -n "${CHART_VERSION}" ]; then
    helm_args+=(--version "${CHART_VERSION}")
  fi

  case "${PROFILE}" in
    full)    ;; # default values
    minimal) helm_args+=(--set opentelemetryOperator.enabled=false
                         --set jaeger.enabled=false
                         --set otelCollector.enabled=false
                         --set otelInstrumentation.enabled=false) ;;
    *)       die "Unknown RCA_PROFILE '${PROFILE}'. Use 'full' or 'minimal'." ;;
  esac

  if [ -n "${VALUES_FILE}" ]; then
    [ -r "${VALUES_FILE}" ] || die "Values file not readable: ${VALUES_FILE}"
    helm_args+=(--values "${VALUES_FILE}")
  fi

  log "Installing chart into '${NAMESPACE}' (release '${RELEASE}', profile '${PROFILE}')"
  run helm "${helm_args[@]}"

  cat <<EOF >&2

\033[1;32mDone.\033[0m The starter RCAAgent is created automatically and watches namespace '${NAMESPACE}'.

  View it:
    kubectl get rcaagent -n ${NAMESPACE}

  Tail incidents as they appear:
    kubectl get incidentreport -A -w

  Open the dashboard locally:
    kubectl -n ${NAMESPACE} port-forward svc/${RELEASE}-dashboard 9090:9090
    open http://localhost:9090

  Watch additional namespaces by editing the starter agent or creating your own:
    kubectl edit rcaagent default -n ${NAMESPACE}

Disable the starter agent with --set defaultAgent.enabled=false on the next upgrade.
EOF
}

main "$@"
