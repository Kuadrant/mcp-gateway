# Streamed Body Processing for `2026-07-28`

## Problem

By default, the router uses `BUFFERED` request body mode so it can parse the JSON-RPC body to extract the method and tool name for routing decisions. This is a 2025 protocol constraint. With `2026-07-28` clients, `Mcp-Method` and `Mcp-Name` headers must be sent and carry the needed routing information. Request bodies still need their tool names to be re-written but the routing decision can be made and returned to Envoy before the body is sent.

## Summary

When the ext_proc adapter identifies a `2026-07-28` client in the request headers phase, it responds with a `ModeOverride` switching the request body mode to `STREAMED` and the response body mode to `NONE`. Routing completes entirely in the headers phase. The request body streams through for tool name prefix stripping only.

## Relationship to Router Design

This builds on the [router 2026-07-28 design](router-2026-07-28-design.md). That design established header-based routing and the `Router` interface. This document covers the ext_proc processing mode changes that header-based routing enables.

## Design

### Mode override on request header response

When the adapter receives request headers with `MCP-Protocol-Version: 2026-07-28`, the request header response includes a `ModeOverride`:

```go
responses[0].ModeOverride = &extprochttp.ProcessingMode{
    RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
    ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
    // switches from buffered to streamed when 2026
    RequestBodyMode:     extprochttp.ProcessingMode_STREAMED, 
    // default to none unless a guardrails config exists or the response is an elicitation (existing behaviour)
    ResponseBodyMode:    extprochttp.ProcessingMode_NONE,
    RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
    ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
}
```

`ResponseBodyMode` defaults to `NONE` — the response streams directly from backend to client without ext_proc involvement.

This is already the case. For `2025-11-25` clients, the existing behaviour is unchanged: `BUFFERED` request body for JSON-RPC parsing, `STREAMED` response body for elicitation ID rewriting (opted into when needed).

### Request body: streamed prefix stripping

With `STREAMED` mode, Envoy sends request body chunks to ext_proc as they arrive without waiting for the full body. The router needs the body only for tool name prefix stripping — rewriting `"name": "github_search"` to `"name": "search"` in the JSON-RPC body.

When no prefix is configured for the target server, the request body needs no mutation. The adapter sets `RequestBodyMode` to `NONE`, eliminating the body phase entirely. Header-body name validation is the responsibility of the target MCP server, which the `2026-07-28` spec requires (`HeaderMismatch` rejection).

### Response body: default NONE

For `2026-07-28`, the response body mode defaults to `NONE`. The response streams directly from the backend to the client — ext_proc is not involved. No session ID mapping, no elicitation ID rewriting, no SSE stream parsing.


### Header mutations and body-phase restrictions

Envoy only applies header mutations in body-phase responses when the body mode is `BUFFERED`. Since `2026-07-28` completes all header mutations in the request headers phase (`:authority`, `Mcp-Name`, `x-mcp-*` headers), this restriction does not apply. Body-phase responses contain only `BodyMutation` for prefix stripping, no header changes.

This is a key difference from `2025-11-25`, where `:authority` is set in the body phase response using `BUFFERED` mode with `ClearRouteCache: true` and `allow_all_routing: true` in the EnvoyFilter config.

### Protocol-specific flow

```text
2026-07-28 client (with prefix):
  Request Headers → routing decision + ModeOverride(STREAMED, NONE)
  Request Body    → stream chunks, strip prefix, validate header-body match
  Response Headers→ pass-through
  Response Body   → NONE

2026-07-28 client (no prefix):
  Request Headers → routing decision + ModeOverride(NONE, NONE)
  Request Body    → skipped
  Response Headers→ pass-through
  Response Body   → NONE

2025-11-25 client:
  Request Headers → protocol selection only
  Request Body    → BUFFERED, parse JSON-RPC, routing decision, :authority set
  Response Headers→ session ID rewrite
  Response Body   → STREAMED (elicitation ID rewriting)
```

## Future Considerations

### Response body streaming for guardrails

When guardrails are configured, the router sets `ResponseBodyMode` to `FULL_DUPLEX_STREAMED`, overriding `NONE` in the protocol-specific flow above. This applies to both `2026-07-28` and `2025-11-25` clients. For SSE responses, the router processes per-event using SSE framing. For JSON responses, the router accumulates the full body. See the [guardrails design](../guardrails/guardrails-design.md) for the buffering model and size cap behaviour.
