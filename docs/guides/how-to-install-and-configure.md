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
export MCP_GATEWAY_VERSION=0.7.1
kubectl apply -k "https://github.com/kuadrant/mcp-gateway/config/crd?ref=v${MCP_GATEWAY_VERSION}"
```

Verify CRDs are installed:

```bash
kubectl get crd | grep mcp.kuadrant.io
```

Note: CRDs are also installed automatically when deploying via Helm.

### Step 2: Install MCP Gateway

Find your Gateway name and namespace, then install from GitHub Container Registry:

```bash
# find your gateway
kubectl get gateway -A
```

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
- **HTTPRoute** - Named `mcp-gateway-route`, routes traffic from the Gateway listener to the broker service for `/mcp` and `/.well-known/oauth-protected-resource`. The hostname is derived from the listener (wildcards like `*.example.com` become `mcp.example.com`). This can be disabled by setting `spec.httpRouteManagement: Disabled` on the MCPGatewayExtension if you need a custom HTTPRoute (e.g. with CORS headers or custom filters). Note: disabling does not delete a previously created `mcp-gateway-route`; you must remove it manually
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
| `--mcp-gateway-config` | `/config/config.yaml` | Fixed |

> **Note:** The router authenticates its own hairpin backend-init requests with a short-lived JWT signed by the same HMAC key as client session JWTs (`GATEWAY_SIGNING_KEY`). There is no separate router-key secret to manage; the previous `--mcp-router-key` flag has been removed.

The `--mcp-gateway-private-host` flag enables hair-pinning: when a `tools/call` request arrives, the router sends an `initialize` request back through the gateway to establish a backend session. The port in this address matches the listener port from the Gateway spec.

#### HTTPS Listeners and `--gateway-ca-cert`

When the targeted Gateway listener uses HTTPS, the controller automatically prepends `https://` to the private host. The broker-router's hairpin request connects to the internal service address but verifies the TLS certificate against the public hostname (`--mcp-gateway-public-host`).

For listeners using a **private CA** (e.g. cert-manager with a self-signed CA), the broker-router also needs the CA certificate to trust the gateway's TLS cert. Add the `--gateway-ca-cert` flag pointing to the CA cert file:

| Flag | Description |
|------|-------------|
| `--gateway-ca-cert` | Path to a PEM CA certificate file for the gateway's TLS listener. Only needed when the listener uses a private CA. |

The operator does not set this flag automatically. To configure it, mount the CA certificate as a volume and add the flag to the broker-router deployment:

```bash
kubectl patch deployment mcp-gateway -n mcp-system --type=json -p '[
  {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"gateway-ca","secret":{"secretName":"my-ca-bundle"}}},
  {"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"gateway-ca","mountPath":"/certs/gateway-ca.crt","subPath":"ca.crt","readOnly":true}},
  {"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"--gateway-ca-cert=/certs/gateway-ca.crt"}
]'
```

The operator preserves user-added volumes, volume mounts, and command flags across reconciliations.

#### Supported TLS Configurations

The gateway uses TLS termination (`tls.mode: Terminate`) on HTTPS listeners. Envoy decrypts incoming TLS, processes the request (ext_proc, AuthPolicy), then forwards **plain HTTP** to backends. This affects which backend setups work:

**Fully supported — tool discovery and tool calls work:**

- **HTTP listener → HTTP backend** — no TLS involved.
- **HTTPS listener → HTTP backend** — Envoy terminates client TLS, forwards HTTP to backend. Requires `--gateway-ca-cert` if the listener uses a private CA.
- **HTTPS listener → external HTTPS backend (with DestinationRule)** — Envoy terminates client TLS, then a DestinationRule configures TLS origination to the external service. See [External MCP Servers](./external-mcp-server.md).

**Partial support — tool discovery works, tool calls require additional configuration:**

- **Any listener → internal HTTPS backend (`caCertSecretRef`)** — the broker connects directly to the backend using the CA cert, so tool discovery works. However, `tools/call` is routed through Envoy, which sends plain HTTP to the TLS backend after termination. The backend rejects the non-TLS connection. To enable `tools/call`, create an Istio DestinationRule with TLS origination for that service — the same pattern used for [external MCP servers](./external-mcp-server.md). See the [Istio documentation on destination rule TLS settings](https://istio.io/latest/docs/reference/config/networking/destination-rule/#ClientTLSSettings) for configuring `credentialName` with a custom CA certificate.

#### HTTPS listener count

With HTTP, multiple listeners can share the same port — the client-facing listener and backend server listeners share a single Envoy route table, so the router can re-route `tools/call` requests to any backend on the same port.

With HTTPS, each listener gets its own TLS filter chain with an isolated route table (selected by SNI). The router re-routes `tools/call` by rewriting the `:authority` header, which can only reach routes within the same filter chain. A backend HTTPRoute on a separate HTTPS listener is unreachable from the client's filter chain. Use a **single HTTPS listener** with a wildcard hostname for both client and backend traffic. See [Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md) for examples.

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
