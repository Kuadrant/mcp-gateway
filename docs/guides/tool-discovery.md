# Tool Discovery Walkthrough

This guide walks through the progressive tool discovery feature using a local Kind cluster. You will deploy the gateway with demo MCP servers, connect an AI agent, and observe how the agent uses `discover_tools` and `select_tools` to find and scope relevant tools instead of receiving the full tool catalog upfront.

## Prerequisites

- [Docker](https://docs.docker.com/engine/install/) or [Podman](https://podman.io/docs/installation) installed and running
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [Helm](https://helm.sh/docs/intro/install/) installed
- An MCP-capable AI agent (e.g., Claude Desktop, Claude Code, or any MCP client)
- The MCP Gateway repository cloned locally

## Step 1: Set up the local environment

From the repository root, create a Kind cluster with the gateway and test servers:

```bash
make local-env-setup
```

This creates a Kind cluster, installs Istio as the Gateway API provider, deploys the MCP Gateway controller and broker, and registers the default test MCP servers.

Verify the gateway is running:

```bash
kubectl -n mcp-system get deployment mcp-gateway
```

Expected output:

```
NAME          READY   UP-TO-DATE   AVAILABLE   AGE
mcp-gateway   1/1     1            1           ...
```

## Step 2: Apply discovery metadata

`make local-env-setup` already builds and deploys all test servers (including the restaurant and messaging servers) and registers them with the base MCPServerRegistrations. To enable discovery metadata (categories and hints), apply the discovery variant:

```bash
kubectl apply -f config/samples/mcpserverregistration-discovery.yaml
```

Verify the registrations have the expected categories:

```bash
kubectl -n mcp-test get mcpserverregistrations
```

Expected output shows servers with discovery metadata alongside the base registrations:

```
NAME                PREFIX        TARGET                        PATH   READY   TOOLS   CREDENTIALS   AGE
restaurant-server   restaurant_   mcp-restaurant-server-route   /mcp   True    5                     ...
messaging-server    messaging_    mcp-messaging-server-route    /mcp   True    5                     ...
test-server1        test1_        mcp-server1-route             /mcp   True    5                     ...
test-server2        test2_        mcp-server2-route             /mcp   True    7                     ...
test-server3        test3_        mcp-server3-route             /mcp   True    7                     ...
...
```

At this point the gateway federates tools from multiple servers. The total tool count exceeds the default discovery threshold of 10, so new sessions will only see the discovery meta-tools.

## Step 3: Connect the gateway to your AI agent

The gateway is accessible at `http://mcp.127-0-0-1.sslip.io:8001/mcp`.

### Claude Code

```bash
claude mcp add --transport http -s user mcp-gateway http://mcp.127-0-0-1.sslip.io:8001/mcp
```

### MCP Inspector (for manual testing)

```bash
make inspect-gateway
```

## Step 4: Observe progressive discovery

Send the following prompt to your agent:

> Using the mcp-gateway, I would like to book an Italian restaurant in New York for 4 people on Saturday. After each turn, show me the tools in your context.

### What to expect

**Turn 1 — Initial tool list**

The agent's initial `tools/list` call returns only two tools:

- `discover_tools` — browse available servers, categories, and tool names
- `select_tools` — scope the session to specific tools

No upstream tools are visible. The agent must use the discovery flow.

**Turn 2 — Discovery**

The agent calls `discover_tools` and receives lightweight metadata about all registered servers:

```json
{
  "servers": [
    {
      "name": "mcp-test/restaurant-server",
      "category": "dining reservations",
      "hint": "search restaurants by cuisine and location, check table availability, make and cancel reservations",
      "tools": ["restaurant_search_restaurants", "restaurant_get_restaurant_details", "restaurant_check_availability", "restaurant_make_reservation", "restaurant_cancel_reservation"]
    },
    {
      "name": "mcp-test/messaging-server",
      "category": "communication contacts",
      "hint": "find contacts, send messages via email/sms/slack, view message history, create messaging groups",
      "tools": ["messaging_find_contacts", "messaging_get_contact", "messaging_send_message", "messaging_get_messages", "messaging_create_group"]
    },
    ...
  ]
}
```

The agent identifies the restaurant tools as relevant and calls `select_tools` with those tool names.

**Turn 3 — Scoped tools**

After `select_tools`, the gateway sends a `notifications/tools/list_changed` notification. The agent's next `tools/list` call returns only the selected restaurant tools plus the two meta-tools. The agent now has full schemas for just the tools it needs and proceeds to search restaurants, check availability, and make a reservation.

### Key observations

- **Before discovery**: the agent sees 2 tools (meta-tools only)
- **After select_tools**: the agent sees ~7 tools (5 restaurant + 2 meta-tools) instead of 30+ from all servers
- **Token savings**: the agent never ingests schemas for messaging, math, greeting, or other irrelevant tools
- **Re-scoping**: if the conversation shifts (e.g., "now send a confirmation message"), the agent can call `discover_tools` again and `select_tools` with a different set

### Shifting context mid-conversation

After the restaurant is booked, send a follow-up prompt that requires a different set of tools:

> Great but now I would like to invite my friends. Can show me my contacts so I can message them?

The agent recognises that messaging tools are needed. It calls `discover_tools` again, identifies the messaging server, and calls `select_tools` with the messaging tool names. After re-scoping, the agent's tool list changes from the restaurant tools to the messaging tools (plus the two meta-tools). The agent can then look up contacts and send invitations without ever loading the full tool set.

## Configuration

### Discovery threshold

The `--discovery-tool-threshold` flag (default: 10) controls when progressive discovery activates. When the total number of non-meta tools exceeds this threshold, new sessions only see meta-tools. At or below the threshold, all tools are shown directly.

To change the threshold on a running deployment:

```bash
kubectl -n mcp-system set env deployment/mcp-gateway -- DISCOVERY_TOOL_THRESHOLD=20
```

Or add the flag to the deployment command args:

```bash
kubectl -n mcp-system patch deployment mcp-gateway --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"--discovery-tool-threshold=20"}]'
```

Setting the threshold to 0 always requires discovery regardless of tool count.

### Adding discovery metadata to your servers

Add `category` and `hint` fields to your MCPServerRegistration resources:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-server
  namespace: mcp-test
spec:
  toolPrefix: myprefix_
  category: "my domain"
  hint: "short description of what tools this server provides"
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-server-route
```

- **category**: free-text classification (e.g., "payments", "analytics", "communication"). Servers without a category appear as "uncategorised".
- **hint**: natural-language summary of the server's tools. Gives the LLM enough context to decide relevance without full schemas.
