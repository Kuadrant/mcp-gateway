# Guardrails Integration — Documentation Plan

Documentation for guardrails integration, organized by user goals.

## User-Facing Guide (`docs/guides/guardrails.md`)

### When I want to protect tool calls with guardrails

When a platform engineer wants to enforce safety policies on MCP tool calls, they want to configure a guardrails server and apply it to their gateway so that all tool calls are checked before reaching backends.

**Cover:**
- Creating the guardrails Secret (type `guardrails/external/nemo`, required label)
- Setting `guardrailsRef` on MCPGatewayExtension
- Configuring `configIDs`, `model`, `failMode`, `timeoutSeconds`
- Adding the `bearer-token` for authentication
- Verifying guardrails are active (test a blocked call)

### When I want per-server guardrails policies

When a platform engineer or MCP server developer wants different guardrails policies for specific servers, they want to add server-level guardrails configuration alongside the global policy.

**Cover:**
- Setting `guardrailsRef` on MCPServerRegistration
- Additive behavior: global + per-server config IDs merged when same URL
- Two separate checks when different URLs
- YAML examples showing combined configuration

### When I want to understand fail modes

When a platform engineer wants to decide how the gateway behaves when the guardrails server is down, they want to understand the trade-offs between fail-closed and fail-open.

**Cover:**
- `failMode: deny` (default) — tool calls rejected when guardrails unreachable
- `failMode: allow` — tool calls proceed without guardrails check
- Consistency with ext_proc `failure_mode_allow` invariant
- When to use each mode

### When I need TLS trust for the guardrails server

When a platform engineer deploys the guardrails server behind a private CA, they want to configure TLS trust.

**Cover:**
- Adding the guardrails server CA to the gateway CA bundle (`caCertBundleRef`)
- No separate CA configuration needed in the guardrails Secret

## API Reference Updates

### `docs/reference/mcpgatewayextension.md`

**Cover:**
- `guardrailsRef` field (optional, SecretReference)
- Secret requirements: type `guardrails/external/nemo`, label `mcp.kuadrant.io/secret=true`
- Secret keys: `config.yaml` (url, configIDs, model, failMode, timeoutSeconds), `bearer-token`
- Relationship to per-server `guardrailsRef` (additive)

### `docs/reference/mcpserverregistration.md`

**Cover:**
- `guardrailsRef` field (optional, SecretReference)
- Additive to global guardrails
- Same Secret format as gateway-level

## Security Architecture Update (`docs/design/security-architecture.md`)

### When I need to understand guardrails in the security model

When a contributor needs to understand how guardrails fits into the security architecture, they want to know the trust boundaries and invariants.

**Cover:**
- Guardrails is a router-only concern (broker not involved)
- Bearer token stored in Secret, never exposed to clients
- Fail-closed default consistent with ext_proc failure mode
- Guardrails checks happen after AuthPolicy (authenticated request, then policy check)
- No credential leak — guardrails Secret has no access to `credentialRef`
