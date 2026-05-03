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

## Compatibility Matrix

The MCP Gateway is tested against the following infrastructure versions:

| Component | Tested Version | Compatibility |
|-----------|----------------|---------------|
| Kubernetes | v1.31.0 | v1.28.0+ |
| Gateway API | v1.2.1 | v1.0.0+ |
| Istio | v1.24.1 | v1.22.0+ |
| OLM | v0.30.0 | v0.25.0+ |

> [!NOTE]
> While other Gateway API providers (like Nginx or Contour) may work, Istio is the primary provider used for development and CI.

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

## Minimal Usage Example

Installing via the methods above deploys the **operator only**. To actually use the MCP Gateway, you must create a `Gateway` (provided by your infrastructure) and an `MCPGatewayExtension` (provided by this project).

### 1. Create a Gateway
Save as `my-gateway.yaml`:
```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway
  namespace: gateway-system
spec:
  gatewayClassName: istio
  listeners:
    - name: mcp
      hostname: "mcp.example.com"
      port: 8080
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: All
```

### 2. Create an MCPGatewayExtension
Save as `my-extension.yaml`:
```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway-extension
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
    sectionName: mcp
  publicHost: "mcp.example.com"
```

Apply both:
```bash
kubectl apply -f my-gateway.yaml
kubectl apply -f my-extension.yaml
```

The operator will detect these resources and automatically deploy the `mcp-gateway` (broker-router) deployment.

## Post-Installation

After installing the controller, you must configure a Gateway and register MCP servers:

1. **[Configure Gateway Listener and Route](./configure-mcp-gateway-listener-and-router.md)**
2. **[Register MCP Servers](./register-mcp-servers.md)**
3. **[Connect to External MCP Servers](./external-mcp-server.md)**

## Development and Testing

> [!IMPORTANT]
> The project includes several "full stack" setup scripts and Make targets. These are **strictly for development and evaluation** and should not be used in production as they bundle specific versions of Istio, Keycloak, and other components.

| Method | Target | Description |
|--------|--------|-------------|
| **Quick Start** | `scripts/quick-start.sh` | Automated setup of Kind + Istio + MCP Gateway |
| **Make Dev** | `make local-env-setup` | Full dev environment with test servers |
| **Make OLM Dev** | `make local-env-setup-olm` | Local OLM-based dev environment |

For production, always use the OLM or Helm/Kustomize methods described in the sections above and provide your own infrastructure.
