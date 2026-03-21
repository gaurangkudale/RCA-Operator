#!/bin/bash
set -e

echo "╔════════════════════════════════════════════════════════════╗"
echo "║   RCA Operator Helm - CRD Upgrade Script                  ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

HELM_RELEASE="${1:-rca-operator}"
NAMESPACE="${2:-rca-system}"

echo -e "${YELLOW}Configuration:${NC}"
echo "  Release: $HELM_RELEASE"
echo "  Namespace: $NAMESPACE"
echo ""

echo -e "${YELLOW}Step 1: Checking current installation...${NC}"
if ! helm list -n "$NAMESPACE" | grep -q "$HELM_RELEASE"; then
    echo -e "${RED}✗ Helm release '$HELM_RELEASE' not found in namespace '$NAMESPACE'${NC}"
    echo "  Available releases:"
    helm list -A
    exit 1
fi
echo -e "${GREEN}✓ Helm release found${NC}"
echo ""

echo -e "${YELLOW}Step 2: Checking for old CRDs (.io API group)...${NC}"
OLD_CRDS=$(kubectl get crd 2>/dev/null | grep "rca.rca-operator.io" || true)
if [ -n "$OLD_CRDS" ]; then
    echo -e "${YELLOW}Found old CRDs:${NC}"
    echo "$OLD_CRDS"
    echo ""

    echo -e "${YELLOW}Step 3: Backing up existing resources...${NC}"
    mkdir -p /tmp/rca-backup
    kubectl get rcaagents.rca.rca-operator.io -A -o yaml > /tmp/rca-backup/rcaagents-backup.yaml 2>/dev/null || echo "No RCAAgents with .io API group found"
    kubectl get incidentreports.rca.rca-operator.io -A -o yaml > /tmp/rca-backup/incidentreports-backup.yaml 2>/dev/null || echo "No IncidentReports with .io API group found"
    echo -e "${GREEN}✓ Backup created at /tmp/rca-backup/${NC}"
    echo ""

    echo -e "${YELLOW}Step 4: Deleting old CRDs...${NC}"
    kubectl delete crd rcaagents.rca.rca-operator.io 2>/dev/null && echo -e "${GREEN}✓ Deleted rcaagents.rca.rca-operator.io${NC}" || echo -e "${YELLOW}⚠ rcaagents.rca.rca-operator.io not found${NC}"
    kubectl delete crd incidentreports.rca.rca-operator.io 2>/dev/null && echo -e "${GREEN}✓ Deleted incidentreports.rca.rca-operator.io${NC}" || echo -e "${YELLOW}⚠ incidentreports.rca.rca-operator.io not found${NC}"
    echo ""
else
    echo -e "${GREEN}✓ No old CRDs found${NC}"
    echo ""
fi

echo -e "${YELLOW}Step 5: Installing correct CRDs (.tech API group)...${NC}"
# Get the chart version
CHART_VERSION=$(grep '^version:' helm/Chart.yaml | awk '{print $2}')
echo "Chart version: $CHART_VERSION"

# Apply CRDs from the Helm chart
kubectl apply -f helm/crds/
echo -e "${GREEN}✓ CRDs installed${NC}"
echo ""

echo -e "${YELLOW}Step 6: Verifying CRDs...${NC}"
kubectl get crd | grep rca.rca-operator.tech
echo ""

echo -e "${YELLOW}Step 7: Upgrading Helm release...${NC}"
helm upgrade "$HELM_RELEASE" helm/ -n "$NAMESPACE" --reuse-values
echo -e "${GREEN}✓ Helm release upgraded${NC}"
echo ""

echo -e "${YELLOW}Step 8: Waiting for operator to be ready...${NC}"
kubectl rollout status deployment -n "$NAMESPACE" "${HELM_RELEASE}-controller-manager" --timeout=60s
echo -e "${GREEN}✓ Operator is ready${NC}"
echo ""

echo -e "${GREEN}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║                ✓ Upgrade Complete!                        ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

echo "Verify the operator is running without errors:"
echo "  kubectl logs -n $NAMESPACE deployment/${HELM_RELEASE}-controller-manager -c manager -f"
echo ""
echo "If you had backed up resources, they are at: /tmp/rca-backup/"
