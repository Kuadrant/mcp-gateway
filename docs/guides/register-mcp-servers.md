# MCP Server Registration

You must register your MCP servers to be discovered and routed by the MCP Gateway. When a server is registered, the gateway automatically discovers and federates its capabilities (tools, prompts, and resources), applying a prefix to avoid name collisions across multiple servers.

## Overview

MCP Gateway supports federating MCP Tools, MCP Prompts, and MCP Resources.

- **Discovery**: The gateway's broker automatically discovers available tools, prompts, and resources from registered upstream servers.
- **Prefixing**: To prevent collisions between capabilities with the same name on different servers, the gateway applies the `prefix` defined in the `MCPServerRegistration` to each tool and prompt name (e.g., `greet` becomes `myserver_greet`), and to the authority of each `ui://` resource URI (e.g., `ui://widget.html` becomes `ui://myserver_widget.html`).
- **Routing**: When a client calls a tool, requests a prompt, or reads a resource, the gateway identifies the target upstream server by the prefix, strips the prefix, and routes the request to the correct server.

> **Note:** Resource federation only covers `ui://` resources (MCP Apps / SEP-1865). Only servers with a `prefix` set participate; a server registered without one is silently excluded from `resources/list`.

To connect an MCP server to MCP Gateway, you must create an `HTTPRoute` that routes to your MCP server and an `MCPServerRegistration` resource that references the `HTTPRoute`.

## Prerequisites

- You installed and configured the MCP Gateway
- Your Gateway has both the `mcp` and `mcps` listeners configured (see [Configure MCP Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md))
- An MCP server is running in your cluster

## Procedure

## Step 1: Ensure MCPGatewayExtension exists

An MCPGatewayExtension tells the controller which Gateway this MCP Gateway instance serves. Without it, MCPServerRegistration resources will remain in NotReady status.

> **Note:** Only one MCPGatewayExtension is allowed per namespace. The `sectionName` field selects which listener on the Gateway to use, but each namespace can only have one MCPGatewayExtension. If you followed the [quick start](./quick-start.md) or [install guide](./how-to-install-and-configure.md), an MCPGatewayExtension already exists. Check with `kubectl get mcpgatewayextension -A`. If one is already present and Ready, skip to Step 2.

If you need to create one:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
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
    sectionName: mcp  # must match a listener name on the Gateway
EOF
```

If your MCPGatewayExtension is in a different namespace than the Gateway, create a ReferenceGrant first:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-mcp-extension
  namespace: gateway-system  # Gateway's namespace
spec:
  from:
    - group: mcp.kuadrant.io
      kind: MCPGatewayExtension
      namespace: mcp-test  # MCPGatewayExtension's namespace
  to:
    - group: gateway.networking.k8s.io
      kind: Gateway
EOF
```

Wait for it to become ready:

```bash
kubectl wait --for=condition=Ready mcpgatewayextension/mcp-extension -n mcp-test --timeout=60s
```

## Step 2: Create an HTTPRoute

Create an `HTTPRoute` that routes to your MCP server. The hostname must match the wildcard on the `mcps` listener of your Gateway (e.g. `*.mcp.local`). This is an internal-only hostname used for routing through the gateway and does not need to be publicly resolvable.

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-mcp-server-route
  namespace: mcp-test
spec:
  parentRefs:
    - name: mcp-gateway
      namespace: gateway-system
      sectionName: mcps
  hostnames:
    - 'my-mcp-server.mcp.local'  # must match the mcps listener wildcard
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: mcp-test-server1    # Your MCP server service name
          port: 9090                # Your MCP server port
EOF
```

## Step 3: Create MCPServerRegistration Resource

Create an `MCPServerRegistration` resource that references the HTTPRoute:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-mcp-server
  namespace: mcp-test
spec:
  prefix: "myserver_"
  targetRef:
    group: "gateway.networking.k8s.io"
    kind: "HTTPRoute"
    name: "my-mcp-server-route"  # Must match the HTTPRoute name
    namespace: "mcp-test"
EOF
```

> **Note:** An exact-duplicate `prefix` across two registrations sharing a gateway is rejected at registration time. A prefix that's itself a prefix of another (e.g. `app_` and `app_admin_`) is not rejected; resource URI lookups resolve it via longest-prefix match, and the shorter-prefix server's colliding resource becomes unreachable via `resources/read`. Choose prefixes that aren't prefixes of one another.

