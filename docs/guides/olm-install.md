# Installing MCP Gateway via OLM

This guide covers installing MCP Gateway on OpenShift via kuadrant-operator.
MCP Gateway is deployed as a managed component of kuadrant-operator — there is no
separate MCP Gateway OLM subscription.

To install MCP Gateway without kuadrant-operator, use [Helm](./how-to-install-and-configure.md).

## Prerequisites

- OpenShift 4.18+ with OLM
- Gateway API CRDs present (pre-installed on OCP 4.18+)
- Istio installed (mcp-gateway requires Istio EnvoyFilter CRDs)

## Step 1: Install kuadrant-operator

Create a namespace, OperatorGroup, and Subscription for kuadrant-operator:

```bash
oc new-project kuadrant-system

oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: kuadrant-operator-group
  namespace: kuadrant-system
spec: {}
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: kuadrant-system
spec:
  channel: stable
  name: kuadrant-operator
  source: kuadrant-operator-catalog
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
```

Wait for the operator to be ready:

```bash
oc wait csv -n kuadrant-system -l operators.coreos.com/kuadrant-operator.kuadrant-system="" \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=5m
```

The kuadrant-operator deploys all component controllers (including the MCP Gateway
controller) automatically on startup into the `kuadrant-system` namespace. No
additional configuration is needed to enable MCP Gateway — it runs as a singleton
alongside authorino-operator, limitador-operator, and dns-operator.

Verify the MCP Gateway controller is running:

```bash
oc get deployment mcp-gateway-controller -n kuadrant-system
# Expected: READY 1/1
```

## Step 2: Create a Kuadrant CR

Create a Kuadrant CR to enable data plane resources (Authorino, Limitador, policies):

```bash
oc apply -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
spec: {}
EOF
```

## Step 3: Create an MCPGatewayExtension

Create an `MCPGatewayExtension` in the namespace where you want to deploy the broker-router.
The MCP Gateway controller watches for these across all namespaces and creates the
broker-router infrastructure per extension:

```bash
GATEWAY_HOST=$(oc get gateway <your-gateway> -n <gateway-namespace> \
  -o jsonpath='{.status.addresses[0].value}')

oc apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: <your-namespace>
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: <your-gateway>
    namespace: <gateway-namespace>
    sectionName: mcp
  publicHost: $GATEWAY_HOST
EOF
```

Wait for it to be ready:

```bash
oc wait mcpgatewayextension mcp-gateway -n <your-namespace> \
  --for=jsonpath='{.status.conditions[0].reason}'=ValidMCPGatewayExtension \
  --timeout=60s
```

The controller automatically creates the broker-router Deployment, Service, HTTPRoute,
EnvoyFilter, and configuration Secret.

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md)
- [Authentication](./authentication.md)
- [Authorization](./authorization.md)

## Uninstall

Delete your MCPGatewayExtension CRs first to trigger cascaded cleanup of broker-router
resources, then uninstall kuadrant-operator via OLM.
