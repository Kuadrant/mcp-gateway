# Quick Start Guide

Get MCP Gateway running locally in a Kind cluster.

## Prerequisites

- [Docker](https://docs.docker.com/engine/install/) or [Podman](https://podman.io/docs/installation) installed and running
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) installed
- [Helm](https://helm.sh/docs/intro/install/) installed (for Istio)
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed

## Quick Setup

Set the release version and run the setup script:

```bash
export MCP_GATEWAY_VERSION=0.6.0
curl -sSL https://raw.githubusercontent.com/Kuadrant/mcp-gateway/main/scripts/quick-start.sh | bash
```

The script checks prerequisites, then runs through each step automatically:

1. **Create Kind cluster** with port mapping (`localhost:8001`)
2. **Install Gateway API CRDs and Istio** as the Gateway API provider
3. **Create the Gateway** with listeners and a NodePort service
4. **Install MCP Gateway** CRDs, controller, and MCPGatewayExtension (the controller automatically deploys the broker-router)
5. **Deploy test MCP servers** and register them with the gateway

## Test with MCP Inspector

Once the script completes, you can use [MCP Inspector](https://github.com/modelcontextprotocol/inspector) to interact with the gateway. This requires [Node.js and npm](https://nodejs.org/en/download/).

```bash
DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@0.21.1
```

Open the inspector at [http://localhost:6274](http://localhost:6274) and configure:

- **Transport**: Streamable HTTP
- **URL**: `http://mcp.127-0-0-1.sslip.io:8001/mcp`

Click **Connect**, then browse and test the available tools:

| Tool | Description |
|------|-------------|
| `test1_greet` | Simple greeting |
| `test1_time` | Current time |
| `test1_slow` | Delay N seconds |
| `test1_headers` | HTTP header inspection |
| `test1_add_tool` | Dynamically add a new tool |
| `test2_hello_world` | Greeting from server 2 |
| `test2_time` | Current time from server 2 |
| `test2_headers` | HTTP headers from server 2 |
| `test2_auth1234` | Auth test from server 2 |
| `test2_slow` | Delay from server 2 |
| `test2_set_time` | Set time from server 2 |
| `test2_pour_chocolate_into_mold` | Chocolate mold from server 2 |

## Cleanup

```bash
kind delete cluster
```

## API Reference

- [MCPGatewayExtension](../reference/mcpgatewayextension.md)
- [MCPServerRegistration](../reference/mcpserverregistration.md)
- [MCPVirtualServer](../reference/mcpvirtualserver.md)

## Next Steps

- **[Authentication](./authentication.md)** - Configure OAuth-based security with Keycloak
- **[Authorization](./authorization.md)** - Set up fine-grained access control
- **[Virtual MCP Servers](./virtual-mcp-servers.md)** - Create focused tool collections for specific use cases
- **[External MCP Servers](./external-mcp-server.md)** - Connect to external APIs and services
