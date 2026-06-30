# Tool Discovery

This guide covers configuring progressive tool discovery on the MCP Gateway. Tool discovery lets agents browse and select tools from a lightweight catalogue instead of receiving every tool schema upfront, reducing context window usage and improving tool selection accuracy.

## Prerequisites

- MCP Gateway installed and configured
- At least one MCPServerRegistration registered with the gateway
- An MCP client that supports `tools/list` and tool-call operations (e.g., Claude Code, Claude Desktop)

## How It Works

When tool discovery is active, new sessions see only two meta-tools:

- `discover_tools` -- returns server names, categories, hints, and tool names (no full schemas)
- `select_tools` -- scopes the session to a chosen subset of tools

After `select_tools`, the gateway sends a `notifications/tools/list_changed` notification. The client's next `tools/list` call returns only the selected tools with full schemas.

The agent drives selection using natural language understanding of the task, not keyword matching. This means an agent asked to "book a restaurant for Saturday" can identify both dining and calendar tools from the catalogue without needing an exact keyword match.

## Step 1: Add Discovery Metadata to MCPServerRegistrations

Add `category` and `hint` fields to your MCPServerRegistration resources. These fields populate the catalogue returned by `discover_tools`.

> **Note:** The quality of tool discovery depends directly on the accuracy of these fields. Agents use categories and hints to decide which tools are relevant to a task. Vague or incorrect metadata leads to agents selecting the wrong tools or missing relevant ones entirely. Be specific: prefer "current weather conditions and multi-day forecasts by location" over "weather stuff".

Single category:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: weather-server
  namespace: mcp-test
spec:
  prefix: weather_
  category:
  - "weather"
  hint: "current conditions and forecasts by location"
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: weather-route
EOF
```

Multiple categories:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: scheduling-server
  namespace: mcp-test
spec:
  prefix: scheduling_
  category:
  - "calendar"
  - "scheduling"
  - "productivity"
  hint: "manage calendar events, check availability, book meetings"
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: scheduling-route
EOF
```

- `category` is a string array. Default: `["uncategorised"]`. Max 3 items, max 128 characters each.
- `hint` is a short description. Max 256 characters. Give the agent enough context to judge relevance without full schemas.

> **Note:** Accuracy matters. Agents rely on these fields to decide which tools to load. Vague or incorrect metadata defeats the purpose of discovery.

Verify the registration:

```bash
kubectl get mcpserverregistrations -n mcp-test
```

## Step 2: Configure the Discovery Threshold

The `--discovery-tool-threshold` flag controls when the gateway hides real tools and shows only meta-tools. It applies after auth and virtual server filtering.

| Threshold value | Behaviour |
|-|-|
| `0` (default) | Never hide tools. Meta-tools are available but all tools are also returned in `tools/list`. Agents can optionally use discovery to reduce their working set |
| Positive value (e.g., `20`) | When the visible tool count exceeds the threshold, `tools/list` returns only meta-tools. Agents must use `discover_tools` and `select_tools` to access real tools |

To set the threshold:

```bash
kubectl -n mcp-system patch deployment mcp-gateway --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"--discovery-tool-threshold=20"}]'
```

> **Note:** This is a command-line flag on the broker-router binary, not an environment variable. Changing it triggers a pod restart.

Verify the flag was applied:

```bash
kubectl -n mcp-system get deployment mcp-gateway \
  -o jsonpath='{.spec.template.spec.containers[0].command}' | python3 -m json.tool
```

## Step 3: Verify the Agent Flow

Once configured, agents follow this flow:

1. **Discover**: the agent calls `discover_tools`, optionally with a `category` filter, and receives lightweight metadata about available servers and tools.

2. **Select**: the agent calls `select_tools` with the tool names it needs. The gateway scopes the session and sends `notifications/tools/list_changed`.

3. **Work**: the agent's next `tools/list` returns full schemas for only the selected tools plus the two meta-tools.

Example `discover_tools` response:

```json
{
  "servers": [
    {
      "name": "mcp-test/weather-server",
      "categories": ["weather"],
      "hint": "current conditions and forecasts by location",
      "tools": ["weather_get_forecast", "weather_current_conditions"]
    }
  ]
}
```

### Category Filtering

The `discover_tools` tool accepts an optional `category` parameter. The match is case-insensitive and checks against any element in the server's category array.

Calling `discover_tools` with `{"category": "Calendar"}` matches a server with `category: ["calendar", "scheduling"]`.

### Re-scoping Mid-conversation

If the conversation shifts, the agent calls `select_tools` again with a new set of tool names. The previous scope is replaced entirely.

### Resetting to the Full Tool Set

Calling `select_tools` with an empty list resets the session scope. The next `tools/list` returns all tools (subject to threshold behaviour).

## Gateway Instructions

When discovery is enabled, the gateway includes server-level instructions in the MCP `initialize` response that explain the discovery flow to MCP clients:

```text
This is an MCP Gateway that aggregates tools from multiple backend MCP servers
into a single endpoint. The full tool set may be large.

To avoid loading all tool schemas upfront, use the discovery tools:
1. Call discover_tools to browse available servers, categories, and tool names
   (lightweight, no full schemas).
2. Call select_tools with the tool names relevant to your task. This scopes your
   session -- subsequent tools/list calls will return only the selected tools
   with full schemas.
3. To change scope, call select_tools again with a new set. Pass an empty list
   to reset to the full tool set.
```

Clients that support the MCP `instructions` field will surface this to the agent automatically.

## Interaction with Auth and Virtual Servers

Discovery respects the existing filtering pipeline. The order is:

1. Auth filtering (JWT-based `x-mcp-authorized` claims)
2. Virtual server filtering (MCPVirtualServer scoping)
3. Scope filtering (discovery selection)

Both `discover_tools` and `select_tools` only operate on tools visible after auth and virtual server filtering. An agent cannot discover or select tools it is not authorised to use.

If a virtual server is configured with explicit tool lists, tools outside those lists are not visible regardless of what the agent selects.

## Error Handling

Tool selection is all-or-nothing. If any requested tool is not available (whether it does not exist, is not authorised, or is a meta-tool), `select_tools` returns a generic error:

```json
{"error": "tool not available"}
```

The error message is deliberately generic. It does not reveal whether the tool exists, is unauthorised, or was rejected for another reason. This prevents probing for tool names or authorisation boundaries.

## Disabling Discovery

The `--discovery-tools-enabled` flag (default: `true`) controls whether meta-tools are registered. To disable:

```bash
kubectl -n mcp-system patch deployment mcp-gateway --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"--discovery-tools-enabled=false"}]'
```

> **Note:** This is a command-line flag, not an environment variable. Changing it triggers a pod restart.

When disabled:

- `discover_tools` and `select_tools` are not registered as tools
- All tools are returned directly in `tools/list` (subject to auth and virtual server filtering)
- Gateway instructions are not included in the `initialize` response
- If a client attempts to call `discover_tools` or `select_tools`, the request fails with a standard MCP "tool not found" error

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md) -- configure MCPServerRegistrations
- [Virtual MCP Servers](./virtual-mcp-servers.md) -- create curated tool subsets by category
- [Authorization](./authorization.md) -- configure tool-level access control
- [Authentication](./authentication.md) -- set up OIDC authentication
