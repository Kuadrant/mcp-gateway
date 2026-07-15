# Broker 2026-07-28 Protocol Support — Scope

## Problem

The broker is an MCP server from the client's perspective and an MCP client to upstream servers. Neither side works with 2026-07-28 today:

1. **Broker as server** (client-facing `/mcp`): the compat handler strips `MCP-Protocol-Version` from every request (`http_compat.go:116`), forcing 2025-11-25 negotiation. The `StreamableHTTPHandler` runs without `Stateless: true`, so it rejects 2026-07-28 requests. A 2026-07-28 client cannot connect.

2. **Broker as client** (upstream connections): the SDK's `client.Connect()` successfully negotiates 2026-07-28 via `server/discover`, but subsequent `session.Ping()` calls fail with `missing or invalid _meta field "io.modelcontextprotocol/protocolVersion"`. The SDK should attach `_meta` automatically on a 2026-07-28 session — this may be an SDK bug in v1.7.0-pre.2, or the ping is routed through a code path that doesn't have the session's protocol context.

## Broker as Server — Work Items

### 1. Remove compat handler's protocol version stripping

`http_compat.go:116` deletes `MCP-Protocol-Version` from every request. For 2026-07-28 clients, this header must be preserved so the SDK negotiates correctly.

**Options:**
- Only strip when the value is absent or a pre-2026 version (preserve it for 2026-07-28)
- Remove the compat handler entirely and let the SDK handle protocol negotiation natively (larger scope, breaks mark3labs compat)
- Branch: if version is 2026-07-28, skip the compat handler entirely and delegate directly to the SDK handler

**Recommendation:** Branch — if `MCP-Protocol-Version: 2026-07-28` is present, bypass the compat handler and go straight to the SDK handler. The compat handler exists solely for mark3labs backward compat, which doesn't apply to 2026-07-28 clients.

### 2. Enable stateless mode on the SDK handler

The SDK requires `StreamableHTTPOptions{Stateless: true}` for 2026-07-28. But if we set `Stateless: true`, 2025-11-25 clients (which need sessions) break.

**Options:**
- Two `StreamableHTTPHandler` instances: one stateful (2025-11-25), one stateless (2026-07-28), selected by protocol version header before dispatch
- Single handler with `Stateless: true` — the SDK may handle 2025-11-25 clients via initialize fallback
- Let the SDK evolve to support both modes in one handler (depends on SDK roadmap)

**Recommendation:** Two handlers, fronted by a version-detecting router in `MCPHandler()`. The stateless handler gets its own `mcp.Server` instance (or shares one — the SDK's `NewStreamableHTTPHandler` takes a factory `func(*http.Request) *mcp.Server`).

### 3. Session management bypass for 2026-07-28

The compat handler validates sessions (`checkSession`), strips session headers, and manages session resurrection. None of this applies to 2026-07-28 clients (no sessions).

For the 2026-07-28 path: skip `checkSession`, skip session resurrection, skip session termination on DELETE. The JWT-based session ID generation (`sessionIDGenerator`) and validation (`sessionValidator`) are unnecessary.

### 4. Client identity for tool filtering

Today, `FilterTools` receives `sessionID` for scope filtering. With 2026-07-28, there's no session. Tool filtering needs to work with:
- `x-mcp-authorized` header (JWT-based trusted headers) — already session-independent
- `x-mcp-virtualserver` header — already session-independent  
- Discovery scope store — currently keyed by session ID (disabled in stateless mode, future: re-key by `sub`)

The JWT filter and virtual server filter already work without sessions. The only gap is discovery scope, which is already documented as a known limitation.

### 5. Per-request `_meta` fields

2026-07-28 clients send `_meta` on every request with protocol version, client info, and capabilities. The broker's tool handlers receive these via the SDK's `ServerRequest[P]` accessors. The broker doesn't currently read `_meta` — it reads capabilities from the `initialize` handshake. For 2026-07-28, capabilities come per-request.

Impact: URL token elicitation checks `ClientSupportsElicitation()` which reads from the initialize params. For 2026-07-28, it needs to read from `_meta.io.modelcontextprotocol/clientCapabilities`. This is a broker handler change, not a router change.

## Broker as Client — Work Items

### 6. Upstream ping with 2026-07-28

`MCPServer.Ping()` calls `session.Ping(ctx, nil)`. On a 2026-07-28 session, the SDK should automatically add `_meta` fields. The error suggests it doesn't. This needs investigation:

- Is this an SDK bug in v1.7.0-pre.2?
- Does the broker's `DisableStandaloneSSE: true` interfere with protocol negotiation?
- Does the connection actually negotiate 2026-07-28, or does `server/discover` fail and fall back to `initialize` with a version mismatch?

**Action:** Verify with SDK debug logging what protocol version was negotiated. If the SDK isn't adding `_meta` to pings on a 2026-07-28 session, file an SDK issue.

### 7. Upstream tool/prompt listing

The broker calls `session.ListTools()` and `session.ListPrompts()`. These should work on both protocol versions — the SDK handles the transport. The 2026-07-28 responses include `ttlMs` and `cacheScope` which the broker currently ignores (stripped by the compat handler's rewrite). Future work: respect these for principled cache refresh.

### 8. Notification handling

2026-07-28 removes the standalone GET SSE stream. The broker's `startNotificationWatcher` opens a GET connection for `notifications/tools/list_changed`. With `DisableStandaloneSSE: true`, the broker already doesn't rely on this — the manager's periodic re-list backstops freshness. No change needed.

## Dependency Order

```
Item 6 (investigate upstream ping)
  ↓
Item 1 (stop stripping protocol version header)
  ↓
Item 2 (dual StreamableHTTPHandler)
  ↓
Item 3 (session bypass for 2026-07-28)
  ↓
Item 4 (tool filtering without sessions) — mostly done, discovery scope is known limitation
  ↓
Item 5 (_meta capabilities) — needed for URL elicitation in 2026-07-28
```

Items 7 and 8 are low priority — existing behavior is acceptable.

## What This Unblocks

Once items 1-3 are done:
- A 2026-07-28 client can connect to the broker, list tools/prompts, and call them
- The full steel thread works: client → router (header-based) → broker (stateless handler) → upstream (server/discover)
- The e2e tests pass

Items 4-5 are refinements for feature parity (discovery scope, URL elicitation per-request capabilities).
