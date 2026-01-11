#!/bin/bash
set -e

# check for GITHUB_PAT environment variable
if [ -z "$GITHUB_PAT" ]; then
  echo "Error: GITHUB_PAT environment variable is not set"
  echo ""
  echo "Please set your GitHub Personal Access Token:"
  echo "  export GITHUB_PAT=\"ghp_YOUR_GITHUB_TOKEN_HERE\""
  echo ""
  echo "Get a token at: https://github.com/settings/tokens/new"
  echo "Required permissions: read:user"
  exit 1
fi

# validate token format (should start with ghp_)
if [[ ! "$GITHUB_PAT" =~ ^ghp_ ]]; then
  echo "Warning: GITHUB_PAT should start with 'ghp_' (Personal Access Token)"
  echo "Current value starts with: ${GITHUB_PAT:0:4}..."
  echo ""
  read -p "Continue anyway? (y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    exit 1
  fi
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Step 1: Creating ServiceEntry for GitHub MCP API..."
kubectl apply -f "$SCRIPT_DIR/serviceentry.yaml"

echo ""
echo "==> Step 2: Creating DestinationRule..."
kubectl apply -f "$SCRIPT_DIR/destinationrule.yaml"

echo ""
echo "==> Step 3: Creating HTTPRoute..."
kubectl apply -f "$SCRIPT_DIR/httproute.yaml"

echo ""
echo "==> Step 4: Creating Secret with Authentication..."
envsubst < "$SCRIPT_DIR/secret.yaml" | kubectl apply -f -

echo ""
echo "==> Step 5: Creating MCPServer Resource..."
kubectl apply -f "$SCRIPT_DIR/mcpserver.yaml"

echo ""
echo "==> Step 6: Applying AuthPolicy..."
kubectl apply -f "$SCRIPT_DIR/authpolicy.yaml"

echo ""
echo "==> Done! Resources applied successfully."
echo ""
echo "To verify the setup:"
echo "  kubectl get mcpservers -n mcp-test"
echo "  kubectl logs -n mcp-system deployment/mcp-broker-router | grep github"
echo ""
echo "To wait for tool discovery:"
echo "  until kubectl logs -n mcp-system deploy/mcp-broker-router | grep 'Discovered.*tools.*github'; do sleep 5; done"