## Step 4: Verify Registration

Wait for the MCPServerRegistration to become ready (Ready means the config has been written to the gateway config secret; the broker discovers tools asynchronously after that):

```bash
kubectl wait --for=condition=Ready mcpsr/my-mcp-server -n mcp-test --timeout=120s
```

Then check the status:

```bash
kubectl get mcpsr -A
```

The `READY` column should show `True`. For example:

```text
NAMESPACE   NAME            PREFIX      TARGET               PATH   READY   CATEGORY   CREDENTIALS   AGE
mcp-test    my-mcp-server   myserver_   my-mcp-server-route   /mcp   True                             30s
```

To check tool availability, query the broker's `/status` endpoint.

If the status is not Ready, check the MCPServerRegistration conditions for details:

```bash
kubectl describe mcpsr my-mcp-server -n mcp-test
```

## Step 5: Test Tool Discovery

Verify that your MCP server tools are available through the gateway by using the following commands:

```bash
# Step 1: Initialize MCP session and capture session ID
# Use -D to dump headers to a file, then read the session ID
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

# Extract the MCP session ID from response headers
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')

echo "MCP Session ID: $SESSION_ID"

# Step 2: List tools using the session ID
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'
```

You should now see your MCP server tools in the response, prefixed with your configured `prefix` (e.g., `myserver_`).

## Step 6: Test Prompt Discovery

Verify that your MCP server prompts are also available through the gateway:

```bash
# Use the same SESSION_ID from the previous step
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 3, "method": "prompts/list"}'
```

You should see your MCP server prompts in the response, also prefixed with your configured `prefix`.

## Step 7: Getting a Prompt

To retrieve a specific prompt, use the `prompts/get` method with the federated (prefixed) name:

```bash
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{
    "jsonrpc": "2.0",
    "id": 4,
    "method": "prompts/get",
    "params": {
      "name": "myserver_gh_pr_review",
      "arguments": {
        "pr_number": "42"
      }
    }
  }'

# Clean up
rm -f /tmp/mcp_headers
```

You should now see your MCP server tools and prompts in the response, prefixed with your configured `prefix` (e.g., `myserver_`).

## Step 8: Test Resource Discovery

If your MCP server exposes `ui://` resources, verify they're also federated:

```bash
# Use the same SESSION_ID from the previous steps
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 5, "method": "resources/list"}'
```

You should see your MCP server's resources in the response, with the authority of each `ui://` URI prefixed with your configured `prefix` (e.g., `ui://widget.html` becomes `ui://myserver_widget.html`).

> **Note:** Only the first page of each upstream's resources is fetched. If an upstream server errors or times out during `resources/list`, its resources are skipped rather than failing the whole request; other servers' resources still return.

## Step 9: Getting a Resource

To retrieve a specific resource, use the `resources/read` method with the federated (prefixed) URI:

```bash
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{
    "jsonrpc": "2.0",
    "id": 6,
    "method": "resources/read",
    "params": {
      "uri": "ui://myserver_widget.html"
    }
  }'

# Clean up
rm -f /tmp/mcp_headers
```

You should see the resource's contents (`uri`, `mimeType`, and `text` or `blob` depending on whether the resource is text or binary) in the response.

## Disabling a Server

You can temporarily disable a registered server without deleting it. Setting `state: Disabled` disconnects the broker from the upstream server and removes its tools and prompts from the gateway.

```bash
kubectl patch mcpsr my-mcp-server -n mcp-test --type merge -p '{"spec":{"state":"Disabled"}}'
```

The status condition will show `Ready: False` with reason `Disabled`. To re-enable:

```bash
kubectl patch mcpsr my-mcp-server -n mcp-test --type merge -p '{"spec":{"state":"Enabled"}}'
```

The broker reconnects and restores the server's tools and prompts. No other resources need to be recreated.

## Next Steps

After you have MCP servers registered, you can explore advanced features:

- Create focused tool collections with **[Virtual MCP Servers](./virtual-mcp-servers.md)**
- Configure OAuth-based security with **[Authentication](./authentication.md)**
- Set up fine-grained access control with **[Authorization](./authorization.md)**
