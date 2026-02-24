# Quick Start Guide

Get MCP Gateway running in 5 minutes with our automated setup script.

## Overview

This guide uses an automated script that sets up everything you need:
- Local Kind Kubernetes cluster
- Gateway API CRDs
- Istio service mesh
- MCP Gateway with Helm
- Example MCP servers
- Gateway routing configuration
- MCP Inspector for testing

Perfect for evaluation, demos, and getting started quickly.

## Prerequisites

- [Docker](https://docs.docker.com/engine/install/) or [Podman](https://podman.io/docs/installation) installed and running
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) installed
- [Helm](https://helm.sh/docs/intro/install/) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Node.js and npm](https://nodejs.org/en/download/) installed (for MCP Inspector)

## Quick Setup

Run the automated setup script:

```bash
# change this to the release you want to use if looking to not use the main branch
export MCP_GATEWAY_BRANCH=main 
# Download and run the setup script
curl -sSL https://raw.githubusercontent.com/Kuadrant/mcp-gateway/refs/heads/${MCP_GATEWAY_BRANCH}/charts/sample_local_helm_setup.sh | bash
```

**Or clone the repository and run locally:**

```bash
git clone https://github.com/Kuadrant/mcp-gateway.git
cd mcp-gateway
USE_LOCAL_CHART=true ./charts/sample_local_helm_setup.sh
```

## What the Script Does

The setup script automatically:

1. **Creates Kind cluster** with proper port mappings
2. **Installs Gateway API CRDs** for traffic routing
3. **Deploys Istio** service mesh with Helm
4. **Sets up Istio Gateway** for external traffic
5. **Installs MCP Gateway** using the Helm chart
6. **Deploys test MCP servers** for demonstration
7. **Configures routing** with HTTPRoute resources
9. **Launches MCP Inspector** for testing and exploration

## Testing Your Setup

Once the script completes, you'll have:

### MCP Inspector Access
- **URL**: http://localhost:6274
- **Gateway URL**: http://mcp.127-0-0-1.sslip.io:7001/mcp
- **Pre-configured**: The inspector opens with the correct gateway URL

### Available Test Tools
The setup includes example MCP servers with tools like:
- `test1_hello_world` - Simple greeting tool
- `test1_headers` - HTTP header inspection
- `test2_headers` - Additional header tool

### Try It Out
1. **Connect**: MCP Inspector should open automatically
2. **Initialize**: Click "Connect" to initialize the MCP session
3. **Explore Tools**: Browse available tools in the left panel
4. **Test Tools**: Try calling `test1_hello_world` or `test1_headers`
5. **View Logs**: Check the request/response flow

## Cleanup

To stop the services and clean up:

```bash
# Then delete the Kind cluster
kind delete cluster
```

## Next Steps

Now that you have MCP Gateway running, explore other features:

- **[Authentication](./authentication.md)** - Configure OAuth-based security with Keycloak
- **[Authorization](./authorization.md)** - Set up fine-grained access
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Create focused tool collections for specific use cases
- **[External MCP Servers](./external-mcp-server.md)** - Connect to external APIs and services
