# Body Parsing for AI Workloads

## Assumptions

- Envoy is the underlying proxy technology for all use cases discussed
- Gateway API is the Kubernetes-based API for defining gateway deployments and routing

## Problem

Multiple components independently parse request/response bodies to extract routing information. The MCP Gateway and BBR (Body Based Routing) each run as separate ext_proc filters, each parsing the body independently. Guardrails (e.g., NeMo) plugs into BBR as a plugin. This introduces redundant deserialization, buffering, gRPC round-trip latency per ext_proc, and deployment complexity.

Key use cases requiring body access:
- **MCP Gateway**: routes by JSON-RPC `method` and `params.name` (tool name) sets :authority :path and custom headers runs pre router
- **BBR (inference routing)**: routes by `model` field in OpenAI-format requests, translates between API formats (OpenAI ↔ Anthropic/Azure/Bedrock/Vertex), injects provider credentials
- **Token rate limiting**: extracts `/usage/total_tokens` from response body
- **Prompt guardrails**: reads `messages` array, calls external service (e.g., NeMo) for allow/block decision. This is a BBR plugin — it consumes the body internally and does not need to expose values to other filters.


**Parse->Protect->Process**


The first three use cases follow the same pattern: parse JSON body, extract values, make them available to downstream Envoy filters for routing and policy before doing any heavy lifting. Guardrails is different — it needs the body content but acts on it within the BBR ext_proc so doesn't need to parse the body again.

Filters that need to parse the body as complete JSON (`json_to_metadata`, Wasm) force Envoy to buffer the full body in memory. This means the `per_connection_buffer_limit_bytes` (default 1MB, configured on the Listener) constrains body size solely because the parsing filter is present — requests that would otherwise stream through unbuffered now hit a size ceiling. Large LLM requests (multi-turn conversations with long context) could exceed this limit and be rejected, creating failures that only exist because of the body-parsing filter. ext_proc avoids this by supporting `body_mode: STREAMED`, allowing chunk-by-chunk processing without full body buffering.

## Preferred Approach: `json_to_metadata` + ext_proc

Envoy's core [`json_to_metadata`](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/json_to_metadata_filter) filter parses JSON bodies and extracts values into dynamic metadata — no custom code required. Protocol-specific ext_procs then read metadata (instead of re-parsing the body) and set routing headers.

### Why metadata, not headers?

Envoy route matching (host/path rewriting) requires headers. But auth, rate limiting, RBAC, access logging, and ext_proc can all read dynamic metadata directly. Only routing decisions need headers, and the ext_proc already runs for protocol-specific work — it can set routing headers from metadata without touching the body.

### Configuration

```yaml
# Envoy filter chain
- name: envoy.filters.http.json_to_metadata
  typed_config:
    "@type": type.googleapis.com/envoy.extensions.filters.http.json_to_metadata.v3.JsonToMetadata
    request_rules:
      rules:
      # MCP: extract method
      - selectors:
        - key: method
        on_present:
          metadata_namespace: ai.gateway
          key: rpc_method
      # MCP: extract tool name (nested: params.name)
      - selectors:
        - key: params
        - key: name
        on_present:
          metadata_namespace: ai.gateway
          key: tool_name
      # OpenAI: extract model
      - selectors:
        - key: model
        on_present:
          metadata_namespace: ai.gateway
          key: llm_model
      # OpenAI: extract stream flag
      - selectors:
        - key: stream
        on_present:
          metadata_namespace: ai.gateway
          key: stream_mode
    response_rules:
      rules:
      # Token usage (non-streaming responses)
      - selectors:
        - key: usage
        - key: total_tokens
        on_present:
          metadata_namespace: ai.gateway
          key: total_tokens
```

MCP and OpenAI request fields don't overlap, so all rules can be applied to every request. Missing keys are silently ignored (no metadata set). Per-route configuration is also supported — rules can be scoped to specific virtual hosts or routes.

### Who consumes metadata

| Consumer | How it reads metadata | What it uses |
|---|---|---|
| ext_proc (MCP/BBR) | `ProcessingRequest.metadata_context` | `rpc_method`, `tool_name`, `llm_model` → sets routing headers |
| Rate limiting | `metadata` descriptor type | `total_tokens` as `hits_addend` |
| ext_authz | Metadata matching | `rpc_method`, `llm_model` for policy selection |
| RBAC | Metadata matcher | Fine-grained access control per method/model |
| Access logging | `%DYNAMIC_METADATA%` | Request classification in logs |

## Request Flow

```
Phase 1: json_to_metadata (native Envoy, every request)
├── Parse JSON body once
├── Extract configured keys into dynamic metadata
└── No custom code, no gRPC, no Wasm

Phase 2: Auth + Rate Limiting (Envoy-native filters)
├── Auth filters read metadata directly for policy decisions
└── Rate limiting reads metadata for descriptors (e.g., total_tokens)

Phase 3: Protocol-specific ext_proc
├── Reads metadata (not body) for routing decisions
├── Sets routing headers (:authority, :path, x-mcp-method, etc.)
├── Parses body ONLY for mutation cases
└── Protocol-specific work (session mgmt, API translation, etc.)
```

## MCP ext_proc Changes

With body values pre-extracted into metadata, the MCP ext_proc reads `metadata_context` instead of parsing the request body for routing. It only parses the body for the rare mutation cases.

### What stays in ext_proc (every request)

- **Request headers**: session lookup, lazy init hairpin setup
- **Request metadata**: read `rpc_method`, `tool_name` from `ai.gateway` namespace → set routing headers
- **Response headers**: `mcp-session-id` reverse mapping, 404 session cleanup
- **Response body**: SSE rewriting for elicitation ID mapping (tool calls with elicitation support only)

