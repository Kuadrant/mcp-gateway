# SDK Migration: mark3labs/mcp-go → modelcontextprotocol/go-sdk

## Problem

The gateway depends on `github.com/mark3labs/mcp-go` (v0.53.0) for JSON-RPC types, client/server SDK, and HTTP transport. This is a community SDK with no `2026-07-28` tracking issues and a release cadence decoupled from spec milestones.

The official `github.com/modelcontextprotocol/go-sdk` (v1.6.1) is a Tier 1 SDK maintained by the MCP organization. It ships partial `2026-07-28` support (`Mcp-Method`/`Mcp-Name` headers, extensions framework, error code standardization, multi-round-trip requests) with open tracking issues for the remaining SEPs (stateless protocol SEP-2575, TTL for list results SEP-2549). Staying on mark3labs blocks `2026-07-28` adoption.

## Summary

Full replacement of `github.com/mark3labs/mcp-go` with `github.com/modelcontextprotocol/go-sdk/mcp` across broker, router, upstream client, and test servers. No coexistence period. The migration is structured in three phases: types and constants, server/broker, client/upstream. Performance must not regress in the broker's tool/call serving and tools/list aggregation paths.

## Goals

- **G1:** Remove `github.com/mark3labs/mcp-go` from `go.mod` entirely
- **G2:** Zero performance regression in broker tool/call dispatch and tools/list aggregation. Validate with benchmarks before and after
- **G3:** Unblock `2026-07-28` protocol support
- **G4:** Maintain all existing functionality: streamable HTTP server, SSE, session management, tool/prompt registration and filtering, upstream client connections, notification handling
- **G5:** Existing e2e and integration tests pass without behavioral changes

## Non-Goals

- Adopting `2026-07-28` protocol features in this work
- Changing the broker's architecture or component boundaries
- Adopting the official SDK's generic `AddTool[In, Out]` for the broker's dynamic tool registration — upstream-discovered tools have schemas that arrive at runtime via `tools/list`; there are no compile-time Go types to parameterize.
- Migrating to the official SDK's OAuth support

## Design

### Package structure difference

mark3labs splits across four packages (`mcp`, `server`, `client`, `client/transport`). The official SDK consolidates everything into a single `mcp` package. All imports collapse to one import path: `github.com/modelcontextprotocol/go-sdk/mcp`.

This simplifies imports but means type names share a namespace — the official SDK disambiguates with naming conventions (e.g. `Server` vs `Client`, `ServerSession` vs `ClientSession`, `ServerRequest` vs `ClientRequest`).

### Hooks → Middleware

This is the highest-risk area. The mark3labs SDK provides typed, phase-specific hook callbacks (`AddBeforeAny`, `AddAfterListTools`, etc.). The official SDK replaces this with a generic middleware chain.

The broker uses hooks for:

1. **OpenTelemetry tracing** — `AddBeforeAny`/`AddOnSuccess`/`AddOnError` create and close spans around every request.
2. **Tool and prompt filtering** — `AddAfterListTools`/`AddAfterListPrompts` apply JWT authorization, virtual server filtering, session scope filtering, user-specific tool fetching, and gateway metadata cleanup.
3. **Session lifecycle** — `AddOnRegisterSession`/`AddOnUnregisterSession` track sessions for scope management and logging.

The official SDK's `Middleware` wraps a `MethodHandler` — a function `func(ctx, method, Request) (Result, error)`. Middleware intercepts both before and after the inner handler, and can dispatch on the method string (`"tools/list"`, `"prompts/list"`, etc.).

```go
func filteringMiddleware(broker *mcpBrokerImpl) mcp.Middleware {
    return func(next mcp.MethodHandler) mcp.MethodHandler {
        return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
            result, err := next(ctx, method, req)
            if err != nil {
                return result, err
            }
            if method == "tools/list" {
                toolsReq := req.(*mcp.ListToolsRequest)
                toolsRes := result.(*mcp.ListToolsResult)
                // headers available via toolsReq.Extra.Header
                // mutate toolsRes.Tools
            }
            return result, nil
        }
    }
}
```

