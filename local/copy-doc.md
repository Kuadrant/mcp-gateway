# MCP Router Migration to Wasm Filter

## Overview

The current MCP Gateway uses an Envoy External Processor (ext_proc) for routing MCP requests. This document proposes replacing ext_proc with a combination of Envoy's native MCP filter and a custom Wasm filter to reduce latency and simplify the architecture.

### Current Architecture

```
Client → Envoy → ext_proc (gRPC, port 50051) → Envoy → Upstream
```

The ext_proc handles:
- JSON-RPC parsing and validation
- Tool name extraction and prefix stripping
- Broker queries for tool→server resolution
- Session management (gateway↔upstream mapping)
- Header manipulation (`:authority`, `:path`, credentials)

**Why ext_proc was used**: MCP routing requires reading the request body (to extract the tool name) before setting routing headers (`:authority`, `:path`). The ext_proc protocol naturally supports this - it receives headers, then body, and can modify both before forwarding. In contrast, Wasm filters historically had to forward headers before the body was available, making body-based routing decisions impossible.

**What changed**: Envoy v1.35.0 introduced `allow_on_headers_stop_iteration` for Wasm filters, enabling them to buffer headers until body processing completes. This removes the technical barrier that required ext_proc.

**Problem**: Every request requires a gRPC round-trip to ext_proc, adding latency. Having a separate service also increases complexity around debugging and observability.

### Tradeoffs

| Approach | Body Parsing | Latency | Complexity |
|----------|--------------|---------|------------|
| ext_proc only | Once | Higher (gRPC round-trip) | External service |
| MCP Filter + Wasm | Twice (MCP parses, Wasm re-parses for rewrite) | Lower (in-process) | Filter chain |

The MCP Filter + Wasm approach may parse the body twice: once in the MCP filter (to extract tool name to metadata) and again in the Wasm filter (to rewrite the tool name when stripping prefix). This is only an issue when prefix stripping is required.

**Mitigation options:**
1. Skip MCP filter - let Wasm handle all parsing (single parse, but loses native C++ validation)
2. Accept double-parse as cost of lower latency (parsing JSON is fast)

### Proposed Architecture

```
Client → Envoy → MCP Filter → WASM Routing Filter → (optional Kuadrant → Guardrails) → Upstream
```

- **MCP Filter**: Native C++ filter parses JSON-RPC, validates, extracts tool name to metadata
- **WASM Routing Filter**: In-process filter handles routing decisions, body rewriting, header manipulation
- **No ext_proc**: Eliminates gRPC round-trip latency and complexity

## Proposal

### Filter Chain

1. **MCP Filter** - Parse JSON-RPC, validate, extract tool name to `mcp_proxy` metadata
2. **MCP Routing Wasm** - Route decisions, body rewrite (if prefix), set headers
3. **Kuadrant (optional)** - AuthPolicy and RateLimitPolicy on routed request
4. **Guardrails (optional)** - LLM safety checks
5. **Router** - Forward to upstream

### Data Flow

```
tools/call "weather_get_forecast"
    │
    ├─ MCP Filter: validate MCP payload, extract tool name inject into metadata
    ├─ Wasm: read JWT for prefix→server mapping (in-memory)
    ├─ Wasm: read controller managed configuration for backend server config (in-memory)
    ├─ Wasm: in-memory look up or gRPC call to Redis wrapper for upstream session ID
    ├─ Wasm: read and rewrite body: only if stripping prefix is needed 
    └─ Wasm: set routing headers → Upstream
```

### Data Sources

| Data | Source | When Loaded |
|------|--------|-------------|
| Prefix → server_id | JWT session claims | Set during `initialize` in the broker component |
| Server config | EnvoyFilter `configuration` | Wasm startup/reload |
| Upstream session ID | Redis (if configured, via gRPC wrapper) | Per tool call |

### Key Optimizations

1. **No broker lookup**: Prefix→server mapping stored in JWT, server config in EnvoyFilter configuration
2. **Conditional body rewrite**: Only parse/rewrite body when prefix is set (conflicts exist)
3. **In-process execution**: Wasm runs inside Envoy, no gRPC round-trip

### New Components Required

1. **Redis gRPC Wrapper**: Thin service exposing session operations over gRPC
2. **Wasm Filter**: Rust or Go implementation of routing logic

### What Remains External

