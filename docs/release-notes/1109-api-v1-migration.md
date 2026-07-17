# MCP Gateway API v1 Migration (CONNLINK-1109)

The MCP Gateway APIs have been promoted to `v1`. This migration aligns the Gateway's CRDs (`MCPServerRegistration`, `MCPGatewayExtension`, and `MCPVirtualServer`) with Kubernetes API stability standards. `v1` is now the `storageVersion` for all Custom Resource Definitions.

## What's Changed

- **GroupVersion Promoted**: All Custom Resources have been migrated from `api/v1alpha1` to `api/v1`.
- **Identical Schema**: There are no structural changes between `v1alpha1` and `v1`. The schemas are identical.
- **Shortnames**: Consistent shortnames have been introduced for API resources (`mcpvs`, `mcpge`, `mcpsr`).
- **Printer Columns**: `MCPVirtualServer` now correctly reports its `Ready` status in `kubectl get mcpvs` output.

## Migration Guide

### 1. Update Manifests

Update the `apiVersion` in your YAML manifests from `mcp.kuadrant.io/v1alpha1` to `mcp.kuadrant.io/v1`. Since the schema hasn't changed, no other structural changes are required.

**Before:**

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-server
spec:
  prefix: my_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-route
```

**After:**

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-server
spec:
  prefix: my_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-route
```

You can now use shortnames in `kubectl` commands:

```bash
kubectl get mcpsr    # MCPServerRegistration
kubectl get mcpge    # MCPGatewayExtension
kubectl get mcpvs    # MCPVirtualServer
```

### 2. Existing Resources

Existing `v1alpha1` resources stored in the cluster are transparently served via both API versions. They will be re-stored as `v1` on the next write (e.g., `kubectl edit` or controller reconciliation). No manual migration of in-cluster resources is required.

### 3. Deprecation Timeline

The `v1alpha1` APIs remain served for backward compatibility but are deprecated. Clients using `v1alpha1` will receive a deprecation warning from the API server. Support for `v1alpha1` will be removed in a future release. Migrate your manifests and GitOps repositories to `v1` before then.
