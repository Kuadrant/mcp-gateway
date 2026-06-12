# Auditing MCP Tool Calls

This guide covers configuring structured access logs for MCP Gateway that include caller identity, tool names, and MCP session context. It uses Kuadrant AuthPolicy to inject trusted identity from validated JWT claims, and Istio's Telemetry API to configure JSON access logging on the gateway.

## Overview

MCP Gateway already sets routing headers on every request (`x-mcp-method`, `x-mcp-toolname`, `x-mcp-servername`, `mcp-session-id`). These headers are available to any Envoy access log via `%REQ(...)%` format strings. This guide adds two things:

1. **Caller identity**: AuthPolicy validates JWT tokens and injects the authenticated username as a request header (`x-auth-identity`)
2. **Structured access log**: An Istio Telemetry resource configures JSON access logging on the gateway, capturing MCP routing headers and the identity header

The result is a JSON access log on the gateway pod's stdout that answers: who called which tool, on which server, in which session, and when.

## Prerequisites

- MCP Gateway installed and configured
- Identity provider deployed (this guide uses Keycloak, see [Authentication](./authentication.md) for setup)
- [Kuadrant](https://kuadrant.io) installed with AuthPolicy CRD available
- Istio configured as the Gateway API provider

> **Local development:** To follow along locally, run `make local-env-setup-olm && make auth-example-setup-no-vault` to bring up a Kind cluster with the gateway, Kuadrant, Keycloak, and test servers. The verification steps use the `test1_headers` tool from the test servers to inspect backend headers, but this is optional: any tool call will produce access log entries.

## Step 1: Configure AuthPolicy with Identity Injection

MCP Gateway uses two gateway listeners: `mcp` handles client requests (initialize, tools/list), and `mcps` handles tool call routing to backend MCP servers. To capture the authenticated user identity in access logs for all request types, create an AuthPolicy on each listener.

### Step 1a: Client-facing listener (`mcp`)

This policy validates JWT tokens on client requests and injects the authenticated username as a request header:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-audit-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  defaults:
    when:
      - predicate: "!request.path.contains('/.well-known')"
    rules:
      authentication:
        'keycloak':
          jwt:
            issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
      response:
        success:
          headers:
            "x-auth-identity":
              plain:
                selector: auth.identity.preferred_username
        unauthenticated:
          code: 401
          headers:
            'WWW-Authenticate':
              value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
          body:
            value: |
              {
                "error": "Unauthorized",
                "message": "Authentication required."
              }
EOF
```

### Step 1b: Backend-facing listener (`mcps`)

This policy validates the same JWT on tool call requests routed to backend servers and injects the identity header there too:

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcps-audit-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcps
  defaults:
    rules:
      authentication:
        'keycloak':
          jwt:
            issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
      response:
        success:
          headers:
            "x-auth-identity":
              plain:
                selector: auth.identity.preferred_username
        unauthenticated:
          code: 401
          headers:
            'WWW-Authenticate':
              value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
          body:
            value: |
              {
                "error": "Unauthorized",
                "message": "Authentication required."
              }
EOF
```

After Authorino validates the JWT, it extracts `preferred_username` from the token claims and injects it as the `x-auth-identity` request header. This header is trustworthy because Authorino strips any client-supplied value and sets it from the validated token. With both policies in place, the `mcp_user_id` field appears in access log entries for all MCP request types.

> **Note:** Replace `preferred_username` with `sub` or another claim depending on your identity provider and what you want as the audit identity. For Keycloak, `preferred_username` gives a human-readable username but requires the `profile` scope in the token request.

Verify that the identity header is injected by calling a tool and checking what the backend receives:

```bash
# Get a token (adjust for your identity provider)
TOKEN=$(curl -s -X POST "https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=mcp-gateway&client_secret=secret&username=mcp&password=mcp&scope=openid+profile" \
  | jq -r '.access_token')

# Initialize a session (session ID is returned in the response header)
SESSION_ID=$(curl -si http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"audit-test","version":"0.0.1"}}}' \
  | grep -i 'mcp-session-id:' | awk '{print $2}' | tr -d '\r')

# Call the headers tool (available on test server1) to see what headers reach the backend
curl -s http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"test1_headers","arguments":{}}}' \
  | jq '.result.content[0].text' | grep -i "x-auth-identity"
```

You should see `x-auth-identity` with the Keycloak username in the response.

## Step 2: Add MeshConfig Extension Provider

Add a custom access log provider to Istio's MeshConfig. This provider defines the JSON format for the audit log.

```bash
kubectl patch istio default -n istio-system --type='merge' \
  -p='{
    "spec": {
      "values": {
        "meshConfig": {
          "extensionProviders": [
            {
              "name": "mcp-json-access-log",
              "envoyFileAccessLog": {
                "path": "/dev/stdout",
                "logFormat": {
                  "labels": {
                    "timestamp": "%START_TIME%",
                    "method": "%REQ(:METHOD)%",
                    "path": "%REQ(:PATH)%",
                    "response_code": "%RESPONSE_CODE%",
                    "request_id": "%REQ(X-REQUEST-ID)%",
                    "traceparent": "%REQ(TRACEPARENT)%",
                    "mcp_method": "%REQ(X-MCP-METHOD)%",
                    "mcp_tool_name": "%REQ(X-MCP-TOOLNAME)%",
                    "mcp_server_name": "%REQ(X-MCP-SERVERNAME)%",
                    "mcp_session_id": "%REQ(MCP-SESSION-ID)%",
                    "mcp_user_id": "%REQ(X-AUTH-IDENTITY)%",
                    "duration_ms": "%DURATION%",
                    "upstream_host": "%UPSTREAM_HOST%",
                    "bytes_sent": "%BYTES_SENT%",
                    "bytes_received": "%BYTES_RECEIVED%"
                  }
                }
              }
            }
          ]
        }
      }
    }
  }'
```

> **Note:** If you have other extension providers configured (e.g., for OpenTelemetry tracing), include them in the `extensionProviders` array alongside the access log provider, since the merge replaces the array.

**Fields explained:**

| Field | Source | Description |
|-------|--------|-------------|
| `mcp_method` | `%REQ(X-MCP-METHOD)%` | MCP method (`tools/call`, `tools/list`, `initialize`, etc.) |
| `mcp_tool_name` | `%REQ(X-MCP-TOOLNAME)%` | Tool name (after prefix stripping) |
| `mcp_server_name` | `%REQ(X-MCP-SERVERNAME)%` | Backend MCP server name |
| `mcp_session_id` | `%REQ(MCP-SESSION-ID)%` | MCP session identifier |
| `mcp_user_id` | `%REQ(X-AUTH-IDENTITY)%` | Authenticated user identity (injected by AuthPolicy) |
| `traceparent` | `%REQ(TRACEPARENT)%` | W3C Trace Context for cross-system correlation |
| `request_id` | `%REQ(X-REQUEST-ID)%` | Per-request identifier |

## Step 3: Create Telemetry Resource

Create a Telemetry resource in the gateway namespace to enable the access log on the gateway workload:

```bash
kubectl apply -f - <<EOF
apiVersion: telemetry.istio.io/v1
kind: Telemetry
metadata:
  name: mcp-audit-logging
  namespace: gateway-system
spec:
  selector:
    matchLabels:
      gateway.networking.k8s.io/gateway-name: mcp-gateway
  accessLogging:
    - providers:
        - name: mcp-json-access-log
EOF
```

This scopes the access log to the `mcp-gateway` gateway pods only. Other workloads in the mesh are not affected.

Verify the Telemetry resource is applied:

```bash
kubectl get telemetry -n gateway-system
```

## Step 4: Verify the Audit Trail

Make an authenticated tool call and check the gateway pod logs for the access log entry:

```bash
# Make a tool call (reuse token and session from Step 1)
curl -s http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"test1_greet","arguments":{"name":"audit-test"}}}'

# Check gateway pod logs for the JSON access log entry
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=30s \
  | grep '"mcp_method"' | tail -1 | jq .
```

You should see a JSON entry like:

```json
{
  "timestamp": "2026-05-21T14:23:01.123Z",
  "method": "POST",
  "path": "/mcp",
  "response_code": 200,
  "request_id": "abc-111",
  "traceparent": null,
  "mcp_method": "tools/call",
  "mcp_tool_name": "greet",
  "mcp_server_name": "mcp-test/test-server1",
  "mcp_session_id": "sess-7a3b",
  "mcp_user_id": "mcp",
  "duration_ms": 342,
  "upstream_host": "10.0.1.5:8080",
  "bytes_sent": 1024,
  "bytes_received": 512
}
```

## Example Queries

Filter access logs using `jq`:

```bash
# All tool calls by a specific user
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.mcp_user_id == "mcp")'

# All calls to a specific tool
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.mcp_tool_name == "greet")'

# All calls to a specific backend server
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.mcp_server_name == "mcp-test/test-server1")'

# Slow tool calls (over 500ms)
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.duration_ms > 500)'

# Failed requests (4xx and 5xx)
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.response_code >= 400)'
```

For production use, ship these logs to a log aggregation system (Loki, Elasticsearch, Splunk) and query there. See the [Observability guide](./observability.md) for Loki/Grafana integration.

## Without Authentication

If you don't have an auth layer, the routing headers still provide useful audit context. The `mcp_user_id` field will be empty (`-`), but you still get: which tool was called, on which server, in which session, when, and how long it took.

## Customizing the Identity Header

The `x-auth-identity` header name and the JWT claim used are configurable in the AuthPolicy. Adjust the `response.success.headers` section to match your identity provider:

- **Keycloak**: `auth.identity.preferred_username` or `auth.identity.email`
- **Generic OIDC**: `auth.identity.sub` (subject claim, always present in JWTs)
- **Custom claims**: `auth.identity.<claim_name>` for any claim in the JWT payload

If you change the header name, update the `mcp_user_id` field in the MeshConfig extension provider to match: `%REQ(YOUR-HEADER-NAME)%`.

## Next Steps

- **[Authentication](./authentication.md)**: Configure OAuth 2.1 authentication for MCP Gateway
- **[Authorization](./authorization.md)**: Control which users can access specific tools
- **[OpenTelemetry](./opentelemetry.md)**: Enable distributed tracing for request-level debugging
