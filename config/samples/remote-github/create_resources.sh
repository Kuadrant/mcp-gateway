#!/bin/bash
set -e

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
echo "==> Step 5: Creating MCPServerRegistration Resource..."
kubectl apply -f "$SCRIPT_DIR/mcpserverregistration.yaml"

echo ""
echo "==> Step 6: Applying AuthPolicy..."
kubectl apply -f "$SCRIPT_DIR/authpolicy.yaml"

echo ""
echo "==> Done! Resources applied successfully."
echo ""
echo "To verify the setup:"
echo "  kubectl get mcpserverregistrations -n mcp-test"
echo "  kubectl logs -n mcp-system deployment/mcp-gateway | grep github"
echo ""
echo "To wait for tool discovery:"
echo "  until kubectl logs -n mcp-system deploy/mcp-gateway | grep 'Discovered.*tools.*github'; do sleep 5; done"
