# Guardrails Integration — Documentation Plan

Documentation for guardrails integration, organized by user goals.

## User-Facing Guide (`docs/guides/guardrails.md`)

### When I want to protect tool calls with guardrails

When MCP servers interact with sensitive systems, a platform engineer deploying a gateway wants to configure a guardrails server and apply it to their gateway so that dangerous tool calls are rejected before reaching backends.

**Cover:**
- Creating the guardrails Secret (type `guardrails/external/nemo`, required label)
- Setting `mcp.kuadrant.io/guardrails-ref` annotation on MCPGatewayExtension
- Configuring `configIDs`, `model`, `failMode` in the Secret
- Verifying guardrails are active (test a blocked call)

### When I want per-server guardrails policies

When servers have different risk profiles, a platform engineer or MCP server developer managing multiple servers wants to add server-level config IDs alongside the global policy so that each server's guardrails match its capabilities.

**Cover:**
- Setting `mcp.kuadrant.io/guardrails-config-ids` annotation on MCPServerRegistration
- This annotation is an extension only — it requires `mcp.kuadrant.io/guardrails-ref` on the MCPGatewayExtension. Without a gateway-level guardrails config, the server is set to NotReady
- Additive behavior: per-server IDs are merged with gateway defaults, they cannot remove org-wide policies
- Per-server-only model: gateway annotation with empty `configIDs` in Secret, servers supply their own
- YAML examples showing combined configuration

### When I want to understand fail modes

When the guardrails server may become unavailable, a platform engineer configuring gateway resilience wants to understand the trade-offs between fail-closed and fail-open so that they can choose the right policy for their risk tolerance.

**Cover:**
- `failMode: deny` (default) — tool calls rejected when guardrails unreachable
- `failMode: allow` — tool calls proceed without guardrails check
- Translation failures always denied regardless of `failMode`
- Consistency with ext_proc `failure_mode_allow` invariant

### When I need to configure body size limits

When a platform engineer is configuring the gateway for large tool payloads or response bodies, they want to understand the relationship between the gateway's `maxBodyBytes` and Envoy's `per_connection_buffer_limit_bytes` so that requests are not rejected unexpectedly.

**Cover:**
- `maxBodyBytes` on MCPGatewayExtension (default 1 MiB) — controls router-side buffer for streamed body accumulation
- For `2025-11-25` clients: Envoy's `per_connection_buffer_limit_bytes` (default 1 MiB) must be equal to or larger than `maxBodyBytes`, otherwise Envoy rejects the request before the router sees it
- For `2026-07-28` clients: request body is streamed, `maxBodyBytes` is the only limit
- Response bodies: `maxBodyBytes` applies per SSE event (not per stream) or per JSON body
- How to increase: set `maxBodyBytes` on MCPGatewayExtension, and for 2025 clients also adjust the Envoy listener buffer via EnvoyFilter or Gateway API policy

### When I need TLS trust for the guardrails server

When the guardrails server uses a corporate or self-signed CA, a platform engineer setting up secure communication wants to configure TLS trust so that the gateway can reach the guardrails server over HTTPS.

**Cover:**
- Adding the guardrails server CA to the gateway CA bundle (`caCertBundleRef`)
- No separate CA configuration needed
- Non-TLS acceptable for in-cluster communication (log warning emitted)

## API Reference Updates

### `docs/reference/mcpgatewayextension.md`

**Cover:**
- `mcp.kuadrant.io/guardrails-ref` annotation (Secret name in same namespace)
- `maxBodyBytes` spec field (optional, default 1 MiB) — buffer size cap for streamed body accumulation
- Secret requirements: type `guardrails/external/nemo`, label `mcp.kuadrant.io/secret=true`
- Secret key: `config.yaml` (url, configIDs, model, failMode)
- Annotation-based: not visible via `kubectl explain`, validated at reconcile time

### `docs/reference/mcpserverregistration.md`

**Cover:**
- `mcp.kuadrant.io/guardrails-config-ids` annotation (comma-separated config IDs)
- Extension only — not standalone. Requires `mcp.kuadrant.io/guardrails-ref` on MCPGatewayExtension
- Additive to gateway config IDs, cannot override or remove gateway defaults
- NotReady if gateway guardrails annotation is absent

## Security Architecture Update (`docs/design/security-architecture.md`)

### When I need to understand guardrails in the security model

When reviewing or extending the security architecture, a contributor working on the gateway wants to understand guardrails trust boundaries and invariants so that changes preserve the security model.

**Cover:**
- Guardrails is router-only (broker not involved)
- Router does not forward client `Authorization` headers to the guardrails server — NeMo authenticates to its LLM backend via its own `OPENAI_API_KEY`, not client tokens
- Guardrails server deployed without auth (NeMo default), accessed over in-cluster networking
- Fail-closed default consistent with ext_proc failure mode
- No credential leak — guardrails Secret has no access to `credentialRef`, client tokens not exposed to guardrails infrastructure
- One guardrails server per gateway
