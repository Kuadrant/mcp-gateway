# Auditing MCP Tool Calls

This guide covers how to produce an audit trail for MCP tool calls — capturing who called which tool, on which server, in which session, and whether it succeeded.

The router structured audit log is the supported audit mechanism.

## Router structured audit log

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

## Next Steps

- [Authentication](./authentication.md) — configure OAuth 2.1 for MCP Gateway
- [Authorization](./authorization.md) — control which users can access specific tools
- [OpenTelemetry](./opentelemetry.md) — distributed tracing for request-level debugging
