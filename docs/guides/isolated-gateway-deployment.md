# Isolated MCP Gateway Deployment

This guide demonstrates how to deploy MCP Gateway instances for your environment. Each deployment is given its own configuration for MCP Servers to manage based on the MCPGatewayExtension resource. This allows for multiple MCP Gateway instances to be deployed within a single cluster and to isolate traffic.

This guide assumes some knowledge about exposing MCP servers via an HTTPRoute. You can find more info in the following guide [configure-mcp-gateway-listener](./configure-mcp-gateway-listener-and-router.md).

This guide assumes some knowledge about configuring an MCPServerRegistration. You can find more information in the following guide [register-mcp-servers](./register-mcp-servers.md).

## Overview

The MCP Gateway requires an `MCPGatewayExtension` resource to operate. This resource:

- Defines which Gateway the MCP Gateway instance is responsible for
- Determines where configuration secrets are created (same namespace as the MCPGatewayExtension)
- Enables isolation by allowing multiple MCP Gateway instances (in different namespaces) to target different Gateways


## Prerequisites

- Cluster with Gateway API support
- Istio installed as the Gateway API provider
- Helm 3.x


> Note: The guide expects you have cloned the repo locally. This allows using the latest code. If you want to make use of a specific release, use the following in the helm commands and then there is no need for the repo locally:

```bash
helm upgrade -i mcp-controller oci://ghcr.io/kuadrant/charts/mcp-gateway \
    --version ${MCP_GATEWAY_VERSION} \

```

## Step 1: Install MCP Gateway CRDs

```bash
export MCP_GATEWAY_VERSION=main #change this to the version you want
kubectl apply -k "https://github.com/kuadrant/mcp-gateway/config/crd?ref=${MCP_GATEWAY_VERSION}"
```

Verify the CRDs are installed:

```bash
kubectl get crd | grep mcp.kagenti.com
```

Note: CRDs are also installed automatically when deploying the controller via Helm in Step 3.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                        MCP System Namespace                          │
├──────────────────────────────────────────────────────────────────────┤
│                    MCP Controller (cluster-wide)                     │
└──────────────────────────────────────────────────────────────────────┘
                                    │
              ┌─────────────────────┴─────────────────────┐
              ▼                                           ▼
┌───────────────────────────────┐     ┌───────────────────────────────┐
│          Team A NS            │     │          Team B NS            │
├───────────────────────────────┤     ├───────────────────────────────┤
│ MCPGatewayExtension           │     │ MCPGatewayExtension           │
│   → Gateway A                 │     │   → Gateway B                 │
│ MCP Broker/Router             │     │ MCP Broker/Router             │
│ Config Secret                 │     │ Config Secret                 │
│ MCPServerRegistrations        │     │ MCPServerRegistrations        │
└───────────────────────────────┘     └───────────────────────────────┘
```

Each MCPGatewayExtension must target a different Gateway. The controller creates configuration secrets in the same namespace(s) as valid MCPGatewayExtension(s), which are mounted into the broker/router deployments.

For cross-namespace Gateway references with a MCPGatewayExtension, a ReferenceGrant must exist in the target Gateway's namespace.

## Step 2: Set Environment Variables

### OpenShift

```bash
export TEAM_A_HOST="team-a.apps.$(oc get dns cluster -o jsonpath='{.spec.baseDomain}')"
export TEAM_B_HOST="team-b.apps.$(oc get dns cluster -o jsonpath='{.spec.baseDomain}')"
```

### Kind/Kubernetes

What is shown below is just an example and what is used locally via Kind.

```bash
export TEAM_A_HOST="team-a.127-0-0-1.sslip.io"
export TEAM_B_HOST="team-b.127-0-0-1.sslip.io"
```

## Step 3: Deploy the MCP Controller

The MCP Controller runs cluster-wide and reconciles MCPGatewayExtension and MCPServerRegistration resources. Deploy it once in a central namespace:

```bash
helm upgrade -i mcp-controller ./charts/mcp-gateway \
  --namespace mcp-system \
  --create-namespace \
  --set controller.enabled=true \
  --set gateway.create=false \
  --set httpRoute.create=false \
  --set mcpGatewayExtension.create=false \
  --set envoyFilter.create=false