### Request body parsing (conditional, not every request)

The ext_proc checks metadata (`rpc_method`, `tool_name`) to decide whether body parsing is needed. Only two cases require it:

1. **`tools/call` with a prefixed tool name** — strip the server prefix from the tool name in the body (e.g., `weather_get_forecast` → `get_forecast`)
2. **Elicitation response** — remap the gateway elicitation ID to the backend elicitation ID in the body

All other methods (initialize, tools/list, notifications) pass through without body parsing.

### Summary

| ext_proc phase | Always | Conditional |
|----------------|--------|-------------|
| Request headers | session lookup, init setup | |
| Request metadata | read routing fields, set headers | |
| Request body | | prefix strip (tools/call + prefix), elicitation ID remap |
| Response headers | session ID mapping, 404 handling | |
| Response body | | SSE elicitation rewriting |

## Response Token Extraction

OpenAI chat completions support streaming and non-streaming responses, controlled by `stream` (boolean) in the request body:

| Response type | Format | Where `usage` appears |
|---|---|---|
| Non-streaming (`stream: false`, default) | Single JSON response | Top-level `usage` object, always present |
| Streaming (`stream: true` + `stream_options.include_usage: true`) | SSE chunks (`data: {…}`) | Final data chunk before `[DONE]`, with `choices: []` |
| Streaming (`stream: true`, no `include_usage`) | SSE chunks | Usage not available |

`json_to_metadata` handles non-streaming responses directly (see `response_rules` config above). For streaming responses, the token count appears only in the final SSE data chunk before `[DONE]`. This requires per-chunk processing that `json_to_metadata` does not support — a Wasm filter or ext_proc is needed for the streaming case.

Non-streaming response bodies are bounded by Envoy's `per_codec_buffer_limit` (default 1MB) regardless of filter type.

## Alternative: Wasm Body-to-Headers Filter

A generic Wasm filter (Go 1.24+ WASI) that parses JSON bodies and sets headers directly, bypassing metadata. This was the original proposal before discovering `json_to_metadata`.

### When Wasm might be preferred

- If `json_to_metadata` doesn't support the required selector depth or patterns
- If headers are needed at a filter chain position before ext_proc runs
- For streaming response body processing (token extraction from SSE chunks)

### Configuration

```yaml
hosts:
  - match: "mcp.example.com"
    extractions:
      - jsonPath: "$.method"
        header: "x-rpc-method"
      - jsonPath: "$.params.name"
        header: "x-tool-name"
  - match: "llm.example.com"
    extractions:
      - jsonPath: "$.model"
        header: "x-llm-model"
```

The filter checks `:authority` against configured hosts. Non-matching hosts skip body processing entirely.

### Tradeoffs vs `json_to_metadata`

| Dimension | `json_to_metadata` | Wasm filter |
|---|---|---|
| Custom code | None | Go Wasm module to build/maintain |
| Body copy | Envoy-internal (no copy to external process) | Copied into Wasm VM linear memory |
| Output | Dynamic metadata | Headers (directly usable for routing) |
| Streaming support | No | Yes (per-chunk processing via `on_response_body`) |
| Selector syntax | Chained key selectors | gjson dot-notation or JSONPath |
| Per-route config | Native support | Must be implemented |
| Maturity | Core Envoy filter | Custom |

## Open Questions

- Does `json_to_metadata` support `allow_content_types` for SSE (`text/event-stream`) or only `application/json`?
- Can the ext_proc access metadata set by `json_to_metadata` earlier in the filter chain? (Expected: yes, via `metadata_context`)
- For streaming token extraction: Wasm filter, or handle in the protocol-specific ext_proc?
- Configuration management: how are `json_to_metadata` rules configured in a Kubernetes/Istio environment — EnvoyFilter CR?

## Prior Art: BBR `body-field-to-header` Plugin

BBR already has a generic [`body-field-to-header`](https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/pkg/bbr/plugins/bodyfieldtoheader/body_field_to_header.go) plugin that does exactly this pattern — takes a configurable `fieldName` (JSON body key) and `headerName`, extracts the value, sets the header. It's the default BBR plugin, used to extract `model` → `X-Gateway-Model-Name`.

This confirms the community has identified body-field-to-header as a reusable, declarative pattern. The difference is where it runs: currently inside the BBR ext_proc (after full body parse + gRPC round-trip), vs. potentially in `json_to_metadata` (native Envoy, no ext_proc needed for extraction).

## References

- [`json_to_metadata` filter docs](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/json_to_metadata_filter) — core Envoy filter for JSON body extraction
- [`json_to_metadata` proto API](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/json_to_metadata/v3/json_to_metadata.proto) — full configuration reference
- [MCP Gateway router-to-wasm analysis](https://github.com/maleck13/mcp-gateway/blob/mcp-wasm/docs/design/router-to-wasm-filter.md) — Go 1.24+ WASI approach, proves body mutation works in Wasm
- [BBR ext_proc](https://github.com/kubernetes-sigs/gateway-api-inference-extension/tree/main/pkg/bbr) — plugin-based body routing for inference
- [wasm-shim token usage](https://github.com/Kuadrant/wasm-shim/blob/main/src/kuadrant/pipeline/tasks/token_usage.rs) — response body token extraction in Wasm
- [AI Gateway payload processing](https://github.com/opendatahub-io/ai-gateway-payload-processing) — BBR plugins for API translation and guardrails
