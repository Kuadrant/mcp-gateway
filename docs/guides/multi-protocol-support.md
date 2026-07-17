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

## Which tools each client sees

`tools/list` returns only tools from protocol-compatible backends:

- **2025-11-25 clients** see tools from servers that negotiated 2025-11-25, plus the `discover_tools` and `select_tools` meta-tools
- **2026-07-28 clients** see tools from servers that negotiated 2026-07-28, without meta-tools

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

## Protocol-specific routes

The gateway exposes three endpoints on every MCP listener:

| Endpoint | Protocol | Behaviour |
|---|---|---|
| `/mcp` | Auto-negotiated | `server/discover` determines version; falls back to `initialize` for older SDKs |
| `/mcp/stateful` | Forces 2025-11-25 | Session-based routing; `discover_tools` and `select_tools` available |
| `/mcp/stateless` | Forces 2026-07-28 | Stateless header-based routing; no sessions, no meta-tools |

The `/mcp/stateful` and `/mcp/stateless` endpoints override the client's protocol negotiation. A 2026 SDK client connecting to `/mcp/stateful` will receive 2025-compatible tools and route through the stateful router. A 2025 SDK client connecting to `/mcp/stateless` will receive 2026-compatible tools and route through the stateless router.

These endpoints are served by the broker's `MCPHandler` — no additional configuration or HTTPRoutes are needed. Any Gateway listener with an MCPGatewayExtension automatically serves all three paths.

### When to use protocol-specific routes

Use `/mcp` (the default) for most clients — the gateway negotiates the correct version automatically.

Use the protocol-specific routes when an agent needs tools from backends on different protocol versions. Configure the agent with two MCP server entries pointing at the same gateway host:

```yaml
mcpServers:
  gateway-legacy:
    url: https://mcp.example.com/mcp/stateful
  gateway-modern:
    url: https://mcp.example.com/mcp/stateless
```

Each route returns only protocol-compatible tools. `tools/call` via `/mcp/stateful` routes through the stateful router (hairpin init, session management); `/mcp/stateless` routes through the stateless router (header-based, no sessions).

## Verifying protocol support

Check which protocol versions the gateway advertises:

```bash
curl -s https://mcp.example.com/mcp \
  -H "MCP-Protocol-Version: 2026-07-28" \
  -H "Accept: application/json" | jq .supportedVersions
```

## Next Steps

- [Register MCP Servers](register-mcp-servers.md) with the gateway
- [Configure Authentication](authentication.md) for your MCP servers
- [Understanding MCP Gateway Architecture](understanding-mcp-gateway-architecture.md) for how routing works
