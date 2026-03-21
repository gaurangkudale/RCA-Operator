#!/bin/bash
# Update version references in installation documentation
# Usage: ./scripts/update-install-versions.sh <operator-version> <helm-version>
# Example: ./scripts/update-install-versions.sh v0.1.5 0.1.3

set -e

OPERATOR_VERSION="${1}"
HELM_VERSION="${2}"

if [ -z "$OPERATOR_VERSION" ] || [ -z "$HELM_VERSION" ]; then
    echo "Usage: $0 <operator-version> <helm-version>"
    echo "Example: $0 v0.1.5 0.1.3"
    exit 1
fi

# Remove 'v' prefix if present in Helm version
HELM_VERSION_CLEAN="${HELM_VERSION#v}"

INSTALL_DOC="docs/getting-started/installation.md"

echo "Updating installation documentation..."
echo "  Operator version: $OPERATOR_VERSION"
echo "  Helm version: helm-v$HELM_VERSION_CLEAN"

# Create backup
cp "$INSTALL_DOC" "${INSTALL_DOC}.bak"

# Update operator version (kubectl installation)
sed -i.tmp "s|download/v[0-9.]\+/install.yaml|download/${OPERATOR_VERSION}/install.yaml|g" "$INSTALL_DOC"
sed -i.tmp "s|download/v[0-9.]\+/crds.yaml|download/${OPERATOR_VERSION}/crds.yaml|g" "$INSTALL_DOC"

# Update Helm version
sed -i.tmp "s|download/helm-v[0-9.]\+/rca-operator-[0-9.]\+.tgz|download/helm-v${HELM_VERSION_CLEAN}/rca-operator-${HELM_VERSION_CLEAN}.tgz|g" "$INSTALL_DOC"

# Clean up temp files
rm -f "${INSTALL_DOC}.tmp"

echo "✅ Updated $INSTALL_DOC"
echo ""
echo "Changes:"
git diff "$INSTALL_DOC" || true

echo ""
echo "To commit these changes:"
echo "  git add $INSTALL_DOC"
echo "  git commit -m 'docs: update installation versions to $OPERATOR_VERSION and helm-v$HELM_VERSION_CLEAN'"
echo ""
echo "Backup saved at: ${INSTALL_DOC}.bak"
