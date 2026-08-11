# Auditing MCP Tool Calls

This guide covers how to produce an audit trail for MCP tool calls — capturing who called which tool, on which server, in which session, and whether it succeeded.

Two approaches are available:

| Approach | Where logs appear | Works on OpenShift 4.19+ |
|----------|-------------------|--------------------------|
| **Router structured log** (recommended) | Router pod stdout | Yes |
| Istio Telemetry access log | Envoy gateway pod stdout | No — CIO overwrites the Istio CR on 4.19–4.21; Istio CR is absent on 4.22+ |

## Approach 1: Router structured audit log (recommended)

The router emits a structured `level=INFO` log entry for every `tools/call` request. No additional infrastructure is required.

### What gets logged

An `audit=true` entry appears at two points:

**Response headers phase** — the call reached the backend and received a response:
```
level=INFO msg="tool call" audit=true user=alice tool=echo server=mcp-test/my-server status=200 request_id=<uuid> session=jti:...
```

**Request body phase** — the router returned an error before forwarding (for example, expired session, unknown server):
```
level=INFO msg="tool call" audit=true user=alice tool=echo server="" status=401 request_id=<uuid> session=sha256:...
```

**Not logged** (no tool identity available, or not the router's responsibility):
- AuthPolicy denials — Envoy rejects before ext_proc is involved
- Invalid JSON-RPC or unparseable body — rejected before `mcpRequest` is populated
- `tools/list`, `initialize`, and all other non-`tools/call` methods

### Fields

| Field | Description |
|-------|-------------|
| `audit` | Always `true` — use this to filter audit entries from other Info logs |
| `user` | JWT `sub` claim from the `Authorization` header; empty if unauthenticated |
| `tool` | Prefixed tool name as sent by the client (e.g. `everything_echo`) |
| `server` | `namespace/name` of the MCPServerRegistration; empty if routing failed |
| `status` | HTTP status code of the response |
| `request_id` | Envoy-generated request UUID (`x-request-id`) |
| `session` | Log-safe session identifier — `jti:<uuid>` or `sha256:<prefix>`, never a raw JWT |

### Querying audit entries

```bash
# tail live audit entries
kubectl logs -f -n mcp-system -l app.kubernetes.io/name=mcp-gateway \
  | grep 'audit=true'

# all failed tool calls in the last hour
kubectl logs -n mcp-system -l app.kubernetes.io/name=mcp-gateway --since=1h \
  | grep 'audit=true' | grep -v 'status=200'

# calls by a specific user
kubectl logs -n mcp-system -l app.kubernetes.io/name=mcp-gateway --since=1h \
  | grep 'audit=true' | grep 'user=alice'

# calls to a specific tool
kubectl logs -n mcp-system -l app.kubernetes.io/name=mcp-gateway --since=1h \
  | grep 'audit=true' | grep 'tool=everything_echo'
```

For production, ship router pod logs to a log aggregation system (Loki, Elasticsearch, Splunk) and query there. See the [Observability guide](./opentelemetry.md) for Loki/Grafana integration.

### The `user` field and authentication

The `user` field is sourced from the JWT `sub` claim in the `Authorization` header, extracted directly by the router. It is not sourced from `x-mcp-verified-sub`, which is a router-set internal header and must not be used for audit purposes.

Without an auth layer, `user` is empty. To populate it, configure an AuthPolicy to require a JWT on the gateway listener — the router then extracts the `sub` claim automatically. See [Authentication](./authentication.md) for setup.

---

## Approach 2: Istio Telemetry access log

> **Not supported on OpenShift 4.19+.** On 4.19–4.21 the Cluster Ingress Operator overwrites the Istio CR; on 4.22+ the Istio CR is absent (Istio is installed via Helm through OSSM). Use approach 1 on OpenShift.

This approach configures a JSON access log on the Envoy gateway pod using Istio's Telemetry API. It provides richer Envoy-level fields (upstream host, bytes sent/received, duration) and integrates with the Istio observability stack.

### Prerequisites

- MCP Gateway installed and configured
- Identity provider deployed (this guide uses Keycloak, see [Authentication](./authentication.md) for setup)
- Kuadrant installed with AuthPolicy CRD available
- Istio configured as the Gateway API provider

### Step 1: Configure AuthPolicy with identity injection

MCP Gateway uses two gateway listeners: `mcp` handles client requests and `mcps` handles tool call routing to backend servers. Create an AuthPolicy on each to validate JWTs and inject the authenticated username as a trusted request header.

**Client-facing listener (`mcp`):**

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

**Backend-facing listener (`mcps`):**

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

After Authorino validates the JWT it injects `x-auth-identity` from the token claims. This header is trustworthy because Authorino strips any client-supplied value before setting it.

> Replace `preferred_username` with `sub` or another claim depending on your identity provider. `preferred_username` gives a human-readable name but requires the `profile` scope in the token request.

### Step 2: Add MeshConfig extension provider

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

> If you have other extension providers configured (for example, for OpenTelemetry tracing), include them alongside this one — the merge replaces the array.

### Step 3: Create Telemetry resource

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

### Step 4: Verify

```bash
curl -s http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test1_greet","arguments":{"name":"audit-test"}}}'

kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=30s \
  | grep '"mcp_method"' | tail -1 | jq .
```

Expected output:

```json
{
  "timestamp": "2026-05-21T14:23:01.123Z",
  "mcp_method": "tools/call",
  "mcp_tool_name": "greet",
  "mcp_server_name": "mcp-test/test-server1",
  "mcp_session_id": "sess-7a3b",
  "mcp_user_id": "mcp",
  "response_code": 200,
  "duration_ms": 342
}
```

### Without authentication

If you do not have an auth layer, the routing headers still provide useful audit context. The `mcp_user_id` field will be empty (`-`), but you still get: which tool was called, on which server, in which session, when, and how long it took.

### Example queries

Filter access logs using `jq`:

```bash
# All tool calls by a specific user
kubectl logs -n gateway-system -l gateway.networking.k8s.io/gateway-name=mcp-gateway --since=1h \
  | grep '"mcp_method"' | jq 'select(.mcp_user_id == "alice")'

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

For production, ship gateway pod logs to a log aggregation system (Loki, Elasticsearch, Splunk). See the [Observability guide](./opentelemetry.md) for Loki/Grafana integration.

### Customizing the identity header

The `x-auth-identity` header name and the JWT claim used are configurable in the AuthPolicy. Adjust the `response.success.headers` section to match your identity provider:

- **Keycloak**: `auth.identity.preferred_username` or `auth.identity.email`
- **Generic OIDC**: `auth.identity.sub` (subject claim, always present in JWTs)
- **Custom claims**: `auth.identity.<claim_name>` for any claim in the JWT payload

If you change the header name, update the `mcp_user_id` field in the MeshConfig extension provider to match: `%REQ(YOUR-HEADER-NAME)%`.

## Next Steps

- [Authentication](./authentication.md) — configure OAuth 2.1 for MCP Gateway
- [Authorization](./authorization.md) — control which users can access specific tools
- [OpenTelemetry](./opentelemetry.md) — distributed tracing for request-level debugging
