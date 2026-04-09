#!/bin/bash
set -e

GITHUB_ORG=${MCP_GATEWAY_ORG:-Kuadrant}
VERSION=${MCP_GATEWAY_VERSION:-0.5.1}
GIT_REF="v${VERSION}"
REPO="https://github.com/${GITHUB_ORG}/mcp-gateway"
RAW="https://raw.githubusercontent.com/${GITHUB_ORG}/mcp-gateway/${GIT_REF}"

output() {
    echo ""
    echo "------------------------------------------------------------"
    echo "$1"
    echo "------------------------------------------------------------"
    sleep 1
}

echo "Checking prerequisites..."
missing=()
for cmd in docker kind helm kubectl; do
    if ! command -v "$cmd" &>/dev/null; then
        missing+=("$cmd")
    else
        echo "  $cmd: $(command -v $cmd)"
    fi
done
# podman is an alternative to docker
if [[ " ${missing[*]} " == *" docker "* ]] && command -v podman &>/dev/null; then
    echo "  podman: $(command -v podman) (docker alternative)"
    filtered=()
    for item in "${missing[@]}"; do
        [ "$item" != "docker" ] && filtered+=("$item")
    done
    missing=("${filtered[@]}")
fi
if [ ${#missing[@]} -gt 0 ]; then
    echo ""
    echo "ERROR: missing required tools: ${missing[*]}"
    echo "Install them before running this script. See:"
    echo "  https://docs.docker.com/engine/install/"
    echo "  https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
    echo "  https://helm.sh/docs/intro/install/"
    echo "  https://kubernetes.io/docs/tasks/tools/"
    exit 1
fi
echo ""

output "Step 1: Create Kind cluster with port mapping (localhost:8001 -> NodePort 30080)"

KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-mcp-gateway}
if kind get clusters 2>/dev/null | grep -Fxq -- "$KIND_CLUSTER_NAME"; then
    echo "Kind cluster already exists, reusing it."
else
    cat <<EOF | kind create cluster --name "${KIND_CLUSTER_NAME}" --config=-
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
    hostPort: 8001
    protocol: TCP
EOF
fi

output "Step 2: Install Gateway API CRDs and Istio (Gateway API provider)"

kubectl apply -k "${REPO}/config/gateway-api?ref=${GIT_REF}"

helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update istio
helm upgrade --install istio-base istio/base -n istio-system --create-namespace --wait
helm upgrade --install istiod istio/istiod -n istio-system --wait

output "Step 3: Create the Gateway and NodePort service"

kubectl apply -k "${REPO}/config/istio/gateway?ref=${GIT_REF}"

output "Step 4: Install MCP Gateway CRDs, controller, and MCPGatewayExtension"

kubectl apply -k "${REPO}/config/crd?ref=${GIT_REF}"
kubectl apply -k "${REPO}/config/mcp-gateway/overlays/mcp-system?ref=${GIT_REF}"

echo "Waiting for controller..."
kubectl wait --for=condition=available --timeout=120s deployment/mcp-gateway-controller -n mcp-system
echo "Waiting for broker-router (deployed automatically by the controller)..."
kubectl wait --for=condition=available --timeout=120s deployment/mcp-gateway -n mcp-system
echo "Waiting for gateway pod..."
kubectl wait --for=condition=ready --timeout=120s pod -l gateway.networking.k8s.io/gateway-name=mcp-gateway -n gateway-system

output "Step 5: Deploy two test MCP servers and register them"

kubectl apply -f "${RAW}/config/test-servers/namespace.yaml"
for server in server1 server2; do
    kubectl apply -f "${RAW}/config/test-servers/${server}-deployment.yaml" -n mcp-test
    kubectl apply -f "${RAW}/config/test-servers/${server}-service.yaml" -n mcp-test
    kubectl apply -f "${RAW}/config/test-servers/${server}-httproute.yaml" -n mcp-test
    kubectl patch deployment "mcp-test-${server}" -n mcp-test --type='json' \
        -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
done

kubectl apply -f "${RAW}/config/samples/mcpserverregistration-test-servers-base.yaml"

echo "Waiting for test server images to be pulled and pods to start (this may take a few minutes)..."
kubectl wait --for=condition=available --timeout=300s deployment/mcp-test-server1 -n mcp-test
kubectl wait --for=condition=available --timeout=300s deployment/mcp-test-server2 -n mcp-test

echo ""
echo "Verifying setup..."
echo ""
kubectl get mcpgatewayextension -n mcp-system
echo ""
kubectl get mcpserverregistration -n mcp-test

echo ""
echo "============================================================"
echo "MCP Gateway is ready!"
echo ""
echo "Gateway URL: http://mcp.127-0-0-1.sslip.io:8001/mcp"
echo ""
echo "To test with MCP Inspector (requires Node.js):"
echo "  DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@latest"
echo ""
echo "  Then connect to: http://mcp.127-0-0-1.sslip.io:8001/mcp"
echo "  Transport: Streamable HTTP"
echo ""
echo "To clean up:"
echo "  kind delete cluster --name ${KIND_CLUSTER_NAME}"
echo "============================================================"
