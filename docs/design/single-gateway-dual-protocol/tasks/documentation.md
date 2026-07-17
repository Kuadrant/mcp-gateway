# Documentation Plan: Single Gateway Dual Protocol

`protocolMode` only exists on this branch and was never released. No breaking change documentation or migration steps needed — just remove it from code and any docs on this branch.

## Guide: protocol-modes.md rewrite — DONE

Rewritten to describe single-gateway dual-protocol support. Covers version detection, protocol-filtered tools/list, protocol-specific routes, and behaviour differences.

### When I want to serve clients using different MCP protocol versions

When a platform engineer has agents using both `2025-11-25` and `2026-07-28`, they want to understand how the gateway handles both versions so that they can support mixed client populations without additional infrastructure.

**Cover:**
- Single gateway serves both protocols — no configuration needed
- How version detection works (client sends `MCP-Protocol-Version` header)
- What each client type sees: `tools/list` filtered by protocol version
- `discover_tools`/`select_tools` available only to 2025-11-25 clients
- Behaviour differences between the two protocols (table from existing guide, updated)

### When I want to understand which tools my clients will see

When a platform engineer registers upstream servers that support different protocol versions, they want to understand how `tools/list` is filtered so that they can verify each client type sees the correct tools.

**Cover:**
- Tools filtered by backend server's supported protocol versions
- Servers supporting both versions: tools appear for all clients
- UserSpecificList servers: per-user tools also filtered by protocol version
- How to verify: connect with a 2025 client and 2026 client, compare `tools/list` results

### When I want to register a server that supports both protocol versions

When a platform engineer has an upstream MCP server that supports both `2025-11-25` and `2026-07-28`, they want to know how the gateway detects this so that the server's tools are available to all clients.

**Cover:**
- No configuration needed — gateway detects supported versions automatically
- Detection mechanism: `server/discover` for 2026-capable servers returns `supportedVersions`
- Verification: check server status in MCPServerRegistration for detected protocol versions

### When I want an agent to access tools from both protocol versions

When a platform engineer has backends on different protocol versions and wants a single agent to access all tools, they want to understand how to use protocol-specific routes so that the agent can connect to both `/mcp/stateful` and `/mcp/stateless` as separate MCP servers.

**Cover:**
- Protocol-specific routes: `/mcp/stateful` (forces 2025-11-25), `/mcp/stateless` (forces 2026-07-28)
- Default `/mcp` negotiates the best available version
- Example agent configuration with two MCP server entries pointing at the same gateway
- Each route returns only protocol-compatible tools

### When I want to understand how the gateway advertises its protocol support

When a platform engineer wants to know how clients discover which protocol versions the gateway supports, they want to understand the `server/discover` response so that they can verify clients negotiate the correct version.

**Cover:**
- Gateway computes `supportedVersions` from the union of all upstream server versions
- 2025-only gateway advertises `["2025-11-25"]` — SDK clients negotiate 2025 naturally
- Dual-protocol gateway advertises `["2025-11-25", "2026-07-28"]`
- No client-side workarounds needed — version negotiation is automatic

## API reference: MCPGatewayExtension — DONE

`protocolMode` field removed from `docs/reference/mcpgatewayextension.md` spec table.
