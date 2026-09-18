#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# accesspolicy-controller quickstart
# Sets up a complete demo environment with Kind, Kuadrant, MCP Gateway,
# and the MCP Inspector.
# ============================================================================

# ── Colors & formatting ─────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
RESET='\033[0m'

STEP=0

step() {
  STEP=$((STEP + 1))
  echo ""
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
  echo -e "${BOLD}${CYAN}  Step ${STEP}: ${1}${RESET}"
  echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
}

info()    { echo -e "  ${DIM}ℹ${RESET}  ${1}"; }
success() { echo -e "  ${GREEN}✔${RESET}  ${1}"; }
warn()    { echo -e "  ${YELLOW}⚠${RESET}  ${1}"; }
fail()    { echo -e "  ${RED}✖${RESET}  ${1}"; exit 1; }

# ── Configuration ────────────────────────────────────────────────────────────
CLUSTER_NAME="accesspolicy-demo"
CONTROLLER_IMG="accesspolicy-controller:demo"
MCP_SERVER_IMG="accesspolicy-mcp-server:demo"
NAMESPACE="quickstart-ns"
GATEWAY_API_VERSION="v1.2.0"
PORT_FORWARD_PORT=8080

# Resolve project root (the directory containing this script's parent)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ─────────────────────────────────────────────────────────────────────────────
# Step 1: Check prerequisites
# ─────────────────────────────────────────────────────────────────────────────
step "Checking prerequisites"

MISSING=()
for cmd in kind kubectl docker go helm npx; do
  if command -v "${cmd}" &>/dev/null; then
    success "${cmd} $(${cmd} version --short 2>/dev/null || ${cmd} version --client 2>/dev/null | head -1 || echo 'found')"
  else
    MISSING+=("${cmd}")
    warn "${cmd} — ${RED}not found${RESET}"
  fi
done

