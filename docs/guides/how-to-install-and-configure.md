# Installing and Configuring MCP Gateway

This guide demonstrates how to install and configure the MCP Gateway. The project supports two primary deployment methods:
- **OLM (Operator Lifecycle Manager)**: Recommended for OpenShift environments.
- **Kustomize / Helm**: Standard Kubernetes deployment for the MCP Gateway operator.

## Prerequisites

Before installing MCP Gateway, ensure your cluster meets the following requirements. These dependencies are **not** bundled with the MCP Gateway installation.

1. **Kubernetes Cluster**: Version 1.28.0 or later.
2. **Gateway API CRDs**: Version 1.0 or later.
   - [Official Installation Guide](https://gateway-api.networking.k8s.io/guides/#installing-gateway-api)
3. **Gateway API Provider (e.g., Istio)**:
   - [Istio Installation Guide](https://istio.io/latest/docs/setup/install/)
4. **Kuadrant (Optional)**: For advanced authentication and authorization policies.
   - [Kuadrant Installation Guide](https://kuadrant.io/docs/kuadrant-operator/latest/usage/install-kuadrant/)

## Supported Deployment Methods

### 1. OLM (Recommended for OpenShift)

The Operator Lifecycle Manager path provides a managed experience with automated updates.

See the **[OLM Installation Guide](./olm-install.md)** for detailed instructions.

### 2. Helm / Kustomize

Standard Kubernetes manifests for deploying the MCP Gateway controller.

#### Helm

```bash
export MCP_GATEWAY_VERSION=0.5.1
helm upgrade -i mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
  --version ${MCP_GATEWAY_VERSION} \
  --namespace mcp-system \
  --create-namespace \
  --set controller.enabled=true
```

#### Kustomize

```bash
export MCP_GATEWAY_VERSION=0.5.1
kubectl apply -k "https://github.com/kuadrant/mcp-gateway/config/mcp-gateway/overlays/mcp-system?ref=v${MCP_GATEWAY_VERSION}"
```

## Post-Installation

After installing the controller, you must configure a Gateway and register MCP servers:

1. **[Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md)**
2. **[Register MCP Servers](./register-mcp-servers.md)**
3. **[Connect to External MCP Servers](./external-mcp-server.md)**

## Local Development

For quick evaluation and local development, we provide automated scripts that set up a Kind cluster with all prerequisites. These are **not** intended for production use.

- **[Quick Start Guide](./quick-start.md)**
- `make local-env-setup`
