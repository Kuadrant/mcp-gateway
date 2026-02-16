# MCP Gateway Helm Chart

This directory contains the Helm chart for deploying MCP Gateway to Kubernetes.

> **Note:** This Helm chart is for **production deployments and distribution**. For local development, continue using the existing Kustomize-based workflow (`make local-env-setup`, `make deploy`, etc.).

## Overview

The MCP Gateway Helm chart deploys:
- **MCP Controller**: Manages MCPGatewayExtension, MCPServerRegistration, and MCPVirtualServer custom resources
- **MCPGatewayExtension**: Custom resource that triggers the controller to deploy the broker-router
- **Custom Resource Definitions (CRDs)**: MCPGatewayExtension, MCPServerRegistration, and MCPVirtualServer
- **RBAC**: Service accounts, roles, and bindings for secure operation

When the MCPGatewayExtension becomes ready, the controller automatically creates:
- **MCP Broker/Router**: Aggregates and routes MCP (Model Context Protocol) requests (Deployment + Service named `mcp-gateway`)
- **EnvoyFilter**: Configures Istio with the MCP Router ext-proc filter (created in the Gateway's namespace)

## Prerequisites

- **Gateway API Provider** (Istio) including Gateway API CRDs
- **Some MCP Server** At least 1 MCP server you want to route via the Gateway

### Install from Chart

```bash
helm install mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway --version 0.1.0 --create-namespace --namespace mcp-system
```

> **Note**: The chart defaults to the `mcp-system` namespace to match the controller's expectations.

### Install from Local Chart

```bash
# From the repository root
helm install mcp-gateway ./charts/mcp-gateway --create-namespace --namespace mcp-system
```

## Post Install Setup

**After installing the chart**, follow the complete post-installation setup instructions that are displayed by Helm. These instructions include:

- Verifying the MCPGatewayExtension is ready
- Verifying the broker-router deployment was created
- Connecting your MCP servers using MCPServerRegistration resources
- Accessing the gateway at your configured hostname

## Configuration

The chart uses sensible defaults and requires minimal configuration. The configurable values are:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Broker-router image repository | `ghcr.io/kuadrant/mcp-gateway` |
| `image.tag` | Broker-router image tag | Chart appVersion |
| `imageController.repository` | Controller image repository | `ghcr.io/kuadrant/mcp-controller` |
| `imageController.tag` | Controller image tag | Chart appVersion |
| `controller.enabled` | Enable controller deployment | `true` |
| `broker.pollInterval` | How often broker pings upstream MCP servers | `60s` |
| `gateway.publicHost` | Public hostname for MCP Gateway | `mcp.127-0-0-1.sslip.io` |
| `gateway.create` | Create a Gateway resource | `false` |
| `gateway.name` | Name of the Gateway | `mcp-gateway` |
| `gateway.namespace` | Namespace for the Gateway | `gateway-system` |
| `mcpGatewayExtension.create` | Create MCPGatewayExtension resource | `true` |
| `mcpGatewayExtension.gatewayRef.name` | Target Gateway name | `mcp-gateway` |
| `mcpGatewayExtension.gatewayRef.namespace` | Target Gateway namespace | `gateway-system` |

## Usage

### Creating MCP Servers

After installation, create MCPServerRegistration resources to connect MCP servers:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-mcp-server
spec:
  toolPrefix: "myserver_"
  targetRef:
    group: "gateway.networking.k8s.io"
    kind: "HTTPRoute"
    name: "my-server-route"
  # Optional: for servers requiring authentication
  credentialRef:
    name: "my-server-credentials"
    key: "token"
```

You'll find example MCP servers in https://github.com/Kuadrant/mcp-gateway/tree/main/config/test-servers
along with the corresponding MCPServerRegistration resources in https://github.com/Kuadrant/mcp-gateway/blob/main/config/samples/mcpserverregistration-test-servers-base.yaml and https://github.com/Kuadrant/mcp-gateway/blob/main/config/samples/mcpserverregistration-test-servers-extended.yaml

### Accessing the Gateway

Using your Client or Agent, configure the Gateway as an mcp server.
Here is an example config:

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "transport": {
        "type": "http",
        "url": "http://mcp.127-0-0-1.sslip.io/mcp"
      }
    }
  }
}
```

Alternatively, if using mcp-inspector or another mcp tool to interact directly with the gateway, point it to the hostname in the HTTPRoute e.g. http://mcp.127-0-0-1.sslip.io/mcp

## Upgrading

```bash
helm upgrade mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway --version 0.2.0
```

## Uninstalling

```bash
helm uninstall mcp-gateway

# Note: CRDs are not automatically removed
# Remove them manually if needed:
kubectl delete crd mcpgatewayextensions.mcp.kagenti.com
kubectl delete crd mcpserverregistrations.mcp.kagenti.com
kubectl delete crd mcpvirtualservers.mcp.kagenti.com
```

## Development

### CRD Synchronization

The CRDs in this Helm chart (`charts/mcp-gateway/crds/`) are synchronized from the source CRDs in `config/crd/`. When you modify Go types:

```bash
# Regenerate CRDs from Go types and sync to Helm chart
make generate-crds-all

# Or just sync existing CRDs to Helm chart
make update-helm-crds

# Check if CRDs are synchronized
make check-crd-sync
```

**Important:** Always run `make generate-crds-all` after modifying Go types in `api/` to keep both locations in sync.

### Testing Local Changes

```bash
# Lint the chart
helm lint ./charts/mcp-gateway

# Template and validate
helm template test ./charts/mcp-gateway --debug

# Install locally for testing
helm install test-mcp-gateway ./charts/mcp-gateway \
  --create-namespace --namespace mcp-system --dry-run --debug
```

### Publishing New Versions

The chart is automatically published to GHCR via GitHub Actions. To release a new version:

1. Go to GitHub Actions → "Helm Chart Release"
2. Click "Run workflow"
3. Enter the desired chart version (e.g., `0.2.0`)
4. Optionally specify app version
5. Run the workflow
