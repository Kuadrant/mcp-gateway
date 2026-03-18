#!/bin/bash

set -e

MCP_GATEWAY_VERSION="${MCP_GATEWAY_VERSION:-0.5.1}"
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

# Install MCP Gateway Controller via OLM
echo "Installing MCP Gateway Controller via OLM..."
kubectl create ns $MCP_GATEWAY_NAMESPACE --dry-run=client -o yaml | kubectl apply -f -

if [ -n "${CATALOG_IMG:-}" ]; then
  sed "s|image: .*|image: ${CATALOG_IMG}|" \
    "$SCRIPT_BASE_DIR/../deploy/olm/catalogsource.yaml" | kubectl apply -n openshift-marketplace -f -
else
  kubectl apply -f "$SCRIPT_BASE_DIR/../deploy/olm/catalogsource.yaml" -n openshift-marketplace
fi

echo "Waiting for CatalogSource to be ready..."
retries=0
until kubectl get catalogsource mcp-gateway-catalog -n openshift-marketplace -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null | grep -q "READY"; do
  retries=$((retries + 1))
  if [ $retries -ge 60 ]; then
    echo "Timed out waiting for CatalogSource to be ready"
    exit 1
  fi
  sleep 5
done

kubectl apply -f "$SCRIPT_BASE_DIR/../deploy/olm/operatorgroup.yaml" -n $MCP_GATEWAY_NAMESPACE

# patch subscription sourceNamespace for OpenShift
sed "s|sourceNamespace: .*|sourceNamespace: openshift-marketplace|" \
  "$SCRIPT_BASE_DIR/../deploy/olm/subscription.yaml" > /tmp/mcp-subscription.yaml
kubectl apply -f /tmp/mcp-subscription.yaml -n $MCP_GATEWAY_NAMESPACE

echo "Waiting for controller CSV to succeed..."
retries=0
until kubectl get csv -n $MCP_GATEWAY_NAMESPACE -l operators.coreos.com/mcp-gateway.$MCP_GATEWAY_NAMESPACE="" -o jsonpath='{.items[0].status.phase}' 2>/dev/null | grep -q "Succeeded"; do
  retries=$((retries + 1))
  if [ $retries -ge 60 ]; then
    echo "Timed out waiting for controller CSV to succeed"
    exit 1
  fi
  sleep 5
done
echo "MCP Gateway Controller installed via OLM"

echo "Waiting for MCP Gateway CRDs to be established..."
until kubectl wait crd/mcpgatewayextensions.mcp.kagenti.com --for condition=established &>/dev/null; do sleep 5; done
until kubectl wait crd/mcpserverregistrations.mcp.kagenti.com --for condition=established &>/dev/null; do sleep 5; done

# Install MCP Gateway Instance (Gateway, ReferenceGrant, MCPGatewayExtension)
echo "Installing MCP Gateway Instance..."
if [ "$MCP_GATEWAY_VERSION" = "local" ]; then
  CHART_REF="$SCRIPT_BASE_DIR/../../charts/mcp-gateway/"
  VERSION_FLAG=""
else
  CHART_REF="oci://ghcr.io/kuadrant/charts/mcp-gateway"
  VERSION_FLAG="--version $MCP_GATEWAY_VERSION"
fi

helm upgrade -i mcp-gateway $CHART_REF \
  $VERSION_FLAG \
  --namespace $MCP_GATEWAY_NAMESPACE \
  --skip-crds \
  --set controller.enabled=false \
  --set gateway.create=true \
  --set gateway.name=mcp-gateway \
  --set gateway.namespace=$GATEWAY_NAMESPACE \
  --set gateway.publicHost="$MCP_GATEWAY_HOST" \
  --set gateway.internalHostPattern="*.mcp.local" \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=$GATEWAY_NAMESPACE \
  --set mcpGatewayExtension.gatewayRef.sectionName=mcp

# Create OpenShift Route
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
