# v1alpha1 API No Longer Served

The deprecated `mcp.kuadrant.io/v1alpha1` API version is no longer served. This
is the removal step promised in the [v1 migration](1109-api-v1-migration.md):
the deprecation-warning grace period has ended. `v1` remains the storage version
and is unaffected.

## What's Changed

- **`v1alpha1` unserved**: The API server no longer advertises or accepts
  `v1alpha1` for `MCPServerRegistration`, `MCPGatewayExtension`, and
  `MCPVirtualServer`. Requests to the `v1alpha1` endpoint now return `404`.
- **`v1alpha1` not removed**: The version and its schema remain in each CRD
  (marked `served: false`), so existing resources are never orphaned.

## Impact

Any manifest, GitOps repository, controller, or script that still targets
`apiVersion: mcp.kuadrant.io/v1alpha1` will fail:

```
error: unable to recognize "server.yaml": no matches for kind
"MCPServerRegistration" in version "mcp.kuadrant.io/v1alpha1"
```

Previously these requests succeeded with a deprecation warning; they now fail.

## Migration

### 1. Update Manifests

Change the `apiVersion` in your YAML manifests from `mcp.kuadrant.io/v1alpha1`
to `mcp.kuadrant.io/v1`. The schemas are identical, so no other changes are
required. See the [v1 migration guide](1109-api-v1-migration.md) for examples.

### 2. Existing Resources

No manual migration is needed. Resources already in the cluster remain fully
accessible via `v1`, including any still stored as `v1alpha1` in etcd (the
identical schema is converted transparently on read). You do **not** need to
export and re-apply resources before upgrading.

```bash
kubectl get mcpsr    # returns your resources as v1
kubectl get mcpge
kubectl get mcpvs
```
