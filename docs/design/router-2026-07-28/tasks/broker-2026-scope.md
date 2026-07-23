# Broker 2026-07-28 Protocol Support — Scope

## Problem (resolved)

The broker is an MCP server from the client's perspective and an MCP client to upstream servers. Both sides now support 2026-07-28:

1. **Broker as server** (client-facing `/mcp`): a `protocolRouter` dispatches 2026-07-28 requests to a stateless SDK handler, bypassing the compat handler entirely. 2025-11-25 clients still go through the compat handler. Both protocols work on the same gateway.

2. **Broker as client** (upstream connections): stateless upstreams skip `Ping()` (SDK bug workaround). `ListTools` and `ListPrompts` work on both protocol versions via the SDK session.

**Remaining gap:** URL elicitation doesn't work for 2026-07-28 clients — see Item 5.

## Broker as Server — Work Items

### 1. ✓ Protocol version dispatch

`protocolRouter` (`http_compat.go:72-103`) dispatches by `MCP-Protocol-Version` header or path suffix (`/stateless`, `/stateful`). 2026-07-28 requests bypass the compat handler entirely. The compat handler still strips the version header for 2025-11-25 requests.

### 2. ✓ Dual StreamableHTTPHandler

Two SDK handlers: `legacyHandler` (stateful, wrapped by compat handler) and `statelessHandler` (`Stateless: true`). Both share the same `mcp.Server` instance via the factory function. `protocolRouter` selects the handler before dispatch.

### 3. ✓ Session management bypass

2026-07-28 requests go directly to `statelessHandler`, bypassing `checkSession`, session resurrection, DELETE termination, and JWT session validation.

### 4. ✓ Tool filtering without sessions

`FilterTools` uses `x-mcp-authorized` and `x-mcp-virtualserver` headers (session-independent). For stateless requests, `sessionID` is empty string — the discovery scope filter is a no-op. Known limitation: discovery scope doesn't work for 2026-07-28 clients.

### 5. Per-request `_meta` fields

2026-07-28 clients send `_meta` on every request with protocol version, client info, and capabilities. For 2026-07-28, capabilities come per-request rather than from an `initialize` handshake.

Impact: The **router** response handler (`response.go:73`) records `ClientSupportsElicitation()` from the `initialize` response and stores it in the session cache via `SetClientElicitation`. This flag never gets set for 2026-07-28 clients (no initialize, no session). Without it, the router won't trigger URL elicitation for 2026-07-28 clients.

Fix: On the 2026-07-28 path, read `elicitation` capability from `_meta.io.modelcontextprotocol/clientCapabilities` per-request in the router, rather than relying on a session cache flag set during initialize.

## Broker as Client — Work Items

### 6. ✓ Upstream ping

Stateless upstreams (`UsesStatelessProtocol() == true`) skip `Ping()` entirely — returns nil. This works around the SDK bug where `_meta` fields aren't added to pings on 2026-07-28 sessions. The manager's periodic re-list backstops liveness detection.

### 7. ✓ Upstream tool/prompt listing

`ListTools` and `ListPrompts` work on both protocol versions via the SDK session. The 2026-07-28 responses include `ttlMs` and `cacheScope` which the broker currently ignores. Future work: respect these for principled cache refresh.

### 8. Notification handling (partially done)

Custom `notificationWatcher` adapted for stateless upstreams (empty sessionID). Still uses the GET SSE pattern. `DisableStandaloneSSE: true` remains — the manager's periodic re-list backstops freshness.

## Dependency Order

```text
Item 6 (investigate upstream ping) ✓ done — stateless upstreams skip ping
  ↓
Item 1 (stop stripping protocol version header) ✓ done — protocolRouter bypasses compat handler for 2026
  ↓
Item 2 (dual StreamableHTTPHandler) ✓ done — stateless + legacy handlers with protocolRouter dispatch
  ↓
Item 3 (session bypass for 2026-07-28) ✓ done — stateless path skips checkSession, resurrection, JWT validation
  ↓
Item 4 (tool filtering without sessions) ✓ done — filters use headers, sessionID is empty string for stateless (no-op scope filter)
  ↓
Item 5 (_meta capabilities) — NOT DONE — elicitation still reads from config, not per-request _meta
```

Items 7 and 8 are low priority — existing behavior is acceptable.
- Item 7: ✓ done — ListTools/ListPrompts work on both protocols via the SDK session
- Item 8: partially done — custom notificationWatcher adapted for stateless (empty sessionID), still uses GET SSE approach

## What This Unblocks

Items 1-4, 6-7 are done:
- A 2026-07-28 client can connect to the broker, list tools/prompts, and call them
- The full steel thread works: client → router (header-based) → broker (stateless handler) → upstream (server/discover)
- The e2e tests pass

Item 5 is the remaining refinement: URL elicitation needs to read capabilities from per-request `_meta` for 2026-07-28 clients.
