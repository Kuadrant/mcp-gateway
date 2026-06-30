# Minimal MCP Gateway installation

This directory provides a minimal installation of MCP Gateway with just the core components.

## Prerequisites

- Kubernetes cluster (1.28+)
- **gateway API CRDs installed** (required!)
  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
  ```
- gateway controller (istio, envoy gateway, etc.)
- kubectl configured

**Note:** the controller will crash-loop until gateway API CRDs are present

## What gets installed

- MCP Gateway CRDs (`MCPServer`)
- MCP broker/router deployment
- MCP controller deployment
- RBAC (service accounts, roles, bindings)
- services (mcp-broker-router, mcp-config)
- basic HTTPRoute for the broker

## What you need to provide

- gateway resource (your own gateway instance)
- authentication/authorization (optional - kuadrant, keycloak, etc.)
- TLS certificates (optional)
- MCP server deployments

## Installation

### From GitHub (recommended)

```bash
kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=main'
```

or a specific version tag:

```bash
kubectl apply -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=v0.1.0'
```

**Note:** quotes are required in zsh to prevent globbing on the `?` character

### Local Development

```bash
git clone https://github.com/Kuadrant/mcp-gateway
cd mcp-gateway
kubectl apply -k config/install
```

## Verify Installation

```bash
# check namespace created
kubectl get namespace mcp-system

# check deployments
kubectl get deployments -n mcp-system

# check CRDs
kubectl get crd mcpserverregistrations.mcp.kuadrant.io
```

## Next Steps

1. create a gateway resource that the HTTPRoute can attach to
2. deploy your MCP servers
3. create MCPServerRegistration resources to register them
4. (optional) configure authentication via AuthPolicy

## Example Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  gatewayClassName: istio  # or your gateway class
  listeners:
  - name: http
    protocol: HTTP
    port: 8080
    allowedRoutes:
      namespaces:
        from: All
```

## Example MCPServerRegistration

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-mcp-server
  namespace: mcp-test
spec:
  prefix: myserver_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-mcp-route
```

## Uninstall

```bash
kubectl delete -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=main'
```