```

Verify the controller is running:

```bash
kubectl get pods -n mcp-system
```

## Step 4: Deploy Team A Gateway Instance

Deploy an MCP Gateway instance for Team A with its own Gateway resource:

```bash
helm upgrade -i team-a-mcp-gateway ./charts/mcp-gateway \
  --namespace team-a \
  --create-namespace \
  --set controller.enabled=false \
  --set broker.create=true \
  --set gateway.create=true \
  --set gateway.name=team-a-gateway \
  --set gateway.namespace=gateway-system \
  --set gateway.publicHost="$TEAM_A_HOST" \
  --set gateway.internalHostPattern="*.team-a.mcp.local" \
  --set httpRoute.create=true \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=team-a-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system \
  --set envoyFilter.create=true \
  --set envoyFilter.name=team-a-gateway
```

The Helm chart creates:
- Gateway resource in gateway-system namespace
- HTTPRoute for the MCP endpoint
- MCPGatewayExtension targeting the Gateway
- ReferenceGrant in the Gateway namespace (for cross-namespace references)
- Broker/Router deployment
- Service for the broker
- EnvoyFilter to route traffic through the external processor
- ServiceAccount and RBAC
- Config Secret for MCP server configuration

## Step 5: Verify Team A Deployment

```bash
# Check Gateway is created
kubectl get gateway team-a-gateway -n gateway-system

# Check MCPGatewayExtension is ready
kubectl wait --for=condition=Ready mcpgatewayextension/team-a-mcp-gateway -n team-a --timeout=60s

# Check pods are running
kubectl get pods -n team-a
```

## Step 6: Deploy Team B Gateway Instance

Deploy a second isolated gateway for Team B:

```bash
helm upgrade -i team-b-mcp-gateway ./charts/mcp-gateway \
  --namespace team-b \
  --create-namespace \
  --set controller.enabled=false \
  --set broker.create=true \
  --set gateway.create=true \
  --set gateway.name=team-b-gateway \
  --set gateway.namespace=gateway-system \
  --set gateway.publicHost="$TEAM_B_HOST" \
  --set gateway.internalHostPattern="*.team-b.mcp.local" \
  --set httpRoute.create=true \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=team-b-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system \
  --set envoyFilter.create=true \
  --set envoyFilter.name=team-b-gateway
```

## Step 7: Verify Team B Deployment

```bash
# Check Gateway is created
kubectl get gateway team-b-gateway -n gateway-system

# Check MCPGatewayExtension is ready
kubectl wait --for=condition=Ready mcpgatewayextension/team-b-mcp-gateway -n team-b --timeout=60s

# Check broker pod is running
kubectl get pods -n team-b
```

## Step 8: Expose Gateways Externally

### OpenShift (Routes)

OpenShift Routes expose the Gateways externally with TLS termination.

**Team A gateway route:**
```bash
oc apply -f - <<EOF
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: team-a-gateway
  namespace: gateway-system
spec:
  host: $TEAM_A_HOST
  tls:
    insecureEdgeTerminationPolicy: Redirect
    termination: edge
  port:
    targetPort: mcp
  to:
    kind: Service
    name: team-a-gateway-istio
    weight: 100
  wildcardPolicy: None
EOF
```

**Team B gateway route:**
```bash
oc apply -f - <<EOF
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: team-b-gateway
  namespace: gateway-system
spec:
  host: $TEAM_B_HOST
  tls:
    insecureEdgeTerminationPolicy: Redirect
    termination: edge
  port:
    targetPort: mcp
  to:
    kind: Service
    name: team-b-gateway-istio
    weight: 100
  wildcardPolicy: None
EOF
```

Verify routes are created:
```bash
oc get routes -n gateway-system
```

## Exposing via NodePort

For a local kind or kubernetes setup you can configure helm to setup the NodePort service. Re-run the commands with the following flags set

```bash
# Team A gateway
--set gateway.nodePort.create=true \
--set gateway.nodePort.mcpPort=30080 \
```

```bash
# Team B gateway
--set gateway.nodePort.create=true \
--set gateway.nodePort.mcpPort=30471 \
```

## Next Steps: Register MCP Servers

Once your gateway instances are deployed, register MCP servers with each instance. See the [Register MCP Servers](./register-mcp-servers.md) guide for detailed instructions.

When registering servers, ensure the HTTPRoute's `parentRef` targets the correct Gateway:
- Team A instance: `team-a-gateway` in `gateway-system`
- Team B instance: `team-b-gateway` in `gateway-system`

## Limitations

**Broker/Router must be co-located with MCPGatewayExtension**: The MCP Gateway instance (broker and router) must be deployed in the same namespace as the MCPGatewayExtension. The controller writes the configuration secret to the MCPGatewayExtension's namespace, and the broker/router mount this secret.

**One MCPGatewayExtension per namespace**: Each namespace can only have one MCPGatewayExtension. The controller writes configuration to a well-known secret name, so multiple extensions would overwrite each other.

**One MCPGatewayExtension per Gateway**: Only one MCPGatewayExtension can target a given Gateway. If multiple extensions target the same Gateway, the controller marks newer ones as conflicted. The oldest extension (by creation timestamp) wins.

## Troubleshooting

### MCPGatewayExtension shows RefGrantRequired

The MCPGatewayExtension is targeting a Gateway in a different namespace, but no ReferenceGrant exists:

```bash
kubectl get mcpgatewayextension -n team-a -o yaml
```

Look for the condition:
```yaml
conditions:
  - type: Ready
    status: "False"
    reason: ReferenceGrantRequired
    message: "ReferenceGrant required in namespace gateway-system to allow cross-namespace reference"
