# MCP Gateway Helm Chart

This directory contains the Helm chart for deploying MCP Gateway to Kubernetes.

> **Note:** This Helm chart is for **production deployments and distribution**. For local development, continue using the existing Kustomize-based workflow (`make local-env-setup`, `make deploy`, etc.).

## Overview

The MCP Gateway Helm chart deploys:
- **MCP Broker/Router**: Aggregates and routes MCP (Model Context Protocol) requests
- **MCP Controller**: Manages MCPServer and MCPVirtualServer custom resources
- **Custom Resource Definitions (CRDs)**: MCPServer and MCPVirtualServer
- **RBAC**: Service accounts, roles, and bindings for secure operation
- **EnvoyFilter**: Configures Istio with the MCP Router ext-proc filter (enabled by default)

## Prerequisites

- **Gateway API Provider** (Istio) including Gateway API CRDs
- **Some MCP Server** At least 1 MCP server you want to route via the Gateway

### Install from Chart

```bash
helm install mcp-gateway oci://ghcr.io/kagenti/charts/mcp-gateway --version 0.1.0 --create-namespace --namespace mcp-system
```

> **Note**: The chart defaults to the `mcp-system` namespace to match the controller's expectations.

### Install from Local Chart

```bash
# From the repository root
helm install mcp-gateway ./charts/mcp-gateway --create-namespace --namespace mcp-system
```

## Post Install Setup

**After installing the chart**, follow the complete post-installation setup instructions that are displayed by Helm. These instructions include:

- Configuring your Gateway with an MCP listener  
- Creating an HTTPRoute to route traffic to the broker
- Connecting your MCP servers using MCPServer resources
- Accessing the gateway at your configured hostname

## Configuration

The chart uses sensible defaults and requires minimal configuration. The configurable values are:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Image repository | `ghcr.io/kagenti/mcp-gateway` |
| `image.tag` | Image tag | `latest` |
| `configMap.create` | Create initial ConfigMap | `true` |
| `envoyFilter.create` | Create EnvoyFilter for Istio integration | `true` |
| `envoyFilter.namespace` | Namespace for EnvoyFilter | `istio-system` |
| `gateway.publicHost` | Public hostname for MCP Gateway (required) | `mcp.127-0-0-1.sslip.io` |

## Usage

### Creating MCP Servers

After installation, create MCPServer resources to connect MCP servers:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServer
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

You'll find example mcp servers in https://github.com/kagenti/mcp-gateway/tree/main/config/test-servers
along with the corresponding MCPServer resources in https://github.com/kagenti/mcp-gateway/blob/main/config/samples/mcpserver-test-servers.yaml

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
helm upgrade mcp-gateway oci://ghcr.io/kagenti/charts/mcp-gateway --version 0.2.0
```

## Uninstalling

```bash
helm uninstall mcp-gateway

# Note: CRDs are not automatically removed
# Remove them manually if needed:
kubectl delete crd mcpservers.mcp.kagenti.com
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
