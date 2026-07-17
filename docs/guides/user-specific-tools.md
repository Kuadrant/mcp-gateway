# User-Specific Tool Lists

Some MCP servers return different tools depending on who is calling. For example, a GitHub MCP server might expose `create_issue` only to users with write access. By default, the gateway caches tools using a service account credential, so per-user tools are invisible.

When `userSpecificList` is enabled on an MCPServerRegistration, the broker fetches tools live during each `tools/list` request using the caller's own headers instead of the cached service account list. The results are merged with cached tools from standard servers before any filters (JWT, virtual server) are applied.

## When to use

Enable `userSpecificList` when:

- The upstream MCP server scopes tools by user credential (e.g. OAuth token, API key)
- The service account used by `credentialRef` does not have access to all tools
- The server dynamically generates tools per user

Do **not** enable it for servers that return the same tools for every caller. Each `tools/list` adds a round-trip to the upstream per userSpecificList server.

## Prerequisites

- MCP Gateway installed and configured
- An MCPServerRegistration with a `prefix` set (required when `userSpecificList` is enabled)
- **User authentication configured** so that the caller's identity headers (e.g. `Authorization`) reach the gateway. Without auth, the broker has no user credentials to forward. See [Authentication](./authentication.md) for setup options (AuthPolicy, OIDC, API key).

## Step 1: Create the MCPServerRegistration

Create an MCPServerRegistration with `userSpecificList: Enabled`. A `prefix` is required so the gateway can route `tools/call` requests back to the correct server.

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-user-scoped-server
  namespace: mcp-system
spec:
  targetRef:
    name: my-server-route
  prefix: myserver
  userSpecificList: Enabled
EOF
```

Verify the registration is ready:

```bash
kubectl get mcpsr my-user-scoped-server -n mcp-system
```

## Step 2: Verify per-user tool discovery

Send a `tools/list` request with user credentials. The response should include tools from the userSpecificList server (prefixed) merged with tools from standard servers.


A different user with different upstream permissions should see a different set of tools from the same server.

## How it works

On the first `tools/list` for a given gateway session, the broker:

1. Creates a short-lived MCP client with the user's headers
2. Initializes the upstream server (MCP handshake)
3. Fetches the user's tool list
4. Caches the upstream session ID for reuse

On subsequent `tools/list` requests in the same session, the broker reuses the cached upstream session, skipping the initialize round-trip. If the upstream session has expired, the broker retries with a fresh initialize automatically.

## Interaction with filters

User-specific tools go through the same filter pipeline as cached tools:

1. **Fetch**: user-specific tools fetched and merged into the result
2. **JWT filter** (`x-mcp-authorized`): filters the merged list by user permissions
3. **Virtual server filter**: scopes to the tools defined in the MCPVirtualServer

You can combine `userSpecificList` with virtual servers and JWT-based authorization. For example, a virtual server can include only specific tools from a userSpecificList server.

## Graceful degradation

If a userSpecificList server is unreachable, the broker logs the error and skips that server. Tools from all healthy servers (both standard and other userSpecificList servers) are still returned. The `tools/list` response does not fail.

## Performance considerations

Each `tools/list` request triggers a network call to every userSpecificList server. The first request per session includes a full MCP initialize handshake; subsequent requests reuse the cached session.

- Expect added latency proportional to the slowest userSpecificList upstream
- Multiple userSpecificList servers are fetched concurrently
- Each fetch has an independent timeout (does not block other fetches)
- Only enable for servers that genuinely return different tools per user

## Limitations

- **No tools/list_changed notifications**: the gateway does not maintain persistent connections to userSpecificList servers, so upstream `tools/list_changed` notifications are not propagated. Clients must re-poll `tools/list`.
- **No caching of user-specific tools**: tools are fetched fresh on each `tools/list`. A short-TTL cache may be added in a future release.
- **credentialRef not used**: the service account credential from `credentialRef` is used for ping checks only. User-specific tool fetches use the caller's own headers.
