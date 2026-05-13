# Tool Discovery

This guide explains the tool discovery behavior that MCP Gateway supports today.

## Current behavior

MCP Gateway federates tools from every Ready `MCPServerRegistration` into the gateway's standard MCP `tools/list` response. Clients discover tools by connecting to the gateway and calling `tools/list`.

Today, MCP Gateway does **not** expose progressive discovery meta-tools such as `discover_tools` or `select_tools`, and `MCPServerRegistration` does **not** support discovery metadata fields such as `category` or `hint`.

If you are looking for that future direction, see the design proposal in [docs/design/tool-discovery.md](../design/tool-discovery.md). Treat that design doc as roadmap material rather than current product behavior.

## Prerequisites

- A running MCP Gateway deployment
- One or more Ready `MCPServerRegistration` resources
- An MCP client such as MCP Inspector

## Discover tools through the gateway

Start the local demo environment if needed:

```bash
make local-env-setup
```

Open MCP Inspector:

```bash
make inspect-gateway
```

Connect MCP Inspector to:

```text
http://mcp.127-0-0-1.sslip.io:8001/mcp
```

After connecting, use **Tools -> List Tools** to view the federated tool catalog returned by the gateway.

## Verify which servers contributed tools

Check the registration status first:

```bash
kubectl get mcpsr -A
```

Example output:

```text
NAMESPACE   NAME            PREFIX      TARGET               PATH   READY   TOOLS   CREDENTIALS   AGE
mcp-test    test-server1    test1_      mcp-server1-route    /mcp   True    5                     2m
mcp-test    test-server2    test2_      mcp-server2-route    /mcp   True    7                     2m
```

The `PREFIX` column tells you how the gateway namespaces tools from each backend server. For example, a backend `greet` tool registered with prefix `test1_` appears to clients as `test1_greet`.

If a registration is not Ready, inspect it for details:

```bash
kubectl describe mcpserverregistration test-server1 -n mcp-test
```

## Keep the tool set focused

Because clients currently receive the gateway's normal federated `tools/list`, the main ways to keep tool discovery manageable are:

- Register only the MCP servers you want exposed through a given gateway listener
- Use prefixes consistently so tool ownership is obvious
- Use `MCPVirtualServer` resources when you want a curated subset of tools for a specific endpoint

For virtual server setup, see [virtual-mcp-servers.md](./virtual-mcp-servers.md).
