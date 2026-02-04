# Installing and Configuring MCP Gateway

This guide demonstrates how to install and configure the MCP Gateway to aggregate multiple Model Context Protocol (MCP) servers behind a single endpoint.

## Prerequisites

MCP Gateway runs on Kubernetes and integrates with Gateway API and Istio. You should be familiar with:
- **Kubernetes** - Basic kubectl and YAML knowledge
- **Gateway API** - Kubernetes standard for traffic routing
- **Istio** - Gateway API provider

**Choose your setup approach:**

**Option A: Local Setup Start (5 minutes)**
- Want to try MCP Gateway immediately with minimal setup
- Automated script handles everything for you
- Perfect for evaluation and testing
- **[Quick Start Guide](./quick-start.md)**

**Option B: Existing Cluster**
- You have a Kubernetes cluster with Gateway API CRDs and Istio already installed
- Are ready to deploy MCP Gateway immediately
- If you want to deploy isolated MCP Gateway instances for different teams there is a specific guide for that **[Isolated Gateway Deployment Guide](./isolated-gateway-deployment.md)** which goes into more detail.

## Installation

### Step 1: Install CRDs

```bash
export MCP_GATEWAY_VERSION=main  # or a specific version tag
kubectl apply -k "https://github.com/kuadrant/mcp-gateway/config/crd?ref=${MCP_GATEWAY_VERSION}"
```

Verify CRDs are installed:

```bash
kubectl get crd | grep mcp.kagenti.com
```

Note: CRDs are also installed automatically when deploying via Helm.

### Step 2: Install MCP Gateway

Install from GitHub Container Registry:

```bash
helm upgrade -i mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
  --version ${MCP_GATEWAY_VERSION} \
  --namespace mcp-system \
  --create-namespace \
  --set controller.enabled=true \
  --set broker.create=true \
  --set gateway.publicHost=your-hostname.example.com \
  --set envoyFilter.create=true \
  --set envoyFilter.namespace=istio-system \
  --set envoyFilter.name=your-gateway \
  --set mcpGatewayExtension.gatewayRef.name=your-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system
```

> **Note:** The `envoyFilter.name` must match the Gateway name. The EnvoyFilter uses this as a label selector (`gateway.io/name`) to target the correct Gateway pods.

This automatically installs:
- MCP Controller (watches MCPGatewayExtension and MCPServerRegistration resources)
- MCP Broker/Router (aggregates tools from upstream MCP servers)
- MCPGatewayExtension resource targeting your Gateway
- EnvoyFilter for Istio integration (routes requests through the external processor)
- Required CRDs, RBAC, and Secrets


> **Note:** The `gateway.publicHost` value must match the hostname configured in your Gateway listener (see [Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md)).

## Post-Installation Configuration

After installation, you'll need to configure the gateway and connect your MCP servers:

1. **[Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md)** - Set up traffic routing
2. **[Register MCP Servers](./register-mcp-servers.md)** - Connect internal MCP servers
3. **[Connect to External MCP Servers](./external-mcp-server.md)** - Connect to external APIs

## Optional Configuration

- **[Authentication](./authentication.md)** - Configure OAuth-based authentication
- **[Authorization](./authorization.md)** - Set up fine-grained access control
- **[User Based Tool Filtering](./user-based-tool-filter.md)** - Define what tools a client is allowed to see.
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Create focused tool collections
- **[Isolated Gateway Deployment](./isolated-gateway-deployment.md)** - Multi-instance deployments for team isolation