**Header access.** The filtering handlers need HTTP headers (`x-mcp-authorized`, `x-mcp-virtualserver`) from the incoming request. In mark3labs, headers are on `req.Header`. In the official SDK, headers are on `req.Extra.Header` (`http.Header`), populated by the transport layer for every request including notifications. This affects all filter functions: `applyAuthorizedCapabilitiesFilter`, `applyVirtualServerFilter`, `applyAuthorizedCapabilitiesFilterForPrompts`, `applyVirtualServerFilterForPrompts`, and `applyScopeFilter`.

**Session lifecycle.** mark3labs `OnRegisterSession`/`OnUnregisterSession` hooks don't map to method-level middleware since they fire on connection events, not method calls. Options: `ServerOptions.InitializedHandler` for session registration, or wrapping the `StreamableHTTPHandler` at the HTTP layer. This needs investigation during implementation.

### Server-side migration (broker)

The broker creates an MCP server, registers tools/prompts dynamically, and serves clients via streamable HTTP.

**Server creation** changes from functional options to a struct-based `ServerOptions`. The `Hooks` option is replaced by calling `Server.AddReceivingMiddleware(...)` after creation.

**Dynamic tool registration** uses the low-level `Server.AddTool(tool, handler)` — not the generic `AddTool[In, Out]`. The low-level API does no schema validation on tool calls ("unmarshaling and validating are the caller's responsibility"), which is correct for pass-through tools where validation is the upstream's responsibility.

**InputSchema requirement.** The official SDK panics if `Server.AddTool` receives a tool with nil `InputSchema`. Tools from upstreams should carry schemas from `tools/list`, but if any upstream omits the schema, the broker must provide a default `{"type": "object"}`.

**HTTP server.** The official SDK's `StreamableHTTPHandler` is an `http.Handler`, not a server — the broker manages its own `http.Server` externally. This is cleaner than mark3labs' `StreamableHTTPServer` which wraps the HTTP server.

**Session ID management.** mark3labs uses `WithSessionIdManager(interface)` with the gateway's `JWTManager`. The official SDK uses `ServerOptions.GetSessionID func() string` for ID generation. JWT validation of incoming `Mcp-Session-Id` headers must be handled separately — the SDK validates session existence, but JWT signature verification needs middleware or handler wrapping.

**Built-in tool construction.** mark3labs provides builder helpers (`NewTool`, `WithDescription`, `WithArray`, `NewMetaFromMap`, `NewToolResultError`, `GetArguments`). The official SDK uses struct literals with JSON schema for `InputSchema` and `result.SetError(err)` for errors. More verbose but more explicit. Detailed type-by-type mapping belongs in the implementation tasks.

### Client-side migration (upstream connections)

The broker connects to upstream MCP servers as a client. The official SDK separates `Client` (reusable config) from `ClientSession` (connection).

**Key changes:**
- **Two-step connect.** mark3labs: `NewStreamableHttpClient(url, opts...) → Start → Initialize`. Official SDK: `NewClient(impl, opts)` then `client.Connect(ctx, transport, nil)` — initialize is implicit.
- **Transport configuration.** Functional options (`WithHTTPHeaders`, `WithHTTPBasicClient`, `WithContinuousListening`) become struct fields on `StreamableHTTPClientTransport` (`Header`, `HTTPClient`).
- **Notification handling.** mark3labs uses `client.OnNotification(handler)` post-creation. Official SDK uses `ClientOptions.NotificationHandler` at client creation time. Per-upstream routing must happen inside the handler.
- **Reconnection.** With Client/Session separation, reconnection creates a new `ClientSession` from the same `Client` — no need to reconstruct the client.
- **Connection lost.** mark3labs uses `client.OnConnectionLost(handler)`. The official SDK equivalent needs investigation — likely context cancellation or session lifecycle callbacks.

