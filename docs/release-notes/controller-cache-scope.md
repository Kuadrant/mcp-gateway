# Controller cache uses less memory on busy clusters

## What changed

The MCP Gateway controller now caches only Deployments, Services, ServiceAccounts, and
NetworkPolicies labeled `app.kubernetes.io/managed-by=mcp-gateway-controller`. It also limits
its CustomResourceDefinition cache to the Istio EnvoyFilter definition. Unrelated objects of
these kinds without the matching label or name no longer add to the controller's heap.

The controller reads backend Services referenced by MCP server registrations directly from
the Kubernetes API. Those Services can keep their existing labels.

## Who is affected

All installations, especially clusters with many workloads unrelated to MCP Gateway. The
managed-by label identifies resources managed by the MCP Gateway controller. Objects from other
workloads that match the label selector also enter the cache.

## Migration Steps

No migration is needed. Existing controller-managed resources already carry the label. If the
label is removed from one of those resources, the controller restores it during reconciliation.
