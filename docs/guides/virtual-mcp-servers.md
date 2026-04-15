# Virtual MCP Servers Configuration

This guide covers configuring virtual MCP servers to create focused, curated tool collections from your aggregated MCP servers.

## Overview

Virtual MCP servers solve a common problem when using MCP Gateway: while aggregating all your MCP tools centrally provides excellent benefits for authentication, authorization, and configuration management, it can overwhelm LLMs and AI agents with too many tools to choose from.

**Why use virtual MCP servers:**
- **Focused Tool Sets**: Create specialized collections of tools for specific use cases
- **Improved AI Performance**: Reduce cognitive load on LLMs by presenting fewer, more relevant tools
- **Domain-Specific Interfaces**: Group tools by function (e.g., "development tools", "data analysis tools")
- **Simplified Discovery**: Make it easier for users and agents to find the right tools
- **Layered Access Control**: Combine with authorization policies for fine-grained access management

Virtual servers work by filtering the complete tool list based on a curated selection, accessed via HTTP headers.

## Prerequisites

- MCP Gateway installed and configured
- [MCP servers configured](./register-mcp-servers.md) with tools available
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [jq](https://jqlang.github.io/jq/download/) installed

## Understanding Virtual Servers

A virtual MCP server is defined by an `MCPVirtualServer` custom resource that specifies:
- **Tool Selection**: Which tools from the aggregated pool to expose
- **Description**: Human-readable description of the virtual server's purpose
- **Access Method**: Accessed via `X-Mcp-Virtualserver` header with `namespace/name` format

When a client includes the virtual server header, MCP Gateway filters responses to only include the specified tools.

## Step 1: Discover Available Tools

Before creating virtual servers, check which tools are available in your gateway. You can browse them using [MCP Inspector](https://github.com/modelcontextprotocol/inspector):

```bash
DANGEROUSLY_OMIT_AUTH=true npx @modelcontextprotocol/inspector@0.21.1
```

> **Note:** `DANGEROUSLY_OMIT_AUTH=true` is for local testing only. Do not use it in shared or production environments.

Open the inspector at [http://localhost:6274](http://localhost:6274) and connect to your gateway using **Streamable HTTP** transport with your gateway URL (e.g. `http://mcp.127-0-0-1.sslip.io:8001/mcp`). The **Tools** tab lists all available tool names -- you'll use these in the next step.

## Step 2: Create Virtual Server Definitions

Create virtual servers for different use cases. Replace the example tool names below with tools from your gateway:

### Development Tools Virtual Server

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: dev-tools
  namespace: mcp-system
spec:
  description: "Development and debugging tools"
  tools:
  - test1_greet             # replace with your actual tool names
  - test1_headers
  - test2_hello_world
EOF
```

### Data Analysis Virtual Server

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: data-tools
  namespace: mcp-system
spec:
  description: "Data analysis and reporting tools"
  tools:
  - test2_time             # replace with your actual tool names
  - test3_dozen
  - test3_add
EOF
```

## Step 3: Verify Virtual Server Creation

Check that your virtual servers were created successfully:

```bash
kubectl get mcpvirtualserver -A
```

Expected output:

```text
NAMESPACE    NAME         TOOLS   AGE
mcp-system   data-tools           10s
mcp-system   dev-tools            15s
```

## Step 4: Test Virtual Server Access

Test your virtual servers using curl with the appropriate header:

### Test Development Tools Virtual Server

```bash
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

# Extract the MCP session ID from response headers
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')

echo "MCP Session ID: $SESSION_ID"

# Request tools from the dev-tools virtual server
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "X-Mcp-Virtualserver: mcp-system/dev-tools" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}' | jq '.result.tools[].name'
```

**Expected Response**: Only tools specified in the `dev-tools` virtual server (the example tools you configured)

### Test Data Analysis Virtual Server

```bash
# Request tools from the data-tools virtual server
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "X-Mcp-Virtualserver: mcp-system/data-tools" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}' | jq '.result.tools[].name'
```

**Expected Response**: Only tools specified in the `data-tools` virtual server (the example tools you configured)

### Test Without Virtual Server Header

```bash
# Request all available tools (no filtering)
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "mcp-session-id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}' | jq '.result.tools[].name'
```

**Expected Response**: All tools from all configured MCP servers

## Step 5: Use with MCP Inspector

You can also test virtual servers using MCP Inspector. Connect to your gateway as described in [Step 1](#step-1-discover-available-tools), then add the `X-Mcp-Virtualserver` header with the `namespace/name` of your virtual server (e.g. `mcp-system/dev-tools`) under the **Headers** section. The tools list will show only the tools defined in that virtual server.

## Remove Virtual Servers

```bash
kubectl delete mcpvirtualserver dev-tools data-tools -n mcp-system
```

## Authorization and Tool Filtering

Virtual MCP servers are a `tools/list` concept only. They filter which tools a client discovers, but do not affect `tools/call` routing or authorization.

If you have [authentication](./authentication.md) and [user-based tool filtering](./user-based-tool-filter.md) configured, the broker applies two filters sequentially when handling a `tools/list` request with a virtual server header:

1. **Identity-based filtering** -- the `x-authorized-tools` reduces the tool list to only tools the user has `resource_access` roles for
2. **Virtual server filtering** -- the `X-Mcp-Virtualserver` header further reduces the list to only tools defined in the MCPVirtualServer resource

The result is the intersection of both filters. For example, if the `accounting` virtual server lists `test1_greet` and `test3_add`, but the user only has the `greet` role on `mcp-test/test-server1`, they will only see `test1_greet`.

No additional AuthPolicy configuration is needed for virtual servers beyond what is already set up for regular MCP server authorization.

## Next Steps

With virtual MCP servers configured, you can:
- **[Configure Authentication](./authentication.md)** - Add user identity validation to virtual servers
- **[Configure Authorization](./authorization.md)** - Add access control to virtual servers
- **[User-Based Tool Filtering](./user-based-tool-filter.md)** - Identity-based tool filtering via trusted headers
- **[External MCP Servers](./external-mcp-server.md)** - Include external tools in virtual servers