### Router-side migration

The router uses mcp-go for JSON-RPC types and the client SDK — it instantiates `*client.Client` for downstream connections during tool call routing (lazy init hairpin). It does not use the server SDK. The migration involves type renaming (e.g. `mcp.CallToolRequest` → official SDK equivalent, `mcp.MCPMethod` → string constants) and updating client instantiation to the official SDK's two-step `Client`/`ClientSession` pattern. The router's `encoding/json` unmarshaling of raw body bytes works with official SDK types since they use standard `json` struct tags.

### Performance

**JSON parsing.** The official SDK uses `github.com/segmentio/encoding` for case-sensitive JSON unmarshaling — faster than `encoding/json` because it skips case-insensitive field matching. The broker's hot path (receiving `tools/call`, dispatching to handler) benefits from this.

**Schema validation.** The low-level `Server.AddTool` does NOT validate input against the schema. Pass-through tools skip validation entirely — no per-call overhead for schema checking. The generic `AddTool[In, Out]` does validate, but the broker only uses it for built-in tools (tags, discovery) where the cost is acceptable.

**Net effect:** neutral to slightly positive. No regression expected.

### Test migration

Test servers in `tests/servers/` and `internal/tests/` use the full mark3labs server/client API. These are not performance-sensitive. E2E test helpers in `tests/e2e/mcp_client.go` use the client SDK. All must be migrated, but the patterns are straightforward — see implementation tasks.

## Security Considerations

- **Case-sensitive JSON.** The official SDK's case-sensitive unmarshaling prevents field-shadowing attacks via case variations. Stricter than mark3labs, but low risk since MCP clients follow the spec.
- **Cross-origin protection.** The official SDK has built-in DNS rebinding and Origin header verification. The gateway runs behind Envoy which handles these. Disable SDK-level protection to avoid conflicts.
- **Session ID validation.** The migration must preserve JWT-based session ID validation. The SDK validates session existence but not JWT signatures — the gateway's JWT verification must be maintained.

## Open Questions

1. **Session lifecycle hooks.** How to observe session creation/destruction for scope store cleanup and logging. Middleware only intercepts method calls, not connection lifecycle.
2. **Connection lost callback.** The `client.OnConnectionLost(handler)` pattern for upstream reconnection — what is the official SDK equivalent?
3. **Continuous listening.** Does `StreamableHTTPClientTransport` maintain the SSE stream by default?
4. **Protocol version constant.** `LATEST_PROTOCOL_VERSION` is unexported in the official SDK. The broker uses this during upstream initialization.
5. **Tool Meta field.** The broker stores gateway metadata (`kuadrant/id`, broker tool marker) in `tool.Meta.AdditionalFields`. Verify the official SDK's `Tool` type supports equivalent metadata.
6. **User-specific tool fetching.** `FetchUserSpecificTools()` mutates `ListToolsResult` inside an `AfterListTools` hook. How should this be reimplemented using the official SDK's post-handler middleware pattern?

## Prerequisites

- Official SDK >= v1.7.0 (supports both `2025-11-25` and `2026-07-28` with automatic version negotiation)
- Benchmark baseline for broker tool/call dispatch and tools/list aggregation established before migration begins

## Future Considerations

- Adopting `2026-07-28` protocol features (SEP-2575 stateless protocol, SEP-2549 TTL for list results) in follow-up work

## Relationship to Existing Approaches

- **[router-2026-07-28-design.md](../router-2026-07-28/router-2026-07-28-design.md):** The router's `RoutingTable` and `Router` interfaces are SDK-agnostic. Migration impact on the router is limited to type imports in the `2025-11-25` code path.
- **[mcp-2026-07-28-impact.md](../mcp-2026-07-28-impact.md):** Identifies the SDK dependency as a `2026-07-28` blocker. This migration resolves it.

## Execution

See:
- [tasks/tasks.md](tasks/tasks.md) for the implementation plan (includes detailed type mapping reference)
