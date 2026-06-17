# Gateway-Level CA Certificate Bundle

## Problem

With custom TLS support ([#659](https://github.com/Kuadrant/mcp-gateway/issues/659), [#1008](https://github.com/Kuadrant/mcp-gateway/pull/1008)), each `MCPServerRegistration` can reference its own CA certificate via `caCertSecretRef`. When many upstream servers share the same root or intermediate CA, this results in:

- **Duplication** — the same CA PEM repeated N times across N registrations. Common in environments using OpenShift service-serving CA, a shared cert-manager issuer, or a corporate root CA.
- **Config secret bloat** — each CA cert (up to 64 KiB per the `maxCACertSize` limit in the controller) is embedded per-server in the config YAML passed to the broker. The config is stored in a Kubernetes Secret (`mcp-gateway-config`), which is subject to the 1 MiB Secret limit. With 15 servers sharing the same 64 KiB CA bundle, the config consumes ~960 KiB on CA certs alone, leaving no room for actual configuration.
- **Operational overhead** — CA rotation requires updating N Secrets and waiting for N re-reconciliations instead of one. Each `MCPServerRegistration` re-reconciles independently, creating a window where some servers use the old CA and some use the new one.

## Summary

Add a `caCertBundleRef` field to `MCPGatewayExtension` that references a shared CA certificate bundle Secret. The broker loads this bundle at startup as a base trust pool. Per-server `caCertSecretRef` on `MCPServerRegistration` appends additional CAs for backends with unique CAs. This mirrors how Kubernetes itself works — cluster-wide CA bundle plus per-resource overrides.

## Goals

- Eliminate CA PEM duplication in the config secret when multiple servers share the same CA
- Reduce operational burden for CA rotation to a single Secret update
- Maintain backward compatibility — existing `caCertSecretRef` on `MCPServerRegistration` continues to work identically

## Non-Goals

- Replace `caCertSecretRef` on `MCPServerRegistration` (still needed for server-specific CAs)
- Modify how Envoy/Gateway handles TLS (this only affects broker-to-upstream connections)
- Implement mutual TLS (mTLS) between broker and upstream servers
- Support non-PEM certificate formats

## Job Stories

### When I have many servers behind the same private CA

When a platform engineer has 10+ upstream MCP servers behind the same OpenShift service-serving CA, they want to configure the CA once at the gateway level so that they don't need to create and maintain separate CA Secrets for each `MCPServerRegistration`.

### When I need to rotate a shared CA certificate

When a platform engineer needs to rotate the CA certificate used by all upstream servers (e.g. cert-manager issuer renewal), they want to update a single Secret and have all servers pick up the new CA within one reconciliation cycle, instead of updating N Secrets and waiting for N independent reconciliations.

### When I have servers with unique CAs alongside a shared CA

When a platform engineer has most servers behind a shared corporate CA but one or two servers using their own private CA, they want to set the shared CA at the gateway level and the unique CAs per-server, so that both types work without duplication.

### When I want to avoid hitting the Kubernetes Secret size limit

When a platform engineer registers many servers that each embed CA PEM data in the config secret, they want the shared CA to be loaded once by the broker (not embedded N times in the config), so that the config secret stays well under the 1 MiB Kubernetes limit.

## Design

### API Changes

#### MCPGatewayExtension

New optional field `caCertBundleRef` on `MCPGatewayExtensionSpec`:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    name: mcp-gateway
    sectionName: mcp
  caCertBundleRef:
    name: shared-ca-bundle
    key: ca.crt    # optional, defaults to "ca.crt"
```

```go
// MCPGatewayExtensionSpec gains:
type MCPGatewayExtensionSpec struct {
    // ... existing fields ...

    // caCertBundleRef references a Secret containing a PEM-encoded CA certificate
    // bundle used as the base trust pool for all upstream MCP server connections.
    // Per-server caCertSecretRef on MCPServerRegistration appends to this pool.
    // The Secret must have the label mcp.kuadrant.io/secret=true.
    // +optional
    CACertBundleRef *CACertBundleReference `json:"caCertBundleRef,omitempty"`
}

// CACertBundleReference identifies a Secret containing a PEM-encoded CA bundle.
type CACertBundleReference struct {
    // name is the name of the Secret resource.
    // +required
    // +kubebuilder:validation:MinLength=1
    Name string `json:"name,omitempty"`

    // key is the key within the Secret that contains the CA bundle PEM data.
    // If not specified, defaults to "ca.crt".
    // +optional
    // +default="ca.crt"
    Key string `json:"key,omitempty"`
}
```

The default key is `ca.crt` — the same default used by per-server `caCertSecretRef`. This means a user can promote an existing per-server CA Secret to gateway-level without creating a new Secret or changing keys.

#### Config Type Changes

`BrokerConfig` in `internal/config/types.go` gains a field for the gateway-level CA bundle:

```go
type BrokerConfig struct {
    Servers          []MCPServer           `json:"servers"          yaml:"servers"`
    VirtualServers   []VirtualServerConfig `json:"virtualServers,omitempty" yaml:"virtualServers,omitempty"`
    GatewayCACertPEM string                `json:"gatewayCACertPEM,omitempty" yaml:"gatewayCACertPEM,omitempty"`
}
```

The gateway CA bundle PEM is written once into the existing `mcp-gateway-config` Secret alongside the server list. The broker reads it from the same config source — no separate Secrets or volume mounts. Servers that share the gateway CA no longer need individual `CACert` entries, reducing the per-server duplication from N copies to 1.

### Architecture

#### How the CA Bundle Reaches the Broker

The gateway CA bundle uses the same config path as everything else — the `mcp-gateway-config` Secret:

```
MCPGatewayExtension              Controller                     Broker
┌──────────────────┐     ┌──────────────────────┐     ┌────────────────────┐
│ caCertBundleRef: │     │ Reads CA Secret,      │     │                    │
│   name: shared-  │────▶│ validates PEM,        │────▶│ Reads config.yaml  │
│         ca-bundle│     │ writes PEM into       │     │ extracts           │
│   key: ca.crt    │     │ config.yaml under     │     │ gatewayCACertPEM,  │
└──────────────────┘     │ gatewayCACertPEM key  │     │ builds base cert   │
                         └──────────────────────┘     │ pool               │
                                                      └────────────────────┘
```

The MCPGatewayExtension controller:
1. Validates the referenced CA Secret exists, has the required label, contains valid PEM, and is within size limits
2. Writes the CA PEM into the `mcp-gateway-config` Secret's `config.yaml` under the `gatewayCACertPEM` key
3. Existing config watcher detects the Secret update and calls `mcpConfig.Notify()` to trigger observers

The broker:
1. Receives config via `OnConfigChange()` (the existing observer pattern)
2. Reads `GatewayCACertPEM` from the config, builds a base `*x509.CertPool` from system roots + gateway CA bundle
3. For each upstream server, clones this base pool and appends any per-server CA from the config (if `CACert` is set on the `MCPServer`)

Using the existing config Secret ensures **atomic updates** — the server list and CA bundle are always in sync. There is no timing window where the broker has a new server config but not yet the CA needed to connect.

#### Trust Pool Construction

```
System Root CAs (Go default)
         ├── + Gateway CA Bundle (from gatewayCACertPEM in config)
         │        = Base Trust Pool (shared by all servers)
         │
         ├── Server A: uses base pool only (no caCertSecretRef)
         ├── Server B: uses base pool only (no caCertSecretRef)
         └── Server C: base pool + per-server CA (caCertSecretRef set)
                       = Server-specific pool
```

When `caCertBundleRef` is not set, behavior is identical to today — each server uses system roots + its own `caCertSecretRef` if set.

### Interaction with Per-Server caCertSecretRef

| Gateway `caCertBundleRef` | Server `caCertSecretRef` | Effective Trust Pool |
|---------------------------|--------------------------|----------------------|
| Not set                   | Not set                  | System roots only |
| Not set                   | Set                      | System roots + server CA (current behavior) |
| Set                       | Not set                  | System roots + gateway CA bundle |
| Set                       | Set                      | System roots + gateway CA bundle + server CA |

Per-server `caCertSecretRef` always **appends** — it never replaces the gateway bundle. This is additive-only, following the Kubernetes pattern where more-specific configuration adds to less-specific configuration rather than overriding it.

### Validation

The MCPGatewayExtension controller validates `caCertBundleRef` during reconciliation:

| Check | Error |
|-------|-------|
| Secret exists | `CA bundle secret <name> not found` |
| Secret has `mcp.kuadrant.io/secret=true` label | `CA bundle secret <name> missing required label` |
| Key exists in Secret | `CA bundle secret <name> missing key <key>` |
| PEM data is valid | `CA bundle in secret <name> is invalid: <reason>` |
| Size ≤ 256 KiB | `CA bundle data exceeds maximum size` |

The size limit is 256 KiB (vs 64 KiB for per-server CAs) because a shared bundle typically contains multiple CA certificates (root + intermediates, or multiple issuer CAs).

### Config Secret Strategy

The CA bundle PEM is written into the existing `mcp-gateway-config` Secret as the `gatewayCACertPEM` field in `config.yaml`. This:

1. **Atomic updates** — config and CA bundle are in the same Secret, so the broker always sees a consistent snapshot. No timing window where a server appears before its CA is available.
2. **Single config source** — no separate Secrets or volume mounts to synchronize. The broker reads everything from one place.
3. **Reduces per-server duplication** — the gateway CA is written once. Servers sharing it no longer embed individual `CACert` values, so the net effect is a reduction from N copies to 1.

The gateway bundle does add to the config Secret size, but since it replaces N per-server copies with 1, the net effect is always a reduction when multiple servers share the same CA. If the config Secret approaches the 1 MiB Kubernetes limit (e.g. many servers each with *different* CAs plus a large gateway bundle), operators can deploy multiple `MCPGatewayExtension` instances to partition servers across gateways.

### CA Bundle Rotation

When the CA bundle Secret is updated:

1. The MCPGatewayExtension controller detects the Secret update (via watch on labeled Secrets)
2. The controller re-validates the PEM and writes the updated `gatewayCACertPEM` into the config Secret
3. The config watcher detects the config Secret change and calls `mcpConfig.Notify()`
4. The broker's `OnConfigChange()` observer rebuilds the base trust pool and re-registers servers

End-to-end propagation: ~60-120 seconds (dominated by kubelet volume sync of the config Secret to the broker pod).

## Security Considerations

- **Same label requirement** — the CA bundle Secret must have `mcp.kuadrant.io/secret=true`, consistent with per-server CA Secrets and credential Secrets
- **No new trust** — the gateway CA bundle extends the system root pool, it does not replace it. Servers that work today with publicly-trusted CAs continue to work without changes
- **Additive-only** — per-server CAs always append to the gateway bundle, never override. An operator cannot accidentally remove trust for a server by setting the gateway bundle
- **Size limit** — 256 KiB prevents accidental inclusion of large files. This is generous enough for typical CA chains (~20-30 CAs at ~2 KiB each)
- **Config Secret size** — the gateway CA bundle contributes to the 1 MiB Kubernetes Secret limit, but since it replaces N per-server copies with 1, the net size is always smaller when servers share a CA. For extreme cases, operators can partition servers across multiple gateways

## Future Considerations

### Per-Server CA Deduplication

When a server's `caCertSecretRef` references the same CA that is already in the gateway `caCertBundleRef`, the CA PEM is stored twice in the config — once under `gatewayCACertPEM` and once inline in the server's `caCert` field. This is functionally correct (additive trust pools) but wastes space. A future optimization could skip embedding per-server CAs that match the gateway bundle. However, this introduces complexity: Secrets may be in different namespaces, and if one Secret is deleted, the CA disappears from config even though another Secret references the same CA. Given the low likelihood of users having many duplicate CAs without coordinating, this optimization is deferred.

## Execution

See:
- [tasks/tasks.md](tasks/tasks.md) for the implementation plan
- [tasks/e2e_test_cases.md](tasks/e2e_test_cases.md) for E2E test cases
- [tasks/documentation.md](tasks/documentation.md) for documentation outline