```

The Helm chart should create the ReferenceGrant automatically. If not, create it manually as shown in the [Manual Resource Creation](#manual-resource-creation) section.

### MCPGatewayExtension shows InvalidMCPGatewayExtension

The target Gateway doesn't exist or there's a conflict.

Check if there is another MCPGatewayExtension that is older that is also targeting the Gateway.

### MCPServerRegistration shows NotReady

The registration can't find a valid MCPGatewayExtension for the Gateway its HTTPRoute is attached to:

MCPServerRegistration resources are only processed when a valid MCPGatewayExtension exists for the Gateway their HTTPRoute is attached to. Without a matching MCPGatewayExtension, registrations will show a NotReady status.

```bash
kubectl get mcpserverregistration -n team-a -o yaml
```

Check that:
1. The HTTPRoute exists and references the correct Gateway and its status is accepted
2. An MCPGatewayExtension exists targeting that Gateway
3. The MCPGatewayExtension is in Ready state

### No configuration in secret

The config secret exists but has no servers:

```bash
kubectl get secret mcp-gateway-config -n team-a -o jsonpath='{.data.config\.yaml}' | base64 -d
```

Check that MCPServerRegistration resources exist and are Ready:

```bash
kubectl get mcpserverregistration -n team-a
```

## Cleanup

To remove the deployments:

### OpenShift

```bash
# Delete routes
oc delete route team-a-gateway team-b-gateway -n gateway-system

# Uninstall gateway Helm releases
helm uninstall team-b-mcp-gateway -n team-b
helm uninstall team-a-mcp-gateway -n team-a

# Uninstall controller
helm uninstall mcp-controller -n mcp-system

# Delete namespaces
oc delete namespace team-b
oc delete namespace team-a
oc delete namespace mcp-system
```

### Kind/Kubernetes

```bash
# Uninstall gateway Helm releases
helm uninstall team-b-mcp-gateway -n team-b
helm uninstall team-a-mcp-gateway -n team-a

# Uninstall controller
helm uninstall mcp-controller -n mcp-system

# Delete namespaces
kubectl delete namespace team-b
kubectl delete namespace team-a
kubectl delete namespace mcp-system
```

## Manual Resource Creation

If you prefer to create the MCPGatewayExtension and ReferenceGrant manually instead of having Helm manage them, disable automatic creation and apply the resources yourself:

### ReferenceGrant (Cross-Namespace Only)

If the MCPGatewayExtension is in a different namespace than the Gateway, create a ReferenceGrant in the Gateway's namespace:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: allow-team-a
  namespace: gateway-system
spec:
  from:
    - group: mcp.kagenti.com
      kind: MCPGatewayExtension
      namespace: team-a
  to:
    - group: gateway.networking.k8s.io
      kind: Gateway
EOF
```

### MCPGatewayExtension

Create the MCPGatewayExtension to associate the team's namespace with the target Gateway:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: team-a-gateway
  namespace: team-a
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: team-a-gateway
    namespace: gateway-system
EOF
```

### Deploy without automatic resource creation

```bash
helm install team-a-mcp-gateway ./charts/mcp-gateway \
  --namespace team-a \
  --set controller.enabled=false \
  --set gateway.create=true \
  --set gateway.name=team-a-gateway \
  --set gateway.namespace=gateway-system \
  --set gateway.publicHost="$TEAM_A_HOST" \
  --set httpRoute.create=true \
  --set envoyFilter.create=true \
  --set envoyFilter.name=team-a-gateway \
  --set mcpGatewayExtension.create=false
```
