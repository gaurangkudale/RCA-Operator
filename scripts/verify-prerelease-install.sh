#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RELEASE_NAME="${RELEASE_NAME:-rca-operator}"
NAMESPACE="${NAMESPACE:-rca-prerelease-system}"
DEMO_NAMESPACE="${DEMO_NAMESPACE:-rca-demo}"
KIND_CLUSTER="${KIND_CLUSTER:-rca-operator-prerelease}"
IMG="${IMG:-controller:prerelease}"
RUN_KIND_INSTALL="${RUN_KIND_INSTALL:-true}"
KUBECONFORM_K8S_VERSION="${KUBECONFORM_K8S_VERSION:-1.29.0}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

render() {
  local profile="$1"
  local values_file="$2"
  helm template "$RELEASE_NAME" ./helm \
    --namespace "$NAMESPACE" \
    -f "$values_file" \
    > "/tmp/rca-operator-${profile}.yaml"
}

echo "==> Checking tools"
need helm
need kubectl
need kubeconform

echo "==> Helm lint"
helm lint ./helm --strict
helm lint ./helm --strict -f helm/values-full.yaml
helm lint ./helm --strict -f helm/values-minimal.yaml
helm lint ./helm --strict -f helm/values-external-observability.yaml \
  --set graphBuilder.jaegerQueryURL=http://jaeger-query.observability.svc:16686

echo "==> Helm template profiles"
render full helm/values-full.yaml
render minimal helm/values-minimal.yaml
render external-observability helm/values-external-observability.yaml

echo "==> kubeconform rendered profiles"
kubeconform -strict -ignore-missing-schemas -kubernetes-version "$KUBECONFORM_K8S_VERSION" -summary \
  /tmp/rca-operator-full.yaml \
  /tmp/rca-operator-minimal.yaml \
  /tmp/rca-operator-external-observability.yaml

if [[ "$RUN_KIND_INSTALL" != "true" ]]; then
  echo "==> Skipping Kind install because RUN_KIND_INSTALL=$RUN_KIND_INSTALL"
  exit 0
fi

need kind
need docker

cleanup() {
  echo "==> Cleanup"
  helm uninstall "$RELEASE_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || true
  kubectl delete namespace "$DEMO_NAMESPACE" --ignore-not-found=true >/dev/null 2>&1 || true
  kubectl delete namespace "$NAMESPACE" --ignore-not-found=true >/dev/null 2>&1 || true
  kind delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Creating Kind cluster $KIND_CLUSTER"
kind create cluster --name "$KIND_CLUSTER"

echo "==> Building and loading image $IMG"
make docker-build IMG="$IMG"
kind load docker-image "$IMG" --name "$KIND_CLUSTER"

echo "==> Installing full profile"
repo="${IMG%:*}"
tag="${IMG##*:}"
helm upgrade --install "$RELEASE_NAME" ./helm \
  --namespace "$NAMESPACE" --create-namespace \
  -f helm/values-full.yaml \
  --set image.repository="$repo" \
  --set image.tag="$tag" \
  --set image.pullPolicy=Never \
  --wait --timeout 10m

echo "==> Applying quickstart RCAAgent and fixture"
kubectl create namespace "$DEMO_NAMESPACE"
kubectl apply -f - <<EOF
apiVersion: rca.rca-operator.tech/v1alpha1
kind: RCAAgent
metadata:
  name: prerelease-agent
  namespace: ${NAMESPACE}
spec:
  watchNamespaces:
    - ${DEMO_NAMESPACE}
  incidentRetention: 1d
EOF
kubectl apply -n "$DEMO_NAMESPACE" -f test/fixtures/pods/crashloop.yaml

echo "==> Waiting for IncidentReport"
for _ in $(seq 1 90); do
  count="$(kubectl get incidentreports -A --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  if [[ "$count" != "0" ]]; then
    kubectl get incidentreports -A
    echo "Pre-release install verification passed"
    exit 0
  fi
  sleep 2
done

echo "Timed out waiting for IncidentReport" >&2
kubectl get pods -A >&2 || true
kubectl logs -n "$NAMESPACE" deployment/rca-operator-controller-manager -c manager --tail=200 >&2 || true
exit 1
