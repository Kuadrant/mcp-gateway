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
export MCP_GATEWAY_VERSION=0.6.0-rc2
kubectl apply -k "https://github.com/kuadrant/mcp-gateway/config/crd?ref=v${MCP_GATEWAY_VERSION}"
```

Verify CRDs are installed:

```bash
kubectl get crd | grep mcp.kuadrant.io
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
  --set gateway.publicHost=your-hostname.example.com \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=your-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system
```

This automatically installs:
- **MCP Gateway Controller** - Watches MCPGatewayExtension and MCPServerRegistration resources
- **MCPGatewayExtension** - Custom resource targeting your Gateway

When the MCPGatewayExtension becomes ready, the controller automatically creates:
- **MCP Broker/Router Deployment** - Aggregates tools from upstream MCP servers
- **MCP Broker/Router Service** - Named `mcp-gateway` in the MCPGatewayExtension namespace
- **HTTPRoute** - Named `mcp-gateway-route`, routes traffic from the Gateway listener to the broker service on `/mcp`. The hostname is derived from the listener (wildcards like `*.example.com` become `mcp.example.com`). This can be disabled by setting `spec.httpRouteManagement: Disabled` on the MCPGatewayExtension if you need a custom HTTPRoute (e.g. with CORS headers or additional path rules). Note: disabling does not delete a previously created `mcp-gateway-route`; you must remove it manually
- **EnvoyFilter** - Configures Istio to route requests through the external processor (created in the Gateway's namespace)
- **ServiceAccount** - For the broker/router pods
- **Configuration Secret** - `mcp-gateway-config` containing server configuration

### What the Controller Configures

The controller reads the targeted Gateway listener (identified by `sectionName`) and uses it to configure the broker/router deployment. The following flags are set automatically based on the listener:

| Flag | Value | Source |
|------|-------|--------|
| `--mcp-broker-public-address` | `0.0.0.0:8080` | Fixed |
| `--mcp-gateway-private-host` | `<gateway>-istio.<namespace>.svc.cluster.local:<listener-port>` | Listener port + Gateway name/namespace |
| `--mcp-gateway-public-host` | Listener hostname (wildcards like `*.example.com` become `mcp.example.com`) | Listener hostname |
| `--mcp-router-key` | Auto-generated hash | MCPGatewayExtension UID |
| `--mcp-gateway-config` | `/config/config.yaml` | Fixed |

The `--mcp-gateway-private-host` flag enables hair-pinning: when a `tools/call` request arrives, the router sends an `initialize` request back through the gateway to establish a backend session. The port in this address matches the listener port from the Gateway spec.

The `--mcp-gateway-public-host` flag tells the router which `Host` header to expect on incoming requests, so it avoids rewriting it during routing.

The **EnvoyFilter** is configured to intercept traffic on the listener's port and route it through the ext_proc (external processor) running on port 50051.

The **configuration secret** only contains MCP server entries for MCPServerRegistrations whose HTTPRoutes attach to the same listener. This ensures team isolation when multiple teams share a single Gateway with different listeners.

For full details on all MCPGatewayExtension spec fields, see the [MCPGatewayExtension API Reference](../reference/mcpgatewayextension.md).

## Post-Installation Configuration

After installation, the controller automatically creates the HTTPRoute for gateway access. You can connect your MCP servers:

1. **[Register MCP Servers](./register-mcp-servers.md)** - Connect internal MCP servers
2. **[Connect to External MCP Servers](./external-mcp-server.md)** - Connect to external APIs

If you need to customize the HTTPRoute (e.g. add CORS headers), see [Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md).

## API Reference

- [MCPGatewayExtension](../reference/mcpgatewayextension.md)
- [MCPServerRegistration](../reference/mcpserverregistration.md)
- [MCPVirtualServer](../reference/mcpvirtualserver.md)

## Optional Configuration

- **[Authentication](./authentication.md)** - Configure OAuth-based authentication
- **[Authorization](./authorization.md)** - Set up fine-grained access control
- **[User Based Tool Filtering](./user-based-tool-filter.md)** - Define what tools a client is allowed to see.
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Create focused tool collections
- **[Isolated Gateway Deployment](./isolated-gateway-deployment.md)** - Multi-instance deployments for team isolation
