# Installing MCP Gateway via OLM

This guide covers installing MCP Gateway on OpenShift via kuadrant-operator.
MCP Gateway is deployed as a managed component of kuadrant-operator — there is no
separate MCP Gateway OLM subscription.

To install MCP Gateway without kuadrant-operator, use [Helm](./how-to-install-and-configure.md).

## Prerequisites

- OpenShift 4.18+ with OLM
- Gateway API CRDs present (pre-installed on OCP 4.18+)
- Istio installed

## Step 1: Install kuadrant-operator

Create a subscription for kuadrant-operator. The bundle includes the MCP Gateway
CRDs and controller:

```bash
oc apply -f - <<EOF
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

## Step 2: Enable MCP Gateway via the Kuadrant CR

Create a Kuadrant CR with MCP Gateway enabled:

```bash
oc apply -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
spec:
  components:
    mcpGateway:
      enabled: true
EOF
```

Verify the controller is running:

```bash
oc get deployment mcp-gateway-controller -n kuadrant-system
# Expected: READY 1/1
```

## Step 3: Create an MCPGatewayExtension

Create an `MCPGatewayExtension` in the namespace where you want to deploy the broker-router:

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

To disable MCP Gateway, set `enabled: false` on the Kuadrant CR:

```bash
oc patch kuadrant kuadrant -n kuadrant-system --type=merge \
  -p '{"spec":{"components":{"mcpGateway":{"enabled":false}}}}'
```

Delete your MCPGatewayExtension CRs first to trigger cascaded cleanup of broker-router
resources before disabling the component.
