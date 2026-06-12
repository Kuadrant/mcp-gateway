# Gateway CA Certificate Bundle — Documentation Plan

Documentation for the gateway-level CA certificate bundle, organized by user goals. Each section maps to a guide or doc update.

## User-Facing Guide Update (`docs/guides/custom-ca-certificates.md`)

### When I have many upstream servers behind the same private CA

When a platform engineer has multiple upstream MCP servers behind a shared CA (e.g. OpenShift service-serving CA, a shared cert-manager issuer), they want to configure the CA once at the gateway level so they don't need to create and maintain separate CA Secrets for each MCPServerRegistration.

**Cover:**
- Adding `caCertBundleRef` to MCPGatewayExtension
- Creating the CA bundle Secret with required label
- How servers automatically trust the gateway-level CA
- When to use gateway bundle vs per-server `caCertSecretRef`
- Comparison table: gateway bundle vs per-server CA

### When I need to rotate a shared CA certificate

When a platform engineer needs to rotate the CA certificate used by all upstream servers, they want to update a single Secret and have all servers pick up the new CA.

**Cover:**
- Updating the CA bundle Secret
- Propagation timing (~60-120s kubelet volume sync of config Secret)
- How to verify the new CA is active (broker logs, server status)
- Comparison with per-server CA rotation (N updates vs 1)

### When I have servers with unique CAs alongside a shared CA

When a platform engineer has most servers behind a shared CA but some servers using unique CAs, they want to combine gateway-level and per-server CA configuration.

**Cover:**
- Setting `caCertBundleRef` on MCPGatewayExtension for the shared CA
- Setting `caCertSecretRef` on specific MCPServerRegistrations for unique CAs
- Additive trust pool behavior (per-server appends, never replaces)
- YAML examples showing combined configuration

## API Reference Update (`docs/reference/mcpgatewayextension.md`)

### When I need to know the exact field names and types

When a platform engineer is writing MCPGatewayExtension YAML, they want to know the exact API surface for `caCertBundleRef`.

**Cover:**
- `caCertBundleRef` object (optional)
- `caCertBundleRef.name` field (required, Secret name)
- `caCertBundleRef.key` field (optional, defaults to `ca.crt`)
- Secret requirements: must have `mcp.kuadrant.io/secret=true` label
- Size limit: 256 KiB max
- Config Secret size considerations: gateway bundle replaces N per-server copies with 1
- Relationship to per-server `caCertSecretRef` (additive, not replacing)

## AGENTS.md Update

### When an AI agent needs to understand the CA trust model

When an AI agent is working with this codebase, it needs to understand how the two-tier CA trust model works.

**Cover:**
- `MCPGatewayExtension.caCertBundleRef` field description
- How it interacts with `MCPServerRegistration.caCertSecretRef`
- Trust pool hierarchy: system roots → gateway bundle → per-server CA
- Config secret integration: `gatewayCACertPEM` field in `config.yaml`
