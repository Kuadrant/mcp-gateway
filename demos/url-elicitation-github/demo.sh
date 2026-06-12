#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SAMPLES_DIR="$REPO_ROOT/config/samples/remote-github"
NAMESPACE="mcp-test"
GATEWAY_URL="http://mcp.127-0-0-1.sslip.io:8001/mcp"

output() {
  echo ""
  echo "=============================================================="
  echo "  $1"
  echo "=============================================================="
  echo ""
}

cleanup() {
  output "Cleaning up GitHub MCP resources"
  kubectl delete authpolicy mcp-tokens-oidc -n mcp-system --ignore-not-found
  kubectl delete authpolicy mcp-tokens-oidc-callback -n mcp-system --ignore-not-found
  kubectl delete httproute mcp-tokens-oidc-callback -n mcp-system --ignore-not-found
  kubectl delete authpolicy mcp-oidc-policy -n gateway-system --ignore-not-found
  kubectl delete mcpserverregistration github -n "$NAMESPACE" --ignore-not-found
  kubectl delete secret github-token -n "$NAMESPACE" --ignore-not-found
  kubectl delete httproute github-mcp-external -n "$NAMESPACE" --ignore-not-found
  kubectl delete destinationrule github-mcp-tls -n "$NAMESPACE" --ignore-not-found
  kubectl delete serviceentry github-mcp-api -n "$NAMESPACE" --ignore-not-found
  echo "Done."
}

if [ "${1:-}" = "cleanup" ]; then
  cleanup
  exit 0
fi

# --- Prerequisites ---

output "Checking prerequisites"

if [ -z "$GITHUB_PAT" ]; then
  echo "ERROR: GITHUB_PAT environment variable is not set"
  echo ""
  echo "Set your GitHub Personal Access Token:"
  echo "  export GITHUB_PAT=\"ghp_YOUR_TOKEN\""
  echo ""
  echo "Get a token at: https://github.com/settings/tokens/new"
  echo "Required permissions: read:user"
  exit 1
fi

if [[ ! "$GITHUB_PAT" =~ ^ghp_ ]]; then
  echo "Warning: GITHUB_PAT should start with 'ghp_' (Personal Access Token)"
  echo "Current value starts with: ${GITHUB_PAT:0:4}..."
  read -p "Continue anyway? (y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    exit 1
  fi
fi

if ! command -v kubectl &>/dev/null; then
  echo "ERROR: kubectl is required"
  exit 1
fi

echo "Checking gateway is reachable (MCP initialize)..."
INIT_RESPONSE=$(curl -s -X POST "$GATEWAY_URL" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"elicitation":{}},"clientInfo":{"name":"demo-check","version":"0.1.0"}}}')

if echo "$INIT_RESPONSE" | grep -q '"serverInfo"'; then
  echo "Gateway responded to MCP initialize."
else
  echo "WARNING: Gateway at $GATEWAY_URL did not return a valid MCP initialize response."
  echo "Make sure 'make local-env-setup' has been run."
  echo ""
  echo "Response: $INIT_RESPONSE"
  read -p "Continue anyway? (y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    exit 1
  fi
fi

KEYCLOAK_URL="https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp/.well-known/openid-configuration"
echo "Checking Keycloak is reachable..."
KEYCLOAK_STATUS=$(curl -sk -o /dev/null -w "%{http_code}" "$KEYCLOAK_URL" 2>/dev/null || echo "000")
if [ "$KEYCLOAK_STATUS" = "200" ]; then
  echo "Keycloak is reachable."
else
  echo "ERROR: Keycloak is not reachable at $KEYCLOAK_URL (HTTP $KEYCLOAK_STATUS)"
  echo ""
  echo "Run the following to set up Keycloak and OAuth:"
  echo "  make auth-example-setup-no-vault"
  exit 1
fi

echo "Prerequisites OK."

# --- Clean existing resources ---

output "Step 1: Cleaning existing GitHub MCP resources (if any)"
cleanup 2>/dev/null || true

# --- Deploy networking resources ---

output "Step 2: Creating Istio networking resources"

echo "  ServiceEntry..."
kubectl apply -f "$SAMPLES_DIR/serviceentry.yaml"

echo "  DestinationRule..."
kubectl apply -f "$SAMPLES_DIR/destinationrule.yaml"

echo "  HTTPRoute..."
kubectl apply -f "$SAMPLES_DIR/httproute.yaml"

# --- Deploy secret ---

