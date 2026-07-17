# E2E Test Cases: Single Gateway Dual Protocol

These tests prove the gateway is protocol-agnostic — a single instance serves both 2025-11-25 and 2026-07-28 clients correctly.

## Test infrastructure

- **Single MCPGatewayExtension** (no `protocolMode` field) serving both protocols
- Uses the **isolated listener pattern** for parallel safety: dedicated namespace, listener, and hostname
- Two test backends:
  - `mcp-test-stateless-server` — speaks 2026-07-28 only
  - `server1` or `server2` — speaks 2025-11-25 only
  - `user-specific-server` — speaks whichever version the SDK negotiates (both)
- Optionally: a test server that explicitly supports both versions (returns both in `server/discover` `supportedVersions`)
- Two client types per test:
  - 2025 client: `NewMCPGatewayClientWithNotifications` (session-based, SSE)
  - 2026 client: `NewMCPGatewayClient` (stateless, SDK auto-negotiates 2026-07-28)

## Test cases

### [Happy,DualProtocol] 2025 client sees only 2025-compatible tools

Register a 2025-only backend (`server1`) and a 2026-only backend (`stateless-server`) on the same gateway with distinct prefixes. Connect a 2025 client and call `tools/list`. The result contains only tools from the 2025 backend (prefixed). No tools from the 2026 backend appear.

### [Happy,DualProtocol] 2026 client sees only 2026-compatible tools

Same gateway setup as above. Connect a 2026 client and call `tools/list`. The result contains only tools from the 2026 backend (prefixed). No tools from the 2025 backend appear.

### [Happy,DualProtocol] 2025 client can call tools on 2025 backend

Same gateway. 2025 client calls a tool from the 2025 backend via `tools/call`. The call succeeds and returns the expected result.

### [Happy,DualProtocol] 2026 client can call tools on 2026 backend

Same gateway. 2026 client calls a tool from the 2026 backend via `tools/call`. The call succeeds and returns the expected result.

### [DualProtocol] 2025 client sees discover_tools and select_tools

Same gateway. 2025 client calls `tools/list`. The result includes `discover_tools` and `select_tools` broker meta-tools.

### [DualProtocol] 2026 client does not see discover_tools or select_tools

Same gateway. 2026 client calls `tools/list`. The result does not include `discover_tools` or `select_tools`.

### [DualProtocol] Dual-version server tools visible to both clients

Register a backend that supports both protocol versions (returns `["2025-11-25", "2026-07-28"]` in `server/discover` `supportedVersions`). Connect a 2025 client — sees the server's tools. Connect a 2026 client — also sees the server's tools.

### [DualProtocol,UserSpecificList] 2025 client gets per-user tools from 2025 UserSpecificList server

Register a `userSpecificList: true` server that speaks 2025-11-25. Connect a 2025 client with `Authorization: Bearer user-a-token`. Call `tools/list`. The result includes user-A-specific tools from the server.

### [DualProtocol,UserSpecificList] 2026 client gets per-user tools from 2026 UserSpecificList server

Register a `userSpecificList: true` server that speaks 2026-07-28. Connect a 2026 client with `Authorization: Bearer user-a-token`. Call `tools/list`. The result includes user-A-specific tools from the server. Verify the fetch is stateless (no session caching — a second call with `user-b-token` returns different tools without session state leaking).

### [DualProtocol,UserSpecificList] 2025 client does not see tools from 2026-only UserSpecificList server

Register two `userSpecificList: true` servers — one 2025-only, one 2026-only. Connect a 2025 client. `tools/list` includes per-user tools only from the 2025 server. The 2026 server's tools are absent.

### [DualProtocol,UserSpecificList] 2026 client does not see tools from 2025-only UserSpecificList server

Same setup as above. Connect a 2026 client. `tools/list` includes per-user tools only from the 2026 server. The 2025 server's tools are absent.

### [DualProtocol] Existing 2025-only gateway behaviour unchanged (regression)

Deploy with only 2025-11-25 backends (no 2026 backends registered). Connect a 2025 client. `tools/list`, `tools/call`, `discover_tools`, `select_tools` all work as before. This is the regression safety net — existing deployments with no 2026 servers see no change.

### [DualProtocol] Gateway with only 2026 backends returns empty list for 2025 client

Deploy with only 2026-07-28 backends. Connect a 2025 client. `tools/list` returns no tools (or only broker meta-tools). Connect a 2026 client — sees all tools.

### [DualProtocol,Security] Protocol version header cannot leak cross-protocol tools

Connect a 2025 client (session-based). Manually set `MCP-Protocol-Version: 2026-07-28` header on a raw `tools/list` request using the 2025 session. Verify the gateway does not return 2026-only tools — the session-based transport identifies this as a 2025 client regardless of the header override. (This tests that protocol determination is robust, not just header-trusting.)

## Version-aware server/discover

### [Happy,DualProtocol] 2025-only gateway negotiates 2025 with SDK client

Deploy a gateway with only 2025-11-25 backends. Connect a standard SDK client (no legacy transport). The SDK's `server/discover` receives `supportedVersions: ["2025-11-25"]`, negotiates down to 2025. `tools/list` returns 2025 tools. No `blockDiscoverTransport` needed.

### [Happy,DualProtocol] Dual-protocol gateway negotiates 2026 with SDK client

Deploy a gateway with both 2025 and 2026 backends. Connect a standard SDK client. The SDK's `server/discover` receives `supportedVersions: ["2025-11-25", "2026-07-28"]`, negotiates 2026. `tools/list` returns only 2026-compatible tools.

### [DualProtocol] 2026-only gateway negotiates 2026 with SDK client

Deploy a gateway with only 2026-07-28 backends. Connect a standard SDK client. Negotiates 2026. `tools/list` returns 2026 tools.

## Protocol-specific routes

### [Happy,DualProtocol] /mcp/stateful returns only 2025 tools

Deploy a dual-protocol gateway. Connect a standard SDK client to `/mcp/stateful`. Regardless of the client's negotiated version, `tools/list` returns only tools from 2025-compatible backends. `discover_tools` and `select_tools` are available.

### [Happy,DualProtocol] /mcp/stateless returns only 2026 tools

Same gateway. Connect a standard SDK client to `/mcp/stateless`. `tools/list` returns only tools from 2026-compatible backends. No `discover_tools` or `select_tools`.

### [DualProtocol] /mcp/stateful tools/call routes through 2025 router

Connect to `/mcp/stateful` and call a 2025 tool. The request routes through Router202511 (hairpin init, session management). The call succeeds.

### [DualProtocol] /mcp/stateless tools/call routes through 2026 router

Connect to `/mcp/stateless` and call a 2026 tool. The request routes through Router202607 (header-based). The call succeeds.

### [DualProtocol] /mcp negotiates normally

Connect a standard SDK client to `/mcp`. The gateway negotiates the best available version via `server/discover`. Tools returned match the negotiated version. This is the default behaviour — unchanged from the base dual-protocol tests.

## Test server requirements

The existing `stateless-server` speaks 2026-07-28 only. The existing `server1`/`server2` speak 2025-11-25 only. For the dual-version test case, one of:
- Modify `user-specific-server` to explicitly return both versions in `server/discover` `supportedVersions`
- Create a new `dual-protocol-server` test server that advertises both versions

The `user-specific-server` already uses the standard go-sdk which auto-negotiates both versions. If `server/discover` returns `supportedVersions: ["2025-11-25", "2026-07-28"]` by default with the latest SDK, it may already work. Verify before creating a new server.
