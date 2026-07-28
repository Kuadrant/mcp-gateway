# Guardrails Integration — Documentation Plan

Documentation for guardrails integration, organized by user goals.

## User-Facing Guide (`docs/guides/guardrails.md`)

### When I want to protect tool calls with guardrails

When MCP servers interact with sensitive systems, a platform engineer deploying a gateway wants to configure a guardrails server and apply it to their gateway so that dangerous tool calls are rejected before reaching backends.

**Cover:**
- Creating the guardrails Secret (type `guardrails/external/nemo`, required label)
- Setting `guardrailsRef` on MCPGatewayExtension
- Configuring `configIDs`, `model`, `failMode`
- Verifying guardrails are active (test a blocked call)

### When I want per-server guardrails policies

When servers have different risk profiles, a platform engineer or MCP server developer managing multiple servers wants to add server-level config IDs alongside the global policy so that each server's guardrails match its capabilities.

**Cover:**
- Setting `guardrailsConfigIDs` on MCPServerRegistration (not `guardrailsRef`)
- Additive behavior: global + per-server config IDs merged into a single request
- Per-server-only model: gateway `guardrailsRef` with empty `configIDs`, servers supply their own
- Requirement: `guardrailsRef` must exist on MCPGatewayExtension — without it, server is NotReady
- YAML examples showing combined configuration

### When I want to understand fail modes

When the guardrails server may become unavailable, a platform engineer configuring gateway resilience wants to understand the trade-offs between fail-closed and fail-open so that they can choose the right policy for their risk tolerance.

**Cover:**
- `failMode: deny` (default) — tool calls rejected when guardrails unreachable
- `failMode: allow` — tool calls proceed without guardrails check
- Translation failures always denied regardless of `failMode`
- Consistency with ext_proc `failure_mode_allow` invariant

### When I need TLS trust for the guardrails server

When the guardrails server uses a corporate or self-signed CA, a platform engineer setting up secure communication wants to configure TLS trust so that the gateway can reach the guardrails server over HTTPS.

**Cover:**
- Adding the guardrails server CA to the gateway CA bundle (`caCertBundleRef`)
- No separate CA configuration needed
- Non-TLS acceptable for in-cluster communication (log warning emitted)

## API Reference Updates

### `docs/reference/mcpgatewayextension.md`

**Cover:**
- `guardrailsRef` field (optional, SecretReference)
- Secret requirements: type `guardrails/external/nemo`, label `mcp.kuadrant.io/secret=true`
- Secret key: `config.yaml` (url, configIDs, model, failMode)
- `configIDs` is optional — may be empty for per-server-only policies

### `docs/reference/mcpserverregistration.md`

**Cover:**
- `guardrailsConfigIDs` field (optional, list of strings)
- Additive to global guardrails config IDs, merged into single request
- Requires `guardrailsRef` on MCPGatewayExtension — NotReady without it

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