| Component | Purpose |
|-----------|---------|
| Broker    | Only for `initialize` and other none tool calls (aggregates tools list, issues JWT) |
| Redis gRPC| Session state wrapper, access from Wasm if configured |

## Appendix

### Wasm vs ext_proc Comparison

| Feature | ext_proc | Wasm |
|---------|----------|------|
| Body modification  | ✅ | ✅ |
| JSON parsing       | ✅ | ✅ |
| Ext HTTP calls     | ✅ | ✅ |
| Ext gRPC calls     | ✅ | ✅ |
| In-process         | ❌ | ✅ |

### Header/Body Buffering Limitation

The proxy-wasm spec (v0.2.1) requires forwarding headers before the body is available. For MCP routing, we need to read the body (tool name) before setting routing headers.

**Workaround**: Use `allow_on_headers_stop_iteration` in Envoy's Wasm PluginConfig to buffer headers until body processing completes.

**Requires Envoy v1.35.0 or later.**

```yaml
http_filters:
  - name: envoy.filters.http.wasm
    typed_config:
      "@type": type.googleapis.com/envoy.extensions.filters.http.wasm.v3.Wasm
      config:
        name: "mcp-routing"
        allow_on_headers_stop_iteration:
          value: true
        vm_config:
          code:
            local:
              filename: "/etc/envoy/mcp-routing.wasm"
```

Since Istio's `WasmPlugin` resource does not expose `allow_on_headers_stop_iteration`, we use `EnvoyFilter` for all Wasm configuration. This consolidates everything into a single resource.

### EnvoyFilter Configuration

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: mcp-routing-wasm
  namespace: gateway-system
spec:
  workloadSelector:
    labels:
      istio: mcp-gateway
  configPatches:
  - applyTo: HTTP_FILTER
    match:
      context: GATEWAY
      listener:
        filterChain:
          filter:
            name: envoy.filters.network.http_connection_manager
            subFilter:
              name: envoy.filters.http.router
    patch:
      operation: INSERT_BEFORE
      value:
        name: envoy.filters.http.wasm
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.http.wasm.v3.Wasm
          config:
            name: "mcp-routing"
            root_id: "mcp_routing"
            allow_on_headers_stop_iteration:
              value: true
            configuration:
              "@type": type.googleapis.com/google.protobuf.StringValue
              value: |
                {
                  "servers": {
                    "weather-server": {
                      "hostname": "weather.mcp.local",
                      "path": "/mcp",
                      "credentials": "Bearer xxx"
                    },
                    "github-server": {
                      "hostname": "api.githubcopilot.com",
                      "path": "/mcp",
                      "credentials": "Bearer yyy"
                    }
                  },
                  "redisGrpcEndpoint": "redis-grpc-wrapper.mcp-system.svc:50051"
                }
            vm_config:
              runtime: envoy.wasm.runtime.v8
              code:
                remote:
                  http_uri:
                    uri: "https://registry.example.com/mcp-routing-filter:v1"
                    timeout: 10s
```

This EnvoyFilter:
- Sets `allow_on_headers_stop_iteration` for header/body buffering
- Provides server configuration via the `configuration` field
- Loads the Wasm binary from a remote registry

### Tool→Server Mapping Storage

**Default: JWT Session Claims**

Storing the tool→server mapping in the JWT is the default approach. This conservatively supports up to ~200 tool mappings while staying within common HTTP header size limits. The broker would be responsible for setting this in the session JWT.

```json
{
  "exp": 1234567890,
  "tools": {
    "weather_overview": "weather-server",
    "rain_hours": "weather-server-2",
    "github_tool": "github-server"
  }
}
```

**Alternative: Redis**

Redis is already an option for sessions. So if Redis is configured for the gateway, the tool→server mapping can be stored there instead. This supports unlimited mappings and is recommended for deployments with many tools. The Wasm filter would query Redis for the mapping.

### Redis gRPC Wrapper API

The gRPC wrapper would only expose session and tool mapping capabilities backed by redis as the storage layer.


## References

- [Envoy MCP Filter](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/mcp_filter)
- [proxy-wasm spec v0.2.1](https://github.com/proxy-wasm/spec/blob/main/abi-versions/v0.2.1/README.md)
- [Envoy Wasm proto (v1.35.0)](https://www.envoyproxy.io/docs/envoy/v1.35.0/api-v3/extensions/wasm/v3/wasm.proto)
- [proxy-wasm StopIteration issue](https://github.com/proxy-wasm/spec/issues/63)

