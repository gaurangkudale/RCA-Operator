#!/usr/bin/env bash
# Update helm/Chart.yaml version fields
# Usage: ./scripts/update-chart-version.sh <chart-version> [app-version]
# Example: ./scripts/update-chart-version.sh 0.1.3 v0.1.3

set -euo pipefail

CHART_VERSION="${1:-}"
APP_VERSION="${2:-}"
CHART_FILE="helm/Chart.yaml"

if [ -z "$CHART_VERSION" ]; then
    echo "Usage: $0 <chart-version> [app-version]"
    echo "Example: $0 0.1.3 v0.1.3"
    exit 1
fi

if [ -z "$APP_VERSION" ]; then
    APP_VERSION="$CHART_VERSION"
fi

if [ ! -f "$CHART_FILE" ]; then
    echo "Error: $CHART_FILE not found"
    exit 1
fi

sed -i.tmp -E "s|^version:[[:space:]]*.*$|version: ${CHART_VERSION}|" "$CHART_FILE"
sed -i.tmp -E "s|^appVersion:[[:space:]]*.*$|appVersion: \"${APP_VERSION}\"|" "$CHART_FILE"
rm -f "${CHART_FILE}.tmp"

echo "Updated ${CHART_FILE}:"
grep -E '^version:|^appVersion:' "$CHART_FILE"
