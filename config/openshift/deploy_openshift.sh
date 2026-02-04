#!/bin/bash

set -e

MCP_GATEWAY_HELM_VERSION="${MCP_GATEWAY_HELM_VERSION:-0.5.0-rc1}"
MCP_GATEWAY_HOST="${MCP_GATEWAY_HOST:-mcp.apps.$(oc get dns cluster -o jsonpath='{.spec.baseDomain}')}"
MCP_GATEWAY_NAMESPACE="${MCP_GATEWAY_NAMESPACE:-mcp-system}"
GATEWAY_NAMESPACE="${GATEWAY_NAMESPACE:-gateway-system}"

SCRIPT_BASE_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

# Check prerequisites
command -v oc >/dev/null 2>&1 || { echo >&2 "OpenShift CLI is required but not installed. Aborting."; exit 1; }
command -v helm >/dev/null 2>&1 || { echo >&2 "Helm is required but not installed. Aborting."; exit 1; }

# Install Service Mesh Operator
echo "Installing Service Mesh Operator..."
oc apply -k "$SCRIPT_BASE_DIR/kustomize/service-mesh/operator/base"

echo "Waiting for Service Mesh Operator to be ready..."
until kubectl wait crd/istios.sailoperator.io --for condition=established &>/dev/null; do sleep 5; done
until kubectl wait crd/istiocnis.sailoperator.io --for condition=established &>/dev/null; do sleep 5; done

# Install Service Mesh Instance
echo "Installing Service Mesh Instance..."
oc apply -k "$SCRIPT_BASE_DIR/kustomize/service-mesh/instance/base"

# Install Connectivity Link Operator
echo "Installing Connectivity Link Operator..."
oc apply -k "$SCRIPT_BASE_DIR/kustomize/connectivity-link/operator/base"

echo "Waiting for Connectivity Link Operator to be ready..."
until kubectl wait crd/kuadrants.kuadrant.io --for condition=established &>/dev/null; do sleep 5; done

# Install Connectivity Link Instance
echo "Installing Connectivity Link Instance..."
oc apply -k "$SCRIPT_BASE_DIR/kustomize/connectivity-link/instance/base"

# Create gateway namespace
kubectl create ns $GATEWAY_NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

# Install MCP Gateway Controller (cluster-wide, no broker)
echo "Installing MCP Gateway Controller..."
helm upgrade -i mcp-controller oci://ghcr.io/kuadrant/charts/mcp-gateway \
  --version $MCP_GATEWAY_HELM_VERSION \
  --namespace $MCP_GATEWAY_NAMESPACE \
  --create-namespace \
  --set controller.enabled=true \
  --set broker.create=false \
  --set gateway.create=false \
  --set httpRoute.create=false \
  --set mcpGatewayExtension.create=false \
  --set envoyFilter.create=false

# Install MCP Gateway Instance (broker + gateway + routes)
echo "Installing MCP Gateway Instance..."
helm upgrade -i mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
  --version $MCP_GATEWAY_HELM_VERSION \
  --namespace $MCP_GATEWAY_NAMESPACE \
  --set controller.enabled=false \
  --set broker.create=true \
  --set gateway.create=true \
  --set gateway.name=mcp-gateway \
  --set gateway.namespace=$GATEWAY_NAMESPACE \
  --set gateway.publicHost="$MCP_GATEWAY_HOST" \
  --set gateway.internalHostPattern="*.mcp.local" \
  --set httpRoute.create=true \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=$GATEWAY_NAMESPACE \
  --set envoyFilter.create=true \
  --set envoyFilter.name=mcp-gateway

# Create OpenShift Route (still using ingress chart for Route only)
echo "Creating OpenShift Route..."
helm upgrade -i mcp-gateway-ingress "$SCRIPT_BASE_DIR/charts/mcp-gateway-ingress" \
  --namespace $GATEWAY_NAMESPACE \
  --set mcpGateway.host="$MCP_GATEWAY_HOST" \
  --set gateway.name=mcp-gateway \
  --set route.create=true

echo
echo "MCP Gateway deployment completed successfully."
echo "Access the MCP Gateway at: https://$MCP_GATEWAY_HOST/mcp"
echo
