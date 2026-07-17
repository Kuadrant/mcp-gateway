# Connecting to External MCP Servers

This guide shows how to connect MCP Gateway to an external MCP server so that tools can be discovered and listed by the broker.

## Prerequisites

- MCP Gateway installed and configured
- Gateway API Provider (Istio) with ServiceEntry and DestinationRule support
- Network egress access to external MCP server
- Authentication credentials for the external server (if required)
- **MCPGatewayExtension** targeting the Gateway (required for MCPServerRegistration to work)

If you haven't created an MCPGatewayExtension yet, see [Configure MCP Servers](./register-mcp-servers.md#step-1-create-mcpgatewayextension) for instructions.

## Register an External MCP Server

This section demonstrates how to register an external MCP server behind the gateway. The `credentialRef` provides a static credential used only by the broker to connect to the upstream server and discover available tools. User requests are handled separately using per-user authentication mechanisms.

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

Create a secret with the credential used by the broker. The broker uses this credential to connect to the upstream server and discover tools:

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
apiVersion: mcp.kuadrant.io/v1
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
NAME     PREFIX    TARGET                PATH   READY   CATEGORY   CREDENTIALS    AGE
github   github_   github-mcp-external   /mcp   True               github-token   30s
```

Ready means the config has been written to the gateway config secret. The broker discovers tools asynchronously after that. To check tool availability, query the broker's `/status` endpoint.

### Step 7: Connect an MCP Client

Configure your MCP client to connect to the gateway.

Example:

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://<gateway-url>/mcp"
    }
  }
}
```

After connecting, the broker should list the available tools from the external MCP server.

## Next Steps

- [Configure authentication](./authentication.md)
- [Register additional MCP servers](./register-mcp-servers.md)
- [Manage tool access and revocation](./tool-revocation.md)

---
