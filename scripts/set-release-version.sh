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

# Update scripts with MCP_GATEWAY_VERSION bash defaults
for SCRIPT in \
    "$REPO_ROOT/scripts/quick-start.sh" \
    "$REPO_ROOT/charts/sample_local_helm_setup.sh"; do
    if [ -f "$SCRIPT" ]; then
        sed -i.bak -E "s/MCP_GATEWAY_VERSION:-[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)*)?/MCP_GATEWAY_VERSION:-$VERSION/" "$SCRIPT"
        rm -f "$SCRIPT.bak"
        echo "Updated: $SCRIPT"
    else
        echo "Warning: $SCRIPT not found"
    fi
done

# Update config/mcp-system deployment images
CONTROLLER_DEPLOY="$REPO_ROOT/config/mcp-system/deployment-controller.yaml"
if [ -f "$CONTROLLER_DEPLOY" ]; then
    sed -i.bak -E "s|image: ghcr.io/kuadrant/mcp-controller:.+|image: ghcr.io/kuadrant/mcp-controller:v$VERSION|" "$CONTROLLER_DEPLOY"
    rm -f "$CONTROLLER_DEPLOY.bak"
    echo "Updated: $CONTROLLER_DEPLOY"
else
    echo "Warning: $CONTROLLER_DEPLOY not found"
fi

BROKER_DEPLOY="$REPO_ROOT/config/mcp-system/deployment-broker.yaml"
if [ -f "$BROKER_DEPLOY" ]; then
    sed -i.bak -E "s|image: ghcr.io/kuadrant/mcp-gateway:.+|image: ghcr.io/kuadrant/mcp-gateway:v$VERSION|" "$BROKER_DEPLOY"
    rm -f "$BROKER_DEPLOY.bak"
    echo "Updated: $BROKER_DEPLOY"
else
    echo "Warning: $BROKER_DEPLOY not found"
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

# Update docs/guides MCP_GATEWAY_VERSION
for GUIDE in \
    "$REPO_ROOT/docs/guides/quick-start.md" \
    "$REPO_ROOT/docs/guides/isolated-gateway-deployment.md" \
    "$REPO_ROOT/docs/guides/how-to-install-and-configure.md"; do
    if [ -f "$GUIDE" ]; then
        sed -i.bak -E "s/MCP_GATEWAY_VERSION=[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)*)?/MCP_GATEWAY_VERSION=$VERSION/" "$GUIDE"
        rm -f "$GUIDE.bak"
        echo "Updated: $GUIDE"
    else
        echo "Warning: $GUIDE not found"
    fi
done

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
echo "After updating, regenerate the bundle with: make bundle VERSION=$VERSION"
echo "Review changes with: git diff"
