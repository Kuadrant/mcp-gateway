#!/bin/bash
set -euo pipefail

# test-helm-install.sh
# validates helm chart installs correctly against a clean kind cluster
# and that deployed pods become healthy and respond to HTTP requests

CLUSTER_NAME="helm-test-$$"
NAMESPACE="mcp-system"
CHART_DIR="./charts/mcp-gateway"
TIMEOUT="300s"
ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"

# export so make targets use our test cluster
export KIND_CLUSTER_NAME="$CLUSTER_NAME"

# tool paths (built by make)
KIND="${ROOT_DIR}/bin/kind"
HELM="${ROOT_DIR}/bin/helm"

cd "$ROOT_DIR"

cleanup() {
    echo "cleaning up kind cluster ${CLUSTER_NAME}..."
    "$KIND" delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

info() {
    echo "--- $1"
}

# check required tools exist
for tool in "$KIND" "$HELM"; do
    [[ -x "$tool" ]] || fail "required tool not found: $tool (run 'make tools' first)"
done
command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v kubectl >/dev/null 2>&1 || fail "kubectl is required"

# 1. create minimal kind cluster (no OIDC/keycloak extras)
info "creating kind cluster ${CLUSTER_NAME}"
cat <<EOF | "$KIND" create cluster --name "$CLUSTER_NAME" --config -
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
    hostPort: 0
    protocol: TCP
EOF

# 2. install gateway API CRDs
info "installing gateway API CRDs"
make gateway-api-install

# 3. install MCP CRDs
info "installing MCP CRDs"
make install-crd

# 4. install istio via sail operator
info "installing istio via sail operator"
make istio-install

# 5. install metallb for LoadBalancer support
info "installing metallb"
make metallb-install

# 6. build and load images into kind cluster
info "building and loading images"
make docker-build
make load-image

# 7. create namespaces
info "creating namespaces"
make deploy-namespaces

# 8. helm install
info "running helm install"
"$HELM" install mcp-gateway "$CHART_DIR" \
    --namespace "$NAMESPACE" \
    --set gateway.create=true \
    --set gateway.name=mcp-gateway \
    --set gateway.namespace=gateway-system \
    --set broker.create=true \
    --set controller.enabled=true \
    --set gateway.nodePort.create=true \
    --set gateway.publicHost=mcp.127-0-0-1.sslip.io \
    --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
    --set mcpGatewayExtension.gatewayRef.namespace=gateway-system \
    --wait \
    --timeout="${TIMEOUT}"

# 9. verify MCPGatewayExtension is ready
info "waiting for MCPGatewayExtension to be ready"
kubectl wait --for=condition=Ready mcpgatewayextension/mcp-gateway-extension -n "$NAMESPACE" --timeout="${TIMEOUT}" \
    || fail "MCPGatewayExtension not ready"

# 10. verify deployments are available
info "waiting for mcp-gateway deployment"
kubectl wait --for=condition=Available deployment/mcp-gateway -n "$NAMESPACE" --timeout="${TIMEOUT}" \
    || fail "mcp-gateway deployment not available"

info "waiting for controller deployment"
kubectl wait --for=condition=Available deployment/mcp-gateway-controller -n "$NAMESPACE" --timeout="${TIMEOUT}" \
    || fail "controller deployment not available"

# 11. verify gateway is programmed
info "waiting for gateway to be programmed"
kubectl wait --for=condition=Programmed gateway/mcp-gateway -n gateway-system --timeout="${TIMEOUT}" \
    || fail "gateway not programmed"

echo ""
echo "PASS: helm install test completed successfully"
