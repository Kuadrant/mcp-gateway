#!/bin/bash

# Updates version references in release-related files
# Usage: ./scripts/set-release-version.sh <version>
# Example: ./scripts/set-release-version.sh 0.5.0
# Example: ./scripts/set-release-version.sh 0.5.0-rc1

set -e

if [ -z "$1" ]; then
    echo "Usage: $0 <version>"
    echo "Example: $0 0.5.0"
    echo "Example: $0 0.5.0-rc1"
    exit 1
fi

VERSION="$1"

# Validate version format (semver with optional pre-release)
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)*)?$ ]]; then
    echo "Error: Version must be in semver format X.Y.Z or X.Y.Z-prerelease"
    echo "Examples: 0.5.0, 0.5.0-rc1, 0.5.0-alpha.1"
    exit 1
fi

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/.." && pwd )"

echo "Setting release version to: $VERSION"

# Update config/openshift/deploy_openshift.sh
OPENSHIFT_SCRIPT="$REPO_ROOT/config/openshift/deploy_openshift.sh"
if [ -f "$OPENSHIFT_SCRIPT" ]; then
    sed -i.bak -E "s/MCP_GATEWAY_VERSION=\"\\\$\{MCP_GATEWAY_VERSION:-[^}]+\}\"/MCP_GATEWAY_VERSION=\"\${MCP_GATEWAY_VERSION:-$VERSION}\"/" "$OPENSHIFT_SCRIPT"
    rm -f "$OPENSHIFT_SCRIPT.bak"
    echo "Updated: $OPENSHIFT_SCRIPT"
else
    echo "Warning: $OPENSHIFT_SCRIPT not found"
fi

# Update charts/sample_local_helm_setup.sh
SAMPLE_SCRIPT="$REPO_ROOT/charts/sample_local_helm_setup.sh"
if [ -f "$SAMPLE_SCRIPT" ]; then
    sed -i.bak -E "s/--version [0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)*)?/--version $VERSION/" "$SAMPLE_SCRIPT"
    rm -f "$SAMPLE_SCRIPT.bak"
    echo "Updated: $SAMPLE_SCRIPT"
else
    echo "Warning: $SAMPLE_SCRIPT not found"
fi

# Update docs/guides/quick-start.md release branch reference
QUICK_START="$REPO_ROOT/docs/guides/quick-start.md"
if [ -f "$QUICK_START" ]; then
    # extract major.minor.patch (strip any pre-release suffix) for branch name
    BRANCH_VERSION=$(echo "$VERSION" | sed -E 's/-.*//')
    sed -i.bak -E "s/MCP_GATEWAY_BRANCH=release-[0-9]+\.[0-9]+\.[0-9]+/MCP_GATEWAY_BRANCH=release-$BRANCH_VERSION/" "$QUICK_START"
    rm -f "$QUICK_START.bak"
    echo "Updated: $QUICK_START"
else
    echo "Warning: $QUICK_START not found"
fi

# Update OLM base CSV containerImage annotation
CSV_BASE="$REPO_ROOT/config/manifests/bases/mcp-gateway.clusterserviceversion.yaml"
if [ -f "$CSV_BASE" ]; then
    sed -i.bak -E "s|containerImage: ghcr.io/kuadrant/mcp-controller:.+|containerImage: ghcr.io/kuadrant/mcp-controller:v$VERSION|" "$CSV_BASE"
    rm -f "$CSV_BASE.bak"
    echo "Updated: $CSV_BASE"
else
    echo "Warning: $CSV_BASE not found"
fi

# Update OLM CatalogSource image tag
CATALOG_SOURCE="$REPO_ROOT/config/deploy/olm/catalogsource.yaml"
if [ -f "$CATALOG_SOURCE" ]; then
    sed -i.bak "s|image: ghcr.io/kuadrant/mcp-controller-catalog:.*|image: ghcr.io/kuadrant/mcp-controller-catalog:v$VERSION|" "$CATALOG_SOURCE"
    rm -f "$CATALOG_SOURCE.bak"
    echo "Updated: $CATALOG_SOURCE"
else
    echo "Warning: $CATALOG_SOURCE not found"
fi

echo "Done. Version set to $VERSION"
echo ""
echo "Files updated:"
echo "  - config/openshift/deploy_openshift.sh"
echo "  - charts/sample_local_helm_setup.sh"
echo "  - docs/guides/quick-start.md"
echo "  - config/manifests/bases/mcp-gateway.clusterserviceversion.yaml"
echo "  - config/deploy/olm/catalogsource.yaml"
echo ""
echo "After updating, regenerate the bundle with: make bundle VERSION=$VERSION"
echo "Review changes with: git diff"
