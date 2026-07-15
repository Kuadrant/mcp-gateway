# Router 2026-07-28 Documentation Plan

Documentation for the 2026-07-28 stateless protocol support, organized by user goals.

## User-Facing Guide (`docs/guides/protocol-modes.md`) — DONE

### When I want to run a stateless MCP gateway

When a platform engineer has upstream MCP servers that support the 2026-07-28 protocol, they want to configure the gateway in stateless mode so that routing uses headers instead of sessions and the gateway no longer manages backend session lifecycle.

**Cover:**
- Setting `protocolMode: Stateless` on MCPGatewayExtension
- Verifying the deployment picks up `--protocol-mode=stateless`
- Behaviour differences (no sessions, no hairpin init, header-body validation)
- Limitations (discovery tools disabled)

### When I need to support clients on different protocol versions

When a platform engineer is migrating from 2025-11-25 to 2026-07-28 and has clients using both protocols, they want to run both protocol modes simultaneously so that existing clients are not disrupted during the transition.

**Cover:**
- Deploying separate MCPGatewayExtension resources on different listeners
- Each instance gets its own broker-router deployment
- Both instances can register the same upstream servers

## API Reference Update (`docs/reference/mcpgatewayextension.md`) — DONE

### When I need the exact field name and allowed values for protocol mode

When a platform engineer is writing MCPGatewayExtension YAML, they want to know the spec for `protocolMode`.

**Cover:**
- `protocolMode` field (optional, default `Stateful`)
- Allowed values: `Stateful`, `Stateless`
- Relationship to `sessionStore` (unused in stateless mode)

## Design Doc Updates

### When I want to understand the routing architecture for 2026-07-28

When a contributor needs to modify the router or add protocol-specific behaviour, they want to understand how the two protocol paths are isolated.

**Cover:**
- `Router` interface and two implementations (`Router202511`, `Router202607`)
- `ExtProcAdapter` protocol branching by `MCP-Protocol-Version` header
- `ResponseHandler` split (202511 with session mapping vs 202607 pass-through)
- `RoutingTable` shared between both implementations
- Future: Praxis adapter, body phase skip optimization

### When I want to understand what is not yet supported in stateless mode

When a contributor is planning follow-up work, they want to know what was explicitly deferred.

**Cover:**
- Discovery tools (`discover_tools`/`select_tools`) disabled — scope store keyed by session ID
- Future: re-key scope store by identity (`sub` claim) to restore discovery without sessions
- URL token elicitation adaptation (cache key migration from session to identity)
- `server/discover` broker integration (replaces `initialize` for capability detection)
- Broker `ttlMs`/`cacheScope` support for principled refresh
