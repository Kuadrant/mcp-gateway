# Installing MCP Gateway via OLM (Kuadrant Operator)

This guide covers installing MCP Gateway on a cluster that uses
[Operator Lifecycle Manager (OLM)](https://olm.operatorframework.io/).

On OLM-based clusters, install MCP Gateway through the **Kuadrant Operator**, which embeds the
MCP Gateway controller and deploys it on startup. This is the consolidated (umbrella) model
described in
[RFC 0019](https://github.com/Kuadrant/architecture/blob/main/rfcs/0019-olmv1-operator-consolidation.md).

> **Note:** If you are not using OLM, install MCP Gateway standalone with Helm — see
> [Installing and Configuring MCP Gateway](./how-to-install-and-configure.md).
>
> If you previously installed MCP Gateway via its own OLM subscription and want to move to the
> Kuadrant Operator, follow
> [Upgrading from Standalone MCP Gateway to the Kuadrant Operator](./olm-upgrade.md) instead.

## Prerequisites

- A cluster with OLM. OpenShift includes OLM by default; on other Kubernetes distributions,
  install OLM first.
- Gateway API CRDs and an Istio-based Gateway API provider installed.
- A `Gateway` named `<your-gateway>` in `mcp-system` with a listener named `<listener-name>`.
- A catalog source providing a Kuadrant Operator version that bundles MCP Gateway.

> **Note:** Throughout this guide, `mcp-system` is the namespace where the operator and its
> `OperatorGroup` live. Substitute your own namespace if different.

## Step 1: Install the Kuadrant Operator

Create the `mcp-system` namespace, an `OperatorGroup`, and a `Subscription` for the Kuadrant Operator.

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: mcp-system
---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: kuadrant
  namespace: mcp-system
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: mcp-system
spec:
  channel: <channel>
  name: kuadrant-operator
  source: <catalog-source>
  sourceNamespace: <catalog-namespace>
  installPlanApproval: Automatic
EOF
```

> **Note:** `<catalog-namespace>` is the namespace of your catalog source: `openshift-marketplace`
> on OpenShift, `olm` on most other OLM installs.

Wait for the operator's ClusterServiceVersion (CSV) to succeed. The CSV may take a moment to
appear while OLM processes the subscription:

```bash
kubectl get csv -n mcp-system -w
# kuadrant-operator.<version>   ...   Succeeded
```

If `kubectl get csv` returns nothing at first, wait a few seconds and retry — the CSV has not been
created yet.

## Step 2: Verify the MCP Gateway controller is running

The Kuadrant Operator deploys the MCP Gateway controller on startup, without requiring a
`Kuadrant` custom resource. (A `Kuadrant` CR is only needed later to apply `AuthPolicy` or
`RateLimitPolicy`.)

```bash
# The MCP CRDs are installed by the Kuadrant Operator at runtime
kubectl get crd | grep mcp.kuadrant.io
# mcpgatewayextensions.mcp.kuadrant.io
# mcpserverregistrations.mcp.kuadrant.io
# mcpvirtualservers.mcp.kuadrant.io

# The MCP Gateway controller is running
kubectl get deployment mcp-gateway-controller -n mcp-system
# READY 1/1
```

## Step 3: Deploy an MCP Gateway instance

Installing the operator deploys the controller only. To deploy the MCP Gateway data plane
(broker-router), create an `MCPGatewayExtension` that targets your Gateway. The controller
reconciles it and creates the broker-router `Deployment`, `Service`, `HTTPRoute`, an
`EnvoyFilter` on the Gateway, and a `NetworkPolicy` named `mcp-gateway`.

The controller automatically creates and manages this `NetworkPolicy` in the
`MCPGatewayExtension` namespace, which can differ from the target Gateway namespace. The policy
allows ingress on ports `8080` (broker HTTP/MCP) and `50051` (router gRPC ext_proc) only from the
target Gateway namespace.
It allows metrics on port `9090` from any source, denies other broker-router ingress by default,
and allows all egress. The controller selects the target Gateway namespace by matching the
`kubernetes.io/metadata.name` namespace label to `spec.targetRef.namespace`; when that field is
omitted, it uses the `MCPGatewayExtension` namespace.

For details about `MCPGatewayExtension` fields, see the
[MCPGatewayExtension API Reference](../reference/mcpgatewayextension.md).

This example keeps the `MCPGatewayExtension` and target `Gateway` in `mcp-system`, so it does not
require a `ReferenceGrant`. If the target Gateway is in another namespace, set
`targetRef.namespace` and create a `ReferenceGrant` in the target namespace.

Verify that the target Gateway and named listener exist:

```bash
GATEWAY_NAME="<your-gateway>"
GATEWAY_NAMESPACE="mcp-system"
LISTENER_NAME="<listener-name>"

kubectl get gateway "$GATEWAY_NAME" -n "$GATEWAY_NAMESPACE"
kubectl get gateway "$GATEWAY_NAME" -n "$GATEWAY_NAMESPACE" \
  -o jsonpath='{range .spec.listeners[*]}{.name}{"\n"}{end}' \
  | grep -Fx "$LISTENER_NAME"
# <listener-name>
```

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway-extension
  namespace: mcp-system
spec:
  targetRef:
    name: <your-gateway>
    namespace: mcp-system
    sectionName: <listener-name>
EOF
```

Verify the extension becomes Ready and the broker-router is running:

```bash
kubectl get mcpgatewayextension -n mcp-system
# READY True

kubectl get deployment mcp-gateway -n mcp-system
# READY 1/1
```

> **Note:** For more deployment options — multiple isolated instances, cross-namespace Gateway
> references with `ReferenceGrant`, session storage, and listener configuration — see
> [Isolated Gateway Deployment](./isolated-gateway-deployment.md) and
> [Configure MCP Gateway Listener and Router](./configure-mcp-gateway-listener-and-router.md).

## Step 4: Verify end to end

After registering at least one MCP server (see [Register MCP Servers](./register-mcp-servers.md)),
check that the gateway serves tool calls end to end.

Set `MCP_URL` to your Gateway listener's scheme, public hostname and port:

```bash
MCP_URL="http://your-hostname.example.com:8080/mcp"
```

1. Initialize a session with protocol version `2025-11-25` and save the session ID:

   ```bash
   curl -s -D /tmp/mcp_headers -X POST "$MCP_URL" \
     -H "Content-Type: application/json" \
     -H "Accept: application/json, text/event-stream" \
     -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

   SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')
   ```

2. Check that your MCPServerRegistration is ready:

   ```bash
   kubectl get mcpsr -A
   ```

   The `READY` column should show `True`.

3. List tools:

   ```bash
   curl -s -X POST "$MCP_URL" \
     -H "Content-Type: application/json" \
     -H "Accept: application/json, text/event-stream" \
     -H "mcp-session-id: $SESSION_ID" \
     -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'
   ```

   The response should include your server's tools, prefixed with the `prefix` from its
   MCPServerRegistration.

4. Call one of those tools. Replace `<tool-name>` and `arguments` with a prefixed tool from the
   `tools/list` response and its input (not `discover_tools` or `select_tools`, which the gateway
   answers itself):

   ```bash
   curl -s -X POST "$MCP_URL" \
     -H "Content-Type: application/json" \
     -H "Accept: application/json, text/event-stream" \
     -H "mcp-session-id: $SESSION_ID" \
     -d '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "<tool-name>", "arguments": {}}}'
   ```

   The response should contain a `result`, not an `error`. Depending on the server, it comes back
   as plain JSON or as a `data:` line in an event stream. If the call fails, see
   [tools/list Works but tools/call Fails](./troubleshooting.md#toolslist-works-but-toolscall-fails).

> **Note:** Don't stop at `tools/list`. The broker answers `initialize` and `tools/list` itself, so
> both succeed even when the Gateway listener layout or the
> [private host](../reference/mcpgatewayextension.md#mcpgatewayextensionspec) is wrong. Only
> `tools/call` is routed through your server's HTTPRoute, and for `2025-11-25` sessions the router
> first hairpins an `initialize` request through the private host. That's why this check uses
> `2025-11-25`: with `2026-07-28` there is no hairpin, so a wrong private host would go unnoticed.

## Uninstall

Removing MCP Gateway means removing your `MCPGatewayExtension` resources (which the Kuadrant
Operator uses to clean up the broker-router data plane), then removing the Kuadrant Operator if
you no longer need it.

```bash
# Remove the data plane (controller must still be running to clear finalizers)
kubectl delete mcpgatewayextension --all -n mcp-system

# Remove the operator
kubectl delete subscription kuadrant-operator -n mcp-system
kubectl delete csv -n mcp-system -l operators.coreos.com/kuadrant-operator.mcp-system
```

> **Note:** Removing the Kuadrant `Subscription` and CSV uninstalls the entire Kuadrant control
> plane, not just MCP Gateway. If other Kuadrant features (such as `AuthPolicy` or
> `RateLimitPolicy`) are in use on the cluster, leave the Kuadrant Operator installed and only
> remove the `MCPGatewayExtension` resources.
> **Warning:** Deleting an MCP CRD irreversibly deletes every resource of that kind in every
> namespace. Before deleting CRDs, back up the resources and verify that no other operator or
> workload depends on them.

Back up the MCP resources before deleting their CRDs:

```bash
kubectl get mcpgatewayextensions,mcpserverregistrations,mcpvirtualservers -A -o yaml > mcp-resources-backup.yaml
```

Delete the MCP CRDs only when you have confirmed that they are no longer needed. This command
deletes the three MCP CRDs installed by MCP Gateway:

```bash
kubectl delete crd \
  mcpgatewayextensions.mcp.kuadrant.io \
  mcpserverregistrations.mcp.kuadrant.io \
  mcpvirtualservers.mcp.kuadrant.io \
  --wait=true \
  --ignore-not-found=true
```

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md)
- [Authentication](./authentication.md)
- [Authorization](./authorization.md)
