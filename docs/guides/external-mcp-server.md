# Connecting to External MCP Servers

This guide demonstrates how to connect MCP Gateway to external MCP servers using Gateway API and Istio. We'll use the public GitHub MCP server as an example.

Clients call your Gateway's hostname, and the Gateway rewrites and routes traffic to the external service.

## Prerequisites

- MCP Gateway installed and configured
- Gateway API Provider (Istio) with ServiceEntry and DestinationRule support
- Network egress access to external MCP server
- Authentication credentials for the external server (if required)
- **MCPGatewayExtension** targeting the Gateway (required for MCPServerRegistration to work)

**Note:** If you're trying this locally, `make local-env-setup` already meets all prerequisites except the GitHub PAT.

If you haven't created an MCPGatewayExtension yet, see [Configure MCP Servers](./register-mcp-servers.md#step-1-create-mcpgatewayextension) for instructions.

## About the GitHub MCP Server

The GitHub MCP server (https://api.githubcopilot.com/mcp/) provides programmatic access to GitHub functionality through the Model Context Protocol. It exposes tools for repository management, issues, pull requests, and code operations.

For this example, you'll need a GitHub Personal Access Token with `read:user` permissions. Get one at https://github.com/settings/tokens/new

```bash
export GITHUB_PAT="ghp_YOUR_GITHUB_TOKEN_HERE"
```

## Quick Start

The fastest way to set up the GitHub MCP server is using the provided script:

```bash
# Set your GitHub PAT
export GITHUB_PAT="ghp_YOUR_GITHUB_TOKEN_HERE"

# Run the setup script
./config/samples/remote-github/create_resources.sh
```

The script will:
- Validate your GITHUB_PAT environment variable and token format
- Create ServiceEntry, DestinationRule, HTTPRoute, Secret, and MCPServerRegistration
- Apply the AuthPolicy for OAuth + API key handling

All the sample YAML files are available in `config/samples/remote-github/` for reference or customization. For a detailed explanation of each component, continue reading the manual setup steps below.

## Overview

To connect to an external MCP server, you need:
1. ServiceEntry to register the external service in Istio
2. DestinationRule for TLS and connection policies
3. HTTPRoute with your hostname that rewrites and routes to the external service
4. MCPServerRegistration resource to register with MCP Gateway
5. Secret with authentication credentials
6. AuthPolicy to handle authentication headers (optional, for OAuth scenarios)

The existing Gateway already has a `*.mcp.local` wildcard listener, so we'll use `github.mcp.local` as our internal hostname.

## Step 1: Create ServiceEntry

The `ServiceEntry` registers the external service in Istio's service registry:

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

## Step 2: Create DestinationRule

Configure TLS settings for the external service:

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

## Step 3: Create HTTPRoute

Create an `HTTPRoute` that matches your internal hostname and routes to the external service using Istio's Hostname backendRef:

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
  - github.mcp.local  # your internal hostname, matches *.mcp.local listener
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /mcp
    filters:
    - type: URLRewrite
      urlRewrite:
        hostname: api.githubcopilot.com  # rewrite to external hostname
    backendRefs:
    - name: api.githubcopilot.com
      kind: Hostname
      group: networking.istio.io
      port: 443
EOF
```

## Step 4: Create Secret with Authentication

Create a secret containing your GitHub PAT token with the Bearer prefix:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: github-token
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/credential: "true"  # required label
type: Opaque
stringData:
  token: "Bearer $GITHUB_PAT"
EOF
```

The `mcp.kuadrant.io/credential=true` label is required. Without it the MCPServerRegistration will fail validation.

## Step 5: Create the MCPServerRegistration Resource

Create the `MCPServer` resource that registers the GitHub MCP server with the gateway:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: github
  namespace: mcp-test
spec:
  toolPrefix: github_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  credentialRef:
    name: github-token
    key: token
EOF
```

## Step 6: Create AuthPolicy (Optional)

If you're using Kuadrant/Authorino for OAuth authentication, create an `AuthPolicy` to handle authorization headers:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcps-auth-policy
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  rules:
    response:
      success:
        headers:
          authorization:
            plain:
              expression: 'request.headers["authorization"]'
          x-mcp-api-key:
            plain:
              expression: 'request.headers["authorization"].split(" ")[1]'
EOF
```

This AuthPolicy extracts the API key from the OAuth token and sets it as the `x-mcp-api-key` header, resolving the OAuth + API Key conflict described in Issue #201.

**Note:** This step is only required if you're using AuthPolicy for OAuth authentication. For simple bearer token auth, the router handles the Authorization header automatically.

## Step 7: Wait for Configuration Sync

Wait for the configuration to sync to the broker:

```bash
echo "Waiting for GitHub tools to be discovered..."
until kubectl logs -n mcp-system deploy/mcp-gateway | grep "Discovered.*tools.*github"; do
  echo "Still waiting..."
  sleep 5
done
echo "GitHub tools discovered!"
```

## Verification

Check that the MCPServerRegistration is registered:

```bash
kubectl get mcpsrs -n mcp-test
kubectl logs -n mcp-system deployment/mcp-gateway | grep "Discovered.*tools.*github"
```

## Test Integration

Test tools/list through the gateway:

```bash
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

To test tool calls, open the MCP Inspector:

```bash
make inspect-gateway
```

In the `Authentication` section, add a HTTP header called `Authorization` with value `Bearer $GITHUB_PAT`.
After connecting to the Gateway, under `Tools->List Tools`, you should see a list of Github tools with
prefix `github_`. If everything works, when you run the tool `github_get_me`, you should see the information
associated with your access token.

## Cleanup

```bash
kubectl delete mcpserver github -n mcp-test
kubectl delete httproute github-mcp-external -n mcp-test
kubectl delete serviceentry github-mcp-external -n mcp-test
kubectl delete destinationrule github-mcp-external -n mcp-test
kubectl delete secret github-token -n mcp-test
kubectl delete authpolicy mcps-auth-policy -n mcp-test --ignore-not-found
```
