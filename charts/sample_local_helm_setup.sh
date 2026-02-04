#!/bin/bash

# Sample local Helm setup script for MCP Gateway
# This script sets up a complete MCP Gateway environment using remote resources

set -e

# Allow specifying a different GitHub org/user and branch via environment variables
GITHUB_ORG=${MCP_GATEWAY_ORG:-Kuadrant}
BRANCH=${MCP_GATEWAY_BRANCH:-main}
USE_LOCAL_CHART=${USE_LOCAL_CHART:-false}
echo "Using GitHub org: $GITHUB_ORG"
echo "Using branch: $BRANCH"
echo "Using local chart: $USE_LOCAL_CHART"

echo "Setting up MCP Gateway using Helm chart..."

# Create Kind cluster with inline configuration
echo "Creating Kind cluster..."
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 30080
    hostPort: 7001
    protocol: TCP
  - containerPort: 30089
    hostPort: 7002
    protocol: TCP
EOF

kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml

helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update
helm install istio-base istio/base -n istio-system --create-namespace --wait
helm install istiod istio/istiod -n istio-system --wait

# Create gateway namespace for the Gateway resource
kubectl create namespace gateway-system --dry-run=client -o yaml | kubectl apply -f -

# Deploy test servers
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/namespace.yaml
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server1-deployment.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server1-service.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server1-httproute.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server1-httproute-ext.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server2-deployment.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server2-service.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server2-httproute.yaml -n mcp-test
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/test-servers/server2-httproute-ext.yaml -n mcp-test

# Install MCP Gateway using either local chart or remote OCI chart
# The chart creates: Gateway, HTTPRoute, NodePort service, Broker, Controller, EnvoyFilter
if [ "$USE_LOCAL_CHART" = "true" ]; then
    echo "Installing from local chart: ./charts/mcp-gateway/"
    helm install mcp-gateway ./charts/mcp-gateway/ \
        --create-namespace \
        --namespace mcp-system \
        --set broker.create=true \
        --set gateway.create=true \
        --set gateway.name=mcp-gateway \
        --set gateway.namespace=gateway-system \
        --set gateway.publicHost=mcp.127-0-0-1.sslip.io \
        --set gateway.nodePort.create=true \
        --set gateway.nodePort.mcpPort=30080 \
        --set envoyFilter.name=mcp-gateway \
        --set httpRoute.create=true \
        --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
        --set mcpGatewayExtension.gatewayRef.namespace=gateway-system
else
    echo "Installing from remote OCI chart: oci://ghcr.io/kuadrant/charts/mcp-gateway"
    helm install mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
        --create-namespace \
        --namespace mcp-system \
        --version 0.5.0-rc1 \
        --set broker.create=true \
        --set gateway.create=true \
        --set gateway.name=mcp-gateway \
        --set gateway.namespace=gateway-system \
        --set gateway.publicHost=mcp.127-0-0-1.sslip.io \
        --set gateway.nodePort.create=true \
        --set gateway.nodePort.mcpPort=30080 \
        --set envoyFilter.name=mcp-gateway \        
        --set httpRoute.create=true \
        --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
        --set mcpGatewayExtension.gatewayRef.namespace=gateway-system
fi

# Apply MCPServerRegistration samples
kubectl apply -f https://raw.githubusercontent.com/$GITHUB_ORG/mcp-gateway/$BRANCH/config/samples/mcpserverregistration-test-servers-base.yaml

echo "Waiting for MCP Gateway pods to be ready..."
kubectl wait --for=condition=available --timeout=300s deployment/mcp-gateway-broker-router -n mcp-system
kubectl wait --for=condition=available --timeout=300s deployment/mcp-gateway-controller -n mcp-system

echo "Waiting for Istio gateway pod to be ready..."
kubectl wait --for=condition=ready --timeout=300s pod -l gateway.networking.k8s.io/gateway-name=mcp-gateway -n gateway-system

echo "Starting MCP inspector..."
MCP_AUTO_OPEN_ENABLED=false DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@latest &
INSPECTOR_PID=$!

sleep 3

echo "================================================================"
echo "Setup complete!"
echo "================================================================"
echo "MCP Inspector: http://localhost:6274"
echo "Gateway URL: http://mcp.127-0-0-1.sslip.io:7001/mcp"
echo ""
echo "Check status:"
echo "  kubectl get pods -n mcp-system"
echo "  kubectl get pods -n gateway-system"
echo "  kubectl get gateway -n gateway-system"
echo "  kubectl get httproute -n mcp-system"
echo ""
echo "Press Ctrl+C to stop and cleanup."
echo "================================================================"

open "http://localhost:6274/?transport=streamable-http&serverUrl=http://mcp.127-0-0-1.sslip.io:7001/mcp" 2>/dev/null || echo "Open manually: http://localhost:6274/?transport=streamable-http&serverUrl=http://mcp.127-0-0-1.sslip.io:7001/mcp"

# Cleanup function
cleanup() {
    echo "Cleaning up..."
    kill $INSPECTOR_PID 2>/dev/null || true
    exit 0
}

trap cleanup SIGINT SIGTERM
wait
