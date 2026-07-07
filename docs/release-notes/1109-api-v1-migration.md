# MCP Gateway API v1 Migration (CONNLINK-1109)

The MCP Gateway APIs have been promoted to `v1`. This migration aligns the Gateway's CRDs (`MCPServerRegistration`, `MCPGatewayExtension`, and `MCPVirtualServer`) with Kubernetes API stability standards. `v1` is now the `storageVersion` for all Custom Resource Definitions.

## What's Changed

- **GroupVersion Promoted**: All Custom Resources have been migrated from `api/v1alpha1` to `api/v1`.
- **Identical Schema**: There are no structural changes between `v1alpha1` and `v1`. The schemas are identical.
- **Experimental Annotations Promoted to Spec**:
  - `mcp.kuadrant.io/experimental-discovery` has been removed. Use the `TokenURLElicitation` and `UserSpecificList` fields in the `MCPServerRegistrationSpec` directly.
  - `mcp.kuadrant.io/experimental-extension` has been removed. Use the `URLElicitation` field in the `MCPGatewayExtensionSpec` directly.
- **Shortnames**: Consistent shortnames have been introduced for API resources (`mcpvs`, `mcpge`, `mcpsr`).
- **Printer Columns**: `MCPVirtualServer` now correctly reports its `Ready` status in `kubectl get mcpvs` output.

## Migration Guide

### 1. Update Manifests
Update the `apiVersion` in your YAML manifests from `mcp.kuadrant.io/v1alpha1` to `mcp.kuadrant.io/v1`. Since the schema hasn't changed, no other structural changes are required.

### 2. Remove Deprecated Annotations
If you were previously using experimental annotations for URL Elicitation or User-Specific lists, you must migrate to the native Spec fields:

**Before:**
```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-server
  annotations:
    mcp.kuadrant.io/experimental-discovery: '{"tokenURLElicitation":{"url":"https://auth.example.com/login"}}'
spec: ...
```

**After:**
```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-server
spec:
  tokenURLElicitation:
    url: "https://auth.example.com/login"
```

### 3. Controller Upgrades
The `v1alpha1` versions are deprecated but will remain served for 2 minor releases to allow for smooth transitions. The Kubernetes API server will seamlessly handle conversions between the versions since they are structurally identical. Ensure you upgrade to `v1` manifests before the `v1alpha1` APIs are completely removed in a future release.
