# Connecting to External MCP Servers

This guide walks through connecting MCP Gateway to an external MCP server. We use the GitHub MCP server as an example, but the pattern applies to any external service.

The guide has two parts:

1. **Basic setup** — connect to the external server so the broker can discover and list its tools
2. **Per-user authentication** — add OIDC and let each user supply their own upstream credential for tool calls via a custom header

## Prerequisites

- MCP Gateway installed and configured
- Gateway API Provider (Istio) with ServiceEntry and DestinationRule support
- Network egress access to external MCP server
- Authentication credentials for the external server (if required)
- **MCPGatewayExtension** targeting the Gateway (required for MCPServerRegistration to work)

**Note:** If you're trying this locally, `make local-env-setup` or `make local-env-setup-olm` meets all prerequisites except the GitHub PAT. The optional AuthPolicy step (Step 6) additionally requires Kuadrant (`make auth-example-setup`).

If you haven't created an MCPGatewayExtension yet, see [Configure MCP Servers](./register-mcp-servers.md#step-1-create-mcpgatewayextension) for instructions.

## About the GitHub MCP Server

The GitHub MCP server (https://api.githubcopilot.com/mcp/) provides programmatic access to GitHub functionality through the Model Context Protocol. It exposes tools for repository management, issues, pull requests, and code operations.

For this example, you'll need a GitHub Personal Access Token with `read:user` permissions. Get one at https://github.com/settings/tokens/new

```bash
export GITHUB_PAT="ghp_YOUR_GITHUB_TOKEN_HERE"
```

## Local Environment

If you have the repository checked out, you can set up a complete local environment with Keycloak, Kuadrant, and an example external MCP server:

```bash
make local-env-setup      # Kind cluster with gateway
make auth-example-setup   # Only needed for Auth step. Sets up Keycloak + Kuadrant + AuthPolicy prerequisites
```

`auth-example-setup` creates its own AuthPolicies (`mcp-auth-policy` and `mcps-auth-policy` in `gateway-system`). If you ran that command, remove the existing AuthPolicies before following this guide:

```bash
kubectl delete authpolicy mcp-auth-policy -n gateway-system --ignore-not-found
kubectl delete authpolicy mcps-auth-policy -n gateway-system --ignore-not-found
```

---

## Part 1: Basic External MCP Server

This section registers the GitHub MCP server behind the gateway. The `credentialRef` provides a static credential used only by the broker to connect to the upstream server and discover available tools. User requests are handled separately — see Part 2 for per-user credentials.

### Step 1: Create ServiceEntry

Register the external service in Istio's service registry:

```bash
kubectl apply -f - <<EOF
apiVersion: networking.istio.io/v1beta1
kind: ServiceEntry
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  hosts:
  - api.githubcopilot.com
  ports:
  - number: 443
    name: https
    protocol: HTTPS
  location: MESH_EXTERNAL
  resolution: DNS
EOF
```

### Step 2: Create DestinationRule

Configure TLS for the external service:

```bash
kubectl apply -f - <<EOF
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  host: api.githubcopilot.com
  trafficPolicy:
    tls:
      mode: SIMPLE
      sni: api.githubcopilot.com
EOF
```

### Step 3: Create HTTPRoute

Route traffic from your internal hostname to the external service:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  parentRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
  hostnames:
  - github.mcp.local
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /mcp
    filters:
    - type: URLRewrite
      urlRewrite:
        hostname: api.githubcopilot.com
    backendRefs:
    - name: api.githubcopilot.com
      kind: Hostname
      group: networking.istio.io
      port: 443
EOF
```

The Gateway's `*.mcp.local` wildcard listener matches `github.mcp.local`. The URLRewrite filter rewrites the host header to the external service.

### Step 4: Create Secret

Create a secret with your GitHub PAT. The broker uses this credential to connect to the upstream server and discover tools:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: github-token
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/secret: "true"
type: Opaque
stringData:
  token: "Bearer $GITHUB_PAT"
EOF
```

> **Note:** The `mcp.kuadrant.io/secret=true` label is required. Without it the MCPServerRegistration will fail validation.

### Step 5: Create MCPServerRegistration

Register the GitHub MCP server with the gateway:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: github
  namespace: mcp-test
spec:
  prefix: github_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  credentialRef:
    name: github-token
    key: token
EOF
```

### Step 6: Verify

Wait for the registration to become ready:

```bash
kubectl get mcpsr -n mcp-test
```

Expected output:

```text
NAME     PREFIX    TARGET                PATH   READY   TOOLS   CREDENTIALS    AGE
github   github_   github-mcp-external   /mcp   True    41      github-token   30s
```

At this point the broker has discovered the GitHub tools and will list them to clients.

### Step 7: Connect an MCP Client


> **Note:** You may need to open Keycloak in your browser and accept the self-signed certificate if doing this locally.
> If you are using a Claude session, you may need to start it with `NODE_TLS_REJECT_UNAUTHORIZED=0 claude` if doing this locally.

Configure your MCP client to connect to the gateway. For Claude Code, add to `.claude.json`:

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://mcp.127-0-0-1.sslip.io:8001/mcp"
    }
  }
}
```

After connecting and authenticating with Keycloak (credentials: `mcp`/`mcp`), you should see the GitHub tools listed. However, calling any tool will fail with an authentication error because no user credential is being sent to the upstream GitHub server. Part 2 addresses this.

---

## Part 2: Per-User Authentication with x-github-pat

Part 1 gives the broker access for tool discovery. For users to make actual tool calls, each user supplies their own GitHub PAT via the `x-github-pat` header. This section adds:

- OIDC authentication on the gateway (validates user identity)
- A token replacement policy on the external server route (swaps the `x-github-pat` header value into the `Authorization` header sent upstream)

### Additional Prerequisites

- OIDC provider configured — see [Authentication](./authentication.md) (Steps 1-2)
- Kuadrant with Authorino installed — see [Authentication](./authentication.md) (Step 3, Kuadrant install only)

### Step 8: Create AuthPolicy for the Gateway

Apply an AuthPolicy to the gateway that validates OIDC tokens on all MCP requests while allowing unauthenticated access to discovery endpoints:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  when:
    - predicate: "!request.path.contains('/.well-known')"
  rules:
    authentication:
      'keycloak':
        jwt:
          issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
    response:
      unauthenticated:
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "Access denied: Authentication required."
            }
      unauthorized:
        code: 401
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "Access denied: Unsupported method. New authentication required (401)."
            }