if [[ ${#MISSING[@]} -gt 0 ]]; then
  fail "Missing required tools: ${MISSING[*]}. Please install them and re-run."
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 2: Create Kind cluster
# ─────────────────────────────────────────────────────────────────────────────
step "Creating Kind cluster '${CLUSTER_NAME}'"

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  warn "Cluster '${CLUSTER_NAME}' already exists — skipping creation"
else
  kind create cluster --config "${SCRIPT_DIR}/kind-config.yaml"
  success "Kind cluster '${CLUSTER_NAME}' created"
fi

kubectl config use-context "kind-${CLUSTER_NAME}"
kubectl cluster-info >/dev/null 2>&1
success "kubectl context set to kind-${CLUSTER_NAME}"

# ─────────────────────────────────────────────────────────────────────────────
# Step 3: Install Gateway API CRDs
# ─────────────────────────────────────────────────────────────────────────────
step "Installing Gateway API CRDs (${GATEWAY_API_VERSION})"

for i in 1 2 3 4 5; do
  if kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/experimental-install.yaml" --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  sleep 5
done
success "Gateway API CRDs installed"

# ─────────────────────────────────────────────────────────────────────────────
# Step 4: Install Istio, Kuadrant & MCP Gateway
# ─────────────────────────────────────────────────────────────────────────────
step "Installing Istio, Kuadrant, and MCP Gateway"

# Helper: wait for the API server to settle after heavy CRD installations
wait_for_apiserver() {
  info "Waiting for API server to settle..."
  for attempt in $(seq 1 30); do
    if kubectl get --raw /readyz --context "kind-${CLUSTER_NAME}" &>/dev/null; then
      return 0
    fi
    sleep 2
  done
  warn "API server may still be busy"
}

# --- Istio ---
helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update istio

for i in {1..10}; do
  if helm upgrade --install istio-base istio/base -n istio-system --create-namespace \
    --kube-context "kind-${CLUSTER_NAME}" \
    --disable-openapi-validation \
    --wait --timeout 10m; then
    break
  fi
  warn "Failed to install istio-base, retrying..."
  sleep 15
done
wait_for_apiserver

for i in {1..10}; do
  if helm upgrade --install istiod istio/istiod -n istio-system \
    --set pilot.resources.requests.memory=256Mi \
    --set pilot.resources.requests.cpu=100m \
    --kube-context "kind-${CLUSTER_NAME}" \
    --disable-openapi-validation \
    --wait --timeout 10m; then
    break
  fi
  warn "Failed to install istiod, retrying..."
  sleep 15
done
success "Istio installed"
wait_for_apiserver

# --- Kuadrant ---
helm repo add kuadrant https://kuadrant.io/helm-charts/ 2>/dev/null || true
helm repo update kuadrant

if helm status kuadrant --namespace kuadrant-system --kube-context "kind-${CLUSTER_NAME}" &>/dev/null; then
  warn "Kuadrant already installed — skipping"
else
  for i in {1..10}; do
    if helm upgrade --install kuadrant kuadrant/kuadrant-operator \
      --namespace kuadrant-system \
      --create-namespace \
      --kube-context "kind-${CLUSTER_NAME}" \
      --disable-openapi-validation \
      --wait --timeout 10m; then
      break
    fi
    warn "Failed to install kuadrant, retrying..."
    sleep 15
  done
  wait_for_apiserver

  for attempt in $(seq 1 10); do
    if cat <<EOF | kubectl apply --context "kind-${CLUSTER_NAME}" -f -
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
EOF
    then break; fi
    sleep 5
  done
  success "Kuadrant operator installed"
fi
wait_for_apiserver

# --- MCP Gateway ---
for i in {1..10}; do
  if kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/crd?ref=v0.7.1' --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  warn "Failed to apply MCP Gateway CRDs, retrying..."
  sleep 10
done
wait_for_apiserver

# Determine platform matching host architecture to avoid kind load manifest matching errors
ARCH=$(uname -m)
PLATFORM="linux/amd64"
if [[ "${ARCH}" == "arm64" ]]; then
  PLATFORM="linux/arm64"
fi

# Pre-load MCP Gateway images so they don't wait for pulls
info "Pre-loading MCP Gateway images (${PLATFORM})..."
for img in ghcr.io/kuadrant/mcp-controller:v0.7.1 ghcr.io/kuadrant/mcp-gateway:v0.7.1; do
  docker pull --platform "${PLATFORM}" "${img}" 2>/dev/null || true
  kind load docker-image "${img}" --name "${CLUSTER_NAME}" 2>/dev/null || true
done

for i in {1..10}; do
  if kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=v0.7.1' --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  warn "Failed to apply MCP gateway config, retrying..."
  sleep 10
done

# Fix upstream RBAC bug in mcp-gateway early preview
for i in 1 2 3 4 5; do
  if kubectl patch clusterrole mcp-controller --type='json' -p='[
    {"op": "add", "path": "/rules/-", "value": {"apiGroups": ["apps"], "resources": ["deployments"], "verbs": ["get", "list", "watch", "create", "update", "patch", "delete"]}},
    {"op": "add", "path": "/rules/-", "value": {"apiGroups": [""], "resources": ["namespaces"], "verbs": ["get"]}},
    {"op": "add", "path": "/rules/-", "value": {"apiGroups": ["gateway.networking.k8s.io"], "resources": ["httproutes"], "verbs": ["get", "list", "watch", "create", "update", "patch", "delete"]}},
    {"op": "add", "path": "/rules/-", "value": {"apiGroups": ["gateway.networking.k8s.io"], "resources": ["gateways", "gateways/status"], "verbs": ["get", "list", "watch", "update", "patch"]}}
  ]' --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  sleep 5
done

# Fix upstream signing key bug in mcp-broker-router standalone deployment
for i in 1 2 3 4 5; do
  if kubectl patch deployment mcp-broker-router -n mcp-system --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/env", "value": [{"name": "GATEWAY_SIGNING_KEY", "valueFrom": {"secretKeyRef": {"name": "mcp-gateway-session-signing-key", "key": "key"}}}]}]' --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  sleep 5
done
success "MCP Gateway components installed and patched"

# ─────────────────────────────────────────────────────────────────────────────
# Step 5: Build & load controller image
# ─────────────────────────────────────────────────────────────────────────────
step "Building controller image (${CONTROLLER_IMG})"

cd "${PROJECT_ROOT}"
make docker-build IMG="${CONTROLLER_IMG}"
kind load docker-image "${CONTROLLER_IMG}" --name "${CLUSTER_NAME}"
success "Controller image built and loaded into Kind"

# ─────────────────────────────────────────────────────────────────────────────
# Step 6: Install XAccessPolicy CRDs
# ─────────────────────────────────────────────────────────────────────────────
step "Installing XAccessPolicy CRDs"

cd "${PROJECT_ROOT}"
for i in 1 2 3 4 5; do
  if make install; then
    break
  fi
  sleep 5
done
success "XAccessPolicy CRDs installed"

# ─────────────────────────────────────────────────────────────────────────────
# Step 7: Deploy the controller
# ─────────────────────────────────────────────────────────────────────────────
step "Deploying accesspolicy-controller"

cd "${PROJECT_ROOT}"
for i in 1 2 3 4 5; do
  if make deploy IMG="${CONTROLLER_IMG}"; then
    break
  fi
  sleep 5
done
info "Waiting for controller deployment to be ready..."
kubectl rollout status deployment/accesspolicy-controller-manager \
  -n accesspolicy-system \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=120s
success "Controller deployed and ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 8: Create quickstart namespace & pre-load Istio sidecar
# ─────────────────────────────────────────────────────────────────────────────
step "Creating namespace '${NAMESPACE}' and pre-loading Istio sidecar"

kubectl create namespace "${NAMESPACE}" --context "kind-${CLUSTER_NAME}" --dry-run=client -o yaml \
  | kubectl apply -f - --context "kind-${CLUSTER_NAME}"
kubectl label namespace "${NAMESPACE}" istio-injection=enabled --overwrite --context "kind-${CLUSTER_NAME}"

# Pre-load the Istio sidecar image so pods with injection don't wait for pulls
ISTIOD_TAG=$(kubectl get deployment istiod -n istio-system --context "kind-${CLUSTER_NAME}" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "")
if [[ -n "${ISTIOD_TAG}" ]]; then
  PROXY_IMG=$(echo "${ISTIOD_TAG}" | sed -e 's/pilot/proxyv2/g')  # istiod uses pilot, sidecars use proxyv2
  info "Pre-loading Istio sidecar image: ${PROXY_IMG}"
  docker pull --platform "${PLATFORM}" "${PROXY_IMG}" 2>/dev/null || true
  kind load docker-image "${PROXY_IMG}" --name "${CLUSTER_NAME}" 2>/dev/null || true
fi
success "Namespace '${NAMESPACE}' ready with Istio injection"

# ─────────────────────────────────────────────────────────────────────────────
# Step 9: Build & load MCP server image
# ─────────────────────────────────────────────────────────────────────────────
step "Building MCP server image (${MCP_SERVER_IMG})"

docker build -t "${MCP_SERVER_IMG}" "${SCRIPT_DIR}/mcpserver"
kind load docker-image "${MCP_SERVER_IMG}" --name "${CLUSTER_NAME}"
success "MCP server image built and loaded into Kind"

# ─────────────────────────────────────────────────────────────────────────────
# Step 10: Deploy MCP server
# ─────────────────────────────────────────────────────────────────────────────
step "Deploying MCP server"

for i in 1 2 3 4 5; do
  if kubectl apply -f "${SCRIPT_DIR}/mcpserver/deployment.yaml" --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  sleep 5
done
info "Waiting for MCP server deployment to be ready..."
kubectl rollout status deployment/mcp-server \
  -n "${NAMESPACE}" \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=300s
success "MCP server deployed and ready"

# ─────────────────────────────────────────────────────────────────────────────
# Step 11: Apply policy resources (Gateway + HTTPRoute + XAccessPolicy)
# ─────────────────────────────────────────────────────────────────────────────
step "Applying Gateway & XAccessPolicy"

for i in 1 2 3 4 5; do
  if kubectl apply -f "${SCRIPT_DIR}/policy/resources.yaml" --context "kind-${CLUSTER_NAME}"; then
    break
  fi
  sleep 5
done
success "Gateway, HTTPRoute, and XAccessPolicy applied"

info "Waiting for MCP Gateway extension and broker/router to be ready..."
kubectl wait --for=condition=Ready --timeout=120s mcpgatewayextension/mcp-gateway-extension \
  -n mcp-system \
  --context "kind-${CLUSTER_NAME}"
kubectl rollout status deployment/mcp-broker-router \
  -n mcp-system \
  --context "kind-${CLUSTER_NAME}" \
  --timeout=120s
success "MCP Gateway extension ready"

info "Waiting for XAccessPolicy to be accepted..."
sleep 3
kubectl get xaccesspolicies -n "${NAMESPACE}" --context "kind-${CLUSTER_NAME}" || true

# ─────────────────────────────────────────────────────────────────────────────
# Step 12: Port-forward Gateway service
# ─────────────────────────────────────────────────────────────────────────────
step "Setting up port-forward (localhost:${PORT_FORWARD_PORT} → gateway:8080)"

# Kill any existing port-forward on the same port
lsof -ti ":${PORT_FORWARD_PORT}" 2>/dev/null | xargs -r kill 2>/dev/null || true

# Wait until the istio gateway deployment is ready
info "Waiting for the istio gateway deployment to be ready..."
kubectl wait --for=condition=available --timeout=120s deployment/demo-gateway-istio -n quickstart-ns || true

# Kill any existing port-forwards and proxies
pkill -f "kubectl port-forward svc/demo-gateway-istio" || true
pkill -f "node ${SCRIPT_DIR}/proxy.js" || true

# Start port-forward on port 8081 in background
kubectl port-forward svc/demo-gateway-istio 8081:8080 -n quickstart-ns >/dev/null 2>&1 &
PF_PID=$!
sleep 2

if kill -0 "${PF_PID}" 2>/dev/null; then
  success "Port-forward active (PID ${PF_PID}) on port 8081"
else
  warn "Port-forward may have failed — check manually"
fi

# Start the header-injecting proxy on port 8080
node "${SCRIPT_DIR}/proxy.js" >/dev/null 2>&1 &
PROXY_PID=$!
sleep 1

if kill -0 "${PROXY_PID}" 2>/dev/null; then
  success "MCP Proxy active (PID ${PROXY_PID}) on port 8080"
else
  warn "Proxy may have failed — check manually"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Step 13: Print instructions
# ─────────────────────────────────────────────────────────────────────────────
step "Quickstart ready!"

echo ""
echo -e "${GREEN}${BOLD}  ✅  accesspolicy-controller demo is running!${RESET}"
echo ""
echo -e "  To visualize the policy enforcement, run the official MCP Inspector in a new terminal:"
echo -e "  ${CYAN}npx -y @modelcontextprotocol/inspector --transport sse --server-url http://localhost:${PORT_FORWARD_PORT}/sse${RESET}"
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo -e "${BOLD}  Try calling tools from the Inspector UI:${RESET}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo ""
printf "  ${BOLD}%-20s %-15s${RESET}\n" "Tool" "Expected"
printf "  ${DIM}%-20s %-15s${RESET}\n" "────────────────────" "───────────────"
printf "  %-20s ${GREEN}%-15s${RESET}\n" "get-sum" "✅ Allowed"
printf "  %-20s ${GREEN}%-15s${RESET}\n" "echo" "✅ Allowed"
printf "  %-20s ${RED}%-15s${RESET}\n" "get-tiny-image" "❌ Blocked"
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo -e "  ${BOLD}Dynamic policy update:${RESET}"
echo -e "  ${CYAN}kubectl apply -f quickstart/policy/updated-policy.yaml${RESET}"
echo ""
echo -e "  This swaps ${YELLOW}echo${RESET} → ${YELLOW}get-tiny-image${RESET} in the allow list."
echo -e "  After applying, you can retry in the MCP Inspector immediately without reconnecting:"
echo -e "  ${GREEN}get-tiny-image${RESET} will be ${GREEN}✅ Allowed${RESET}"
echo -e "  ${RED}echo${RESET} will be ${RED}❌ Blocked${RESET}."
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${RESET}"
echo ""
echo -e "  ${DIM}Cleanup: make quickstart-clean${RESET}"
echo -e "  ${DIM}Logs:    kubectl logs -n accesspolicy-system deployment/accesspolicy-controller-manager -c manager -f${RESET}"
echo ""
