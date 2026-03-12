# Tool Revocation

This guide covers revoking access to MCP tools for specific users or groups, and monitoring revocation enforcement.

## Overview

Tool revocation prevents a user or group from calling specific MCP tools. It builds on the authorization setup where tool access is controlled by roles in the identity provider's JWT tokens. Revoking a tool means removing the corresponding role from a user or group, so their next token no longer grants access.

Two enforcement points apply:

- **`tools/call`**: The AuthPolicy's CEL expression checks the `x-mcp-toolname` header against the user's `resource_access` roles. A revoked tool returns 403 Forbidden.
- **`tools/list`**: The broker filters the tools list using the signed `x-authorized-tools` header. A revoked tool no longer appears in the list.

## Prerequisites

- [Authentication](./authentication.md) configured
- [Authorization](./authorization.md) configured with tool-level AuthPolicy

## Step 1: Revoke Tool Access

Remove the tool role from the user or group in your identity provider.

In Keycloak, this is done by removing a client role mapping. The client name corresponds to the namespaced MCPServerRegistration (e.g., `mcp-test/server1-route`), and each role represents a tool name (e.g., `greet`, `headers`).

To revoke a tool for a group:
1. Go to **Groups** > select the group (e.g., `accounting`)
2. Go to **Role mapping** > remove the tool role from the relevant client

To revoke a tool for a single user:
1. Go to **Users** > select the user
2. Go to **Role mapping** > remove the tool role from the relevant client

## Step 2: Understand When Revocation Takes Effect

Revocation is not instantaneous. Access is governed by the JWT token, and tokens are valid until they expire.

- **New sessions**: Users who authenticate after revocation receive a token without the revoked tool. They are denied immediately.
- **Existing sessions**: Users with an active token retain access until the token expires. The token lifetime is configured in your identity provider (e.g., Keycloak's **Access Token Lifespan** setting).
- **In-flight requests**: A `tools/call` that is already being processed completes normally. The authorization check occurs before the request reaches the backend, so only new requests are affected.

To force faster revocation, reduce the access token lifespan in your identity provider. Shorter lifetimes mean tokens are refreshed more frequently, picking up role changes sooner. This is a trade-off between revocation latency and token refresh overhead.

> **Note:** There is no mechanism to revoke a specific in-flight token. Revocation relies on token expiry and re-issuance.

## Step 3: Verify Revocation

After revoking a tool, verify that the user can no longer access it.

**Check `tools/list` filtering:**

Authenticate as the affected user and list tools. The revoked tool should no longer appear:

```bash
# Obtain a token as the affected user, then:
curl -X POST http://your-gateway-host/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

**Check `tools/call` denial:**

Attempt to call the revoked tool. You should receive a 403 Forbidden response:

```bash
curl -X POST http://your-gateway-host/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "revoked_tool"}}'
```

Expected response:

```json
{
  "error": "Forbidden",
  "message": "MCP Tool Access denied: Insufficient permissions for this tool."
}
```

## Step 4: Monitor Revocation Enforcement

Use existing observability to track denied access attempts after revocation.

### Authorino Logs

Authorino logs all authorization decisions, including denials. Check for authorization failures:

```bash
kubectl logs -n kuadrant-system -l app=authorino | grep -i "unauthorized\|denied\|forbidden"
```

### OpenTelemetry Traces

If [OpenTelemetry](./opentelemetry.md) is enabled, the MCP Router emits trace spans for every request. Denied requests include `http.status_code: 403` on the response span. You can query your trace backend for these:

- Filter by `http.status_code = 403`
- Use `gen_ai.tool.name` to identify which tool was denied
- Use `mcp.session.id` to correlate with the client session

### Loki / Log Aggregation

If the [observability stack](./observability.md) is deployed, query gateway logs for revocation-related activity:

```logql
{namespace="mcp-system"} |= `x-mcp-toolname` | json | line_format "{{.msg}}"
```

The router logs the `x-mcp-toolname` and `x-mcp-servername` headers for every `tools/call` request. Combined with the 403 status from Authorino, this gives visibility into which users are attempting to call revoked tools.

## Next Steps

- **[Authorization](./authorization.md)** - Configure tool-level access control
- **[OpenTelemetry](./opentelemetry.md)** - Enable distributed tracing
- **[Troubleshooting](./troubleshooting.md)** - Debug authorization issues