EOF
```

Replace the `issuerUrl` with your OIDC provider's issuer URL unless you are using the local setup environment.

### Step 9: Create AuthPolicy for the External Server Route

Apply an AuthPolicy to the GitHub HTTPRoute that validates the `x-github-pat` header and replaces the `Authorization` header with the user's PAT:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: github-token-replacement-policy
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  rules:
    authorization:
      "has-github-pat":
        patternMatching:
          patterns:
          - predicate: |
              request.headers.exists(h, h == "x-github-pat") && request.headers["x-github-pat"] != ""
    response:
      unauthorized:
        code: 400
        body:
          value: |
            {
              "error": "Bad Request",
              "message": "Missing required x-github-pat header"
            }
      success:
        headers:
          "authorization":
            plain:
              expression: |
                "Bearer " + request.headers["x-github-pat"]
EOF
```

Requests without `x-github-pat` get a 400 response. On success, the PAT replaces the `Authorization` header so the upstream server receives the user's own credential.

### Step 10: Update Your MCP Client

Update your MCP client configuration from Step 7 to include the `x-github-pat` header. For Claude Code, update `.claude.json`:

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://mcp.127-0-0-1.sslip.io:8001/mcp",
      "headers": {
        "x-github-pat": "<your-github-pat>"
      }
    }
  }
}
```

Claude Code handles the OIDC login flow automatically. When the gateway returns a 401 with `WWW-Authenticate`, Claude Code performs OAuth discovery, redirects to the OIDC provider for authentication, and attaches the resulting access token to subsequent requests. The `x-github-pat` header is sent alongside the OIDC token on every request.

### Step 11: Verify

Check that both AuthPolicies are accepted:

```bash
kubectl get authpolicy mcp-auth-policy -n gateway-system
kubectl get authpolicy github-token-replacement-policy -n mcp-test
```

Connect with your MCP client. You should see GitHub tools prefixed with `github_`. Calling the `github_get_me` tool via the configured mcp should return the GitHub user associated with your PAT.

## Cleanup

```bash
# Part 1 resources
kubectl delete mcpserverregistration github -n mcp-test
kubectl delete httproute github-mcp-external -n mcp-test
kubectl delete serviceentry github-mcp-external -n mcp-test
kubectl delete destinationrule github-mcp-external -n mcp-test
kubectl delete secret github-token -n mcp-test

# Part 2 resources (if applied)
kubectl delete authpolicy mcp-auth-policy -n gateway-system --ignore-not-found
kubectl delete authpolicy github-token-replacement-policy -n mcp-test --ignore-not-found
```
