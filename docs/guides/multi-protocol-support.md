# Multi Protocol Support

The MCP Gateway serves both `2025-11-25` (stateful) and `2026-07-28` (stateless) protocol versions from a single gateway instance. No configuration is needed — clients negotiate their preferred version automatically.

## How version detection works

When a client connects, the gateway detects the protocol version:

1. The SDK client sends a `server/discover` request with `MCP-Protocol-Version: 2026-07-28`
2. The gateway responds with `supportedVersions` — the union of all upstream server protocol versions
3. If the client and gateway share a common version, the SDK negotiates the highest one
4. If the client skips `server/discover` (older SDKs), the gateway falls back to the `initialize` handshake with `2025-11-25`

| Gateway has | `supportedVersions` | SDK negotiates |
|---|---|---|
| Only 2025 backends | `["2025-11-25"]` | 2025-11-25 |
| Only 2026 backends | `["2026-07-28"]` | 2026-07-28 |
| Both | `["2025-11-25", "2026-07-28"]` | 2026-07-28 (highest) |

### Overriding an upstream's advertised versions

The gateway learns which versions an upstream serves from that upstream's
`server/discover` response. Some MCP servers do not advertise all the protocol
versions they actually support. When that happens, the gateway classifies the
upstream from an incomplete list and federates its tools only to clients on the
advertised versions — leaving it invisible to clients on the versions it serves
but did not advertise, despite serving those clients correctly. This field lets
the gateway accommodate that server-side gap.

Set `spec.supportedProtocolVersions` on the `MCPServerRegistration` to declare
the versions the upstream actually serves. The gateway then classifies and
serves its tools to clients on each listed version instead of trusting
`server/discover` alone. The version negotiated at `initialize` is always
included implicitly.

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: example
spec:
  targetRef: { ... }
  # This upstream serves both eras but advertises only 2026-07-28 in
  # server/discover; declare both so 2025 clients can reach its tools.
  supportedProtocolVersions:
    - "2025-11-25"
    - "2026-07-28"
```

Only list versions the upstream genuinely serves: a client calls a listed tool
over its own negotiated version, so declaring a version the upstream does not
serve will cause those calls to fail.

This field is a last-resort override for servers that misreport their supported
versions. Setting it does not guarantee any future protocol discovery or
cross-version compatibility — it only forces classification against the versions
you list. Prefer fixing the upstream's `server/discover` advertisement where you
can, and use this field only when you cannot.

### Overriding an upstream's cache scope

A private-scope upstream returns per-user list results, so its tool names are
not in the shared routing table and cannot be routed by the router's prefix
fallback. The gateway therefore excludes a private-scope server's tools from
`tools/list` when it has no `prefix`. Under the `2026-07-28` cache-metadata
semantics an unspecified `cacheScope` defaults to `private`, so a server that
never opts into caching still presents as private — even when its tool catalog
is actually static and identical for every user.

Set `spec.cacheScope: Public` on the `MCPServerRegistration` to declare such a
catalog shared. The gateway then treats the upstream's cacheable list results as
public, so its tools land in the shared routing table and federate without a
`prefix`. This is useful for servers that default to `private` for a catalog
that is genuinely shared, and for servers that already self-prefix their tool
names (where adding a routing `prefix` would double it). Leave the field unset
to use the scope from the upstream's list responses.

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: example
spec:
  targetRef: { ... }
  # This upstream's tool catalog is static and identical for every user, but it
  # advertises (or defaults to) private; declare it public so its tools federate
  # without a prefix.
  cacheScope: Public
```

Only set `Public` for a catalog that really is shared across users: the gateway
will place its tools in the shared routing table, so marking a genuinely
per-user catalog public will make its tool calls unroutable. This is a
last-resort override for servers that report a private scope for an
actually-shared catalog; it does not change how the upstream itself caches
results.

## Which tools each client sees

`tools/list` returns only tools from protocol-compatible backends:

- **2025-11-25 clients** see tools from servers that support 2025-11-25, plus the `discover_tools` and `select_tools` meta-tools
- **2026-07-28 clients** see tools from servers that support 2026-07-28, without meta-tools

UserSpecificList servers follow the same filtering — per-user tools are fetched only from backends matching the client's protocol version.

## Behaviour differences

| | 2025-11-25 (stateful) | 2026-07-28 (stateless) |
|---|---|---|
| **Routing** | Body-parsed (JSON-RPC method + params) | Header-based (`Mcp-Method`, `Mcp-Name`) |
| **Sessions** | JWT-based `mcp-session-id` | None |
| **Backend init** | Hairpin initialization through gateway | `server/discover` |
| **Response handling** | Session ID rewriting, elicitation ID rewriting | Pass-through |
| **Header-body validation** | Not applicable | Rejects mismatches between `Mcp-Name` header and body `params.name` |
| **Meta-tools** | `discover_tools`, `select_tools` available | Not available |

## Protocol-specific route

The gateway exposes two endpoints on every MCP listener:

| Endpoint | Protocol | Behaviour |
|---|---|---|
| `/mcp` | Auto-negotiated | `server/discover` determines version; falls back to `initialize` for older SDKs |
| `/mcp/stateful` | Forces 2025-11-25 | Session-based routing; `discover_tools` and `select_tools` available |

A 2026 SDK client connecting to `/mcp` will negotiate 2026 naturally. The `/mcp/stateful` route exists for agents that also need tools from 2025-only backends — it forces 2025-11-25 negotiation regardless of the client's capabilities.

This endpoint is served by the broker's `MCPHandler` — no additional configuration or HTTPRoutes are needed.

### When to use /mcp/stateful

Use `/mcp` (the default) for most clients — the gateway negotiates the correct version automatically.

Use `/mcp/stateful` when a 2026-capable agent also needs access to 2025-only tools. Configure the agent with two MCP server entries pointing at the same gateway host:

```yaml
mcpServers:
  gateway-default:
    url: https://mcp.example.com/mcp
  gateway-legacy:
    url: https://mcp.example.com/mcp/stateful
```

The default entry negotiates 2026 and sees stateless tools. The `/mcp/stateful` entry forces 2025 and sees stateful tools plus `discover_tools` and `select_tools`.

## Verifying protocol support

Check which protocol versions the gateway advertises:

```bash
curl -sS -X POST https://mcp.example.com/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Protocol-Version: 2026-07-28" \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}' \
  | jq '.result.supportedVersions'
```

## Next Steps

- [Register MCP Servers](register-mcp-servers.md) with the gateway
- [Configure Authentication](authentication.md) for your MCP servers
- [Understanding MCP Gateway Architecture](understanding-mcp-gateway-architecture.md) for how routing works
