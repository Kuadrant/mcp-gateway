# Breaking Change: broker-router NetworkPolicy is now controller-managed

## What changed

The `MCPGatewayExtension` controller now creates and owns a `networking.k8s.io/v1`
NetworkPolicy named `mcp-gateway` in the extension's namespace, targeting the broker-router
pods. Ingress is deny-by-default: ports `8080` (broker HTTP/MCP) and `50051` (router gRPC
ext_proc) are only allowed from the target Gateway's namespace, selected via the auto-set
`kubernetes.io/metadata.name` namespace label. Port `9090` (metrics) stays open to any source.
Egress allows all traffic. The policy is owned by the `MCPGatewayExtension` and is garbage
collected when the extension is deleted, and it updates automatically if the targeted Gateway
changes.

The static Helm template `networkpolicy-broker-router.yaml`, which allowed broker-router
ingress from anywhere and required opting in via `networkPolicy.enabled`, has been removed.
The controller now owns this policy unconditionally.

## Who is affected

Clusters whose CNI enforces NetworkPolicy. Traffic from the target Gateway's namespace to the
broker-router's ingress ports keeps working automatically, since that namespace is auto-allowed.
Anything else that was previously reaching the broker-router directly (bypassing the Gateway) is
now blocked, as is the previous opt-in static policy from `networkpolicy-broker-router.yaml`.

## Migration Steps

No action needed for standard setups where clients reach the broker-router only through the
Gateway. If you had `networkPolicy.enabled: true` set in the Helm chart expecting the
broker-router policy, that flag now only controls the separate controller-pod NetworkPolicy.

If something other than the target Gateway's namespace needs direct access to the
broker-router (for example, a debugging pod in another namespace hitting one of the ingress
ports directly), that traffic is now blocked by default. There is no override via the
`MCPGatewayExtension` API, and the controller reconciles the `mcp-gateway` NetworkPolicy, so
direct hand-edits to it are reverted.

NetworkPolicies are additive: to allow the extra ingress source, create a separate
NetworkPolicy in the extension's namespace selecting the broker-router pods (labels
`app.kubernetes.io/name=mcp-gateway` and `app.kubernetes.io/managed-by=mcp-gateway-controller`).
Since Kubernetes unions the allow rules of all NetworkPolicies selecting the same pods, the
extra rules take effect alongside the controller-managed policy.