output "Step 3: Creating broker credential secret"
envsubst < "$SAMPLES_DIR/secret.yaml" | kubectl apply -f -

# --- Enable URL elicitation and disable HTTPRoute management ---

output "Step 4: Enabling URL elicitation"
kubectl patch mcpgatewayextension mcp-gateway-extension -n mcp-system --type=merge \
  -p='{"spec":{"urlElicitation":"Enabled"}}'
echo "Deleting existing HTTPRoute..."
kubectl delete httproute mcp-route -n mcp-system --ignore-not-found
echo "Waiting for rollout..."
kubectl rollout status deployment/mcp-gateway -n mcp-system --timeout=60s

# --- Deploy MCPServerRegistration with tokenURLElicitation ---

output "Step 5: Creating MCPServerRegistration with URL elicitation"
kubectl apply -f "$SCRIPT_DIR/mcpserverregistration.yaml"
echo ""
echo "Note: This MCPServerRegistration has 'tokenURLElicitation: {}' which"
echo "enables the -32042 URL elicitation flow for per-user token collection."

# --- Apply AuthPolicy ---

output "Step 6: Applying AuthPolicy for OIDC authentication"
kubectl apply -f "$SCRIPT_DIR/authpolicy.yaml"
echo "AuthPolicy applied — gateway will require OIDC tokens via Keycloak."

# --- Deploy OIDC auth for /tokens ---

output "Step 7: Applying OIDC browser authentication for /tokens"
kubectl apply -f "$SCRIPT_DIR/callback-httproute.yaml"
kubectl apply -f "$SCRIPT_DIR/tokens-authpolicy.yaml"
kubectl apply -f "$SCRIPT_DIR/callback-authpolicy.yaml"
echo "OIDC auth applied — browser requests to /tokens will redirect to Keycloak login."

# --- Wait for readiness ---

output "Step 8: Waiting for GitHub MCP server to become ready"
echo "This may take up to 2 minutes for tool discovery..."

TIMEOUT=120
ELAPSED=0
while [ $ELAPSED -lt $TIMEOUT ]; do
  STATUS=$(kubectl get mcpserverregistration github -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
  if [ "$STATUS" = "True" ]; then
    TOOLS=$(kubectl get mcpserverregistration github -n "$NAMESPACE" -o jsonpath='{.status.discoveredTools}' 2>/dev/null || echo "0")
    echo "GitHub MCP server is Ready. Discovered $TOOLS tools."
    break
  fi
  echo "  Waiting... ($ELAPSED s)"
  sleep 5
  ELAPSED=$((ELAPSED + 5))
done

if [ "$STATUS" != "True" ]; then
  echo "WARNING: MCPServerRegistration did not become Ready within ${TIMEOUT}s."
  echo "Check status: kubectl get mcpserverregistration github -n $NAMESPACE -o yaml"
  echo "Check logs:   kubectl logs -n mcp-system deployment/mcp-gateway | grep github"
  echo ""
  echo "Continuing anyway — the demo instructions below may not work until ready."
fi

# --- Print demo instructions ---

output "URL Elicitation Demo Ready!"

cat <<'INSTRUCTIONS'
Add the gateway to Claude Code:

  NODE_TLS_REJECT_UNAUTHORIZED=0 claude mcp add mcp-gateway --transport http http://mcp.127-0-0-1.sslip.io:8001/mcp

Or add to your project .mcp.json:

  {
    "mcpServers": {
      "mcp-gateway": {
        "type": "url",
        "url": "http://mcp.127-0-0-1.sslip.io:8001/mcp"
      }
    }
  }

Note: Keycloak uses a self-signed certificate. Start Claude Code
with NODE_TLS_REJECT_UNAUTHORIZED=0 to allow the OAuth flow:

  NODE_TLS_REJECT_UNAUTHORIZED=0 claude

Then in Claude Code:

  1. Start a conversation — Claude discovers tools with the
     github_ prefix from the gateway.

  2. Ask Claude to use a GitHub tool, e.g. "use mcp-gateway github_get_me".

  3. Claude will prompt you to open a URL in your browser —
     this is the gateway's token page.

  4. Open that URL. 

  5. You will be re-directed to keycloak. Login with mcp mcp (this is authenticating you in the browser)

  6. Paste your GitHub PAT and click Submit.

  7. In claude select re-try
     You should see your GitHub user data in the response.

  8. Subsequent GitHub tool calls succeed immediately using
     the cached token (no second prompt).

INSTRUCTIONS

echo "Cleanup:"
echo "  $0 cleanup"
echo ""
