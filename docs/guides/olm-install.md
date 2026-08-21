# Installing MCP Gateway via OLM (Kuadrant Operator)

This guide covers installing MCP Gateway on a cluster that uses
[Operator Lifecycle Manager (OLM)](https://olm.operatorframework.io/).

MCP Gateway does not ship a standalone OLM operator. On OLM-based clusters, MCP Gateway is
installed and managed by the **Kuadrant Operator**, which embeds the MCP Gateway controller and
deploys it on startup. This is the consolidated (umbrella) model described in
[RFC 0019](https://github.com/Kuadrant/architecture/pull/189).

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
- A catalog source providing a Kuadrant Operator version that bundles MCP Gateway.

> **Note:** Throughout this guide, `mcp-system` is the namespace where the operator and its
> `OperatorGroup` live. Substitute your own namespace if different.

## Step 1: Install the Kuadrant Operator

Create an `OperatorGroup` and a `Subscription` for the Kuadrant Operator.

```bash
kubectl apply -f - <<EOF
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
# The MCP CRDs are installed and owned by the Kuadrant Operator CSV
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
reconciles it and creates the broker-router `Deployment`, `Service`, `HTTPRoute`, and the
`EnvoyFilter` on the Gateway.

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway-extension
  namespace: mcp-system
spec:
  targetRef:
    name: <your-gateway>
    namespace: <your-gateway-namespace>
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

## Uninstall

Removing MCP Gateway means removing your `MCPGatewayExtension` resources (which the controller
uses to clean up the broker-router data plane), then removing the operator if you no longer need
it.

```bash
# Remove the data plane (controller must still be running to clear finalizers)
kubectl delete mcpgatewayextension --all -n mcp-system

# Remove the operator
kubectl delete subscription kuadrant-operator -n mcp-system
kubectl delete csv -n mcp-system -l operators.coreos.com/kuadrant-operator.mcp-system
```

> **Note:** OLM does not delete CRDs when a CSV is removed. Delete the MCP CRDs manually only if
> you are sure no other operator or workload depends on them.

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md)
- [Authentication](./authentication.md)
- [Authorization](./authorization.md)
