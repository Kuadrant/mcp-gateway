# MCP Server Registration

You must register your MCP servers to be discovered and routed by the MCP Gateway. To connect an MCP server to MCP Gateway, you must create an `HTTPRoute` that routes to your MCP server and an `MCPServerRegistration` resource that references the `HTTPRoute`.

## Prerequisites

- You installed and configured the MCP Gateway
- You configured a gateway and `HTTPRoute` for the MCP Gateway
- An MCP server is running in your cluster

## Procedure

To connect an MCP server to MCP Gateway, you need:
1. An MCPGatewayExtension resource that targets your Gateway
2. A ReferenceGrant if the MCPGatewayExtension is in a different namespace than the Gateway
3. An HTTPRoute that routes to your MCP server
4. An MCPServerRegistration resource that references the HTTPRoute

The MCPGatewayExtension tells the controller which Gateway this MCP Gateway instance serves. Without it, MCPServerRegistration resources will remain in NotReady status.

## Step 1: Create MCPGatewayExtension

First, create an MCPGatewayExtension in the same namespace as your MCP Gateway broker/router deployment. It should target a unique Gateway resource. 


```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-extension
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
EOF
```

Wait for it to become ready:

```bash
kubectl wait --for=condition=Ready mcpgatewayextension/mcp-extension -n mcp-test --timeout=60s
```

If your target Gateway is in a different namespace than your MCPGatewayExtension, you will also need to create a ReferenceGrant:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-mcp-extension
  namespace: gateway-system  # Gateway's namespace
spec:
  from:
    - group: mcp.kagenti.com
      kind: MCPGatewayExtension
      namespace: mcp-test  # MCPGatewayExtension's namespace
  to:
    - group: gateway.networking.k8s.io
      kind: Gateway
EOF
```

Skip the ReferenceGrant if the MCPGatewayExtension is in the same namespace as the Gateway.

Create an `HTTPRoute` that routes to your MCP server:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: mcp-api-key-server-route
  namespace: mcp-test
  labels:
    mcp-server: 'true'
spec:
  parentRefs:
    - name: mcp-gateway
      namespace: gateway-system
  hostnames:
    - 'api-key-server.mcp.local'  # Internal routing hostname
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: mcp-api-key-server  # Your MCP server service name
          port: 9090                # Your MCP server port
EOF
```

### Step 2: Create MCPServerRegistration Resource

Create an `MCPServerRegistration` resource that references the HTTPRoute:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-mcp-server
  namespace: mcp-test
spec:
  toolPrefix: "myserver_"
  targetRef:
    group: "gateway.networking.k8s.io"
    kind: "HTTPRoute"
    name: "mcp-api-key-server-route"  # The name and namespace of your MCP Server HTTPRoute
    namespace: "mcp-test"
EOF
```

### Step 3: Verify Registration

Check that the `MCPServerRegistration` was created and discovered:

```bash
# Check MCPServerRegistration status
kubectl get mcpsr -A

# Check controller logs
kubectl logs -n mcp-system deployment/mcp-gateway-controller

# Check broker logs for tool discovery
kubectl logs -n mcp-system deployment/mcp-gateway | grep "Discovered tools"
```

### Step 4: Test Tool Discovery

Verify that your MCP server tools are available through the gateway by using the following commands:

```bash
# Step 1: Initialize MCP session and capture session ID
# Use -D to dump headers to a file, then read the session ID
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

# Extract the MCP session ID from response headers
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')

echo "MCP Session ID: $SESSION_ID"

# Step 2: List tools using the session ID
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'

# Clean up
rm -f /tmp/mcp_headers
```

You should now see your MCP server tools in the response, prefixed with your configured `toolPrefix` (e.g., `myserver_`).

## Next Steps

After you have MCP servers registered, you can explore advanced features:

- Create focused tool collections with **[Virtual MCP Servers](./virtual-mcp-servers.md)**
- Configure OAuth-based security with **[Authentication](./authentication.md)**
- Set up fine-grained access control with **[Authorization](./authorization.md)**
