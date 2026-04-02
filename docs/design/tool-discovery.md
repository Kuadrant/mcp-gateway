# Tool Discovery and Scoping

## Problem

The MCP Gateway federates tools from multiple upstream MCP servers and exposes them to clients via the `tools/list` endpoint. Today, every connected client receives the full set of tools it is entitled to access. As the number of upstream MCP servers and registered tools grows, this creates several problems:

### Flat, unbounded tool lists

When a gateway federates dozens of upstream servers, each exposing multiple tools, the `tools/list` response becomes a large flat list. AI agents consuming this list must process every tool definition (name, description, input schema) to determine which ones are relevant to their current task. This has direct consequences:

- **Context window pressure and token cost**: LLM-based agents have finite context windows. Injecting hundreds of tool definitions consumes tokens that could be used for reasoning, conversation history, or user instructions. At scale, tool definitions alone can exhaust a significant portion of the available context. A tool definition includes a full API schema object. Beyond the context limit, every token carrying an irrelevant tool definition is a direct cost — agents pay per token on every request, so oversized tool lists increase the cost of every single interaction.

- **Degraded tool selection**: Studies and practical experience show that LLMs make worse tool-calling decisions as the number of available tools increases. With a small, focused set of tools, models reliably select the right one. With hundreds of tools, they hallucinate tool names, pick semantically similar but wrong tools, or fail to call any tool at all.

- **Increased latency**: Larger tool lists mean larger `tools/list` responses, more data over the wire, and more processing time on the client side before the agent can begin reasoning.

### No context-aware discovery

The current `tools/list` endpoint is stateless and context-free. It returns the same set of tools regardless of what the agent is trying to accomplish. There is no mechanism for an agent to say "I'm working on a data analysis task" and receive only the tools relevant to that domain. The only filtering available today is:

- **MCPVirtualServer**: a static, operator-defined subset of tools exposed on a specific endpoint. This is useful for coarse segmentation but requires upfront configuration and doesn't adapt to the agent's runtime context.

- **Auth-based filtering**: restricts tools based on identity/permissions. This controls *who* can see tools, not *which* tools are relevant to a given task.

Neither mechanism addresses the core discovery problem: helping agents find the right tools for what they're trying to do right now.

### The scale challenge

The problem compounds as organizations adopt the MCP Gateway pattern at scale:

- A platform team federates 20+ MCP servers, each exposing 5-15 tools, resulting in 100-300+ tools behind a single gateway.
- Multiple teams and agents share the same gateway, each needing different subsets of tools.
- New tools are added regularly as teams deploy new MCP servers, further growing the list.

Without a discovery mechanism, the gateway becomes harder to use as it becomes more capable. The value of federation (single endpoint, unified auth, centralized policy) is undermined by the cost of presenting everything to everyone.

### What we need

A mechanism that allows clients to discover relevant tools based on their current context, without requiring operators to pre-define every possible combination of tools. The solution should:

- Reduce the number of tools presented to agents to a relevant, manageable subset
- Work within the existing MCP protocol capabilities, but potentially propose enhancements
- Integrate with the gateway's existing filtering layers (MCPVirtualServer, auth)
- Not require changes to upstream MCP servers
- Scale with the number of registered tools without degrading agent performance

## Proposal: Progressive Discovery with Session Scoping

### Overview

The gateway exposes two meta-tools — `discover_tools` and `select_tools` — that allow agents to progressively narrow their tool set without ever ingesting the full catalog of tool schemas. The LLM drives the relevance decisions, using lightweight metadata (categories, tool names, hints) rather than full tool definitions.

This stays within MCP protocol semantics: standard tools, standard `notifications/tools/list_changed`, no protocol extensions.

### CRD Changes

MCPServerRegistration gains two optional fields that provide discovery metadata:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: restaurant-service
  namespace: mcp-test
spec:
  toolPrefix: rs_
  category: "dining reservations"
  hint: "search restaurants, make and cancel reservations, view menus"
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: restaurant-route
```

- **category**: free-text classification of the server's domain. Used by `discover_tools` to present a high-level overview. Defaults to "uncategorised" if not set.
- **hint**: short natural-language description of what the server's tools do. Cheaper than sending full tool schemas — gives the LLM enough to decide relevance.

These fields are optional. Servers without them still appear in `discover_tools` results but with less metadata for the LLM to work with.

### Meta-tools

The gateway exposes two tools that are always present in `tools/list`, regardless of session scoping:

#### discover_tools

Returns lightweight metadata for all registered servers and their tools. No input schemas, no full tool definitions — just enough for the LLM to decide what's relevant.

```json
{
  "name": "discover_tools",
  "description": "Discover available tool categories and servers. Returns lightweight metadata (categories, tool names, hints) to help you identify which tools are relevant to your current task. Use this before select_tools to narrow your working set.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "category": {
        "type": "string",
        "description": "Optional category filter to narrow results"
      }
    }
  }
}
```

Example response:

```json
{
  "servers": [
    {
      "name": "restaurant-service",
      "category": "dining reservations",
      "hint": "search restaurants, make and cancel reservations, view menus",
      "tools": ["rs_search_restaurants", "rs_make_reservation", "rs_cancel_reservation", "rs_get_menu"]
    },
    {
      "name": "calendar-service",
      "category": "scheduling",
      "hint": "manage calendar events and availability",
      "tools": ["cal_list_events", "cal_create_event", "cal_delete_event"]
    }
  ]
}
```

#### select_tools

Scopes the session to a specific set of tools. After this call, the gateway sends `notifications/tools/list_changed` and subsequent `tools/list` requests return only the selected tools (plus the two meta-tools).

```json
{
  "name": "select_tools",
  "description": "Scope your session to a specific set of tools. After calling this, your tools/list will only return the selected tools. Call discover_tools first to identify relevant tools. Call again with a different set to re-scope, or with an empty list to reset to the full tool set.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "tools": {
        "type": "array",
        "items": {"type": "string"},
        "description": "List of tool names to include in your session scope"
      }
    },
    "required": ["tools"]
  }
}
```

### Session Flow

#### With a discovery-aware agent (no extra user prompts)

```
User: "Find me an Italian restaurant near downtown for Saturday, 4 people, and book it"

Turn 1 (discovery):
  Agent calls discover_tools
  → Sees restaurant-service, calendar-service, email-service, payments-service, analytics-service
  Agent calls select_tools(tools: [rs_search_restaurants, rs_make_reservation, rs_get_menu, cal_list_events, cal_create_event])
  → Gateway scopes session, sends notifications/tools/list_changed
  → Agent auto-continues to next turn (aware of the discovery pattern)

Turn 2+ (work):
  Agent has 5 tools with full schemas (instead of 15+)
  Calls rs_search_restaurants, cal_list_events, rs_make_reservation, cal_create_event
  → Task completed
```

A custom agent built with awareness of the browse → select pattern can detect that `select_tools` was called and auto-continue to the next turn without user interaction.

#### With a generic MCP client (one extra user prompt)

```
User: "Find me an Italian restaurant in New York for Saturday"

Turn 1 (discovery):
  Agent calls discover_tools, then select_tools
  Agent responds: "I've identified the restaurant and calendar tools I'll need.
                   They'll be available on my next turn — shall I proceed?"

User: "yes"

Turn 2+ (work):
  Agent has scoped tools, proceeds normally
```

Standard MCP clients don't understand the discovery flow, so the agent must prompt the user to trigger the next turn after `tools/list_changed`.

### Tool count threshold

Whether new sessions see all tools or only meta-tools is controlled by the `--discovery-tool-threshold` flag (default: 10). When the total number of non-meta tools exceeds the threshold, new sessions default to hidden — only `discover_tools` and `select_tools` are visible until the agent calls `select_tools` to scope its session. At or below the threshold, all tools are shown without requiring discovery.

This auto-scales: small gateways with a handful of tools work without friction, while large aggregation points enforce the discovery flow. Setting the threshold to 0 always requires discovery.

Calling `select_tools` with an empty list resets to the full tool set regardless of threshold, giving agents an escape hatch when discovery isn't needed.

### Re-scoping

When the agent's context changes (e.g., the user asks a follow-up about payments), it can call `select_tools` again with a different set. Calling with an empty list resets to the full tool set.

### Interaction with existing filtering

Session scoping operates as an additional filter layer, composing with existing mechanisms:

1. **Auth-based filtering** removes tools the user isn't permitted to access
2. **MCPVirtualServer** restricts to operator-defined tool subsets
3. **Session scoping** (this proposal) further narrows to agent-selected tools

An agent can only select tools that survive the first two filters. `discover_tools` only returns tools the current session is authorized to see. `select_tools` returns an error if any requested tool name does not exist or is not authorized for the current session — no partial scoping is applied.

### Trade-offs

Note TDT (is a Tool Discovery Tool that uses BM25 and keywords to reduce down the tool set)

| | Progressive discovery (this proposal) | Full list + select | Server-side search (tdt) |
|---|---|---|---|
| **First-turn cost** | Low — lightweight metadata only | High — full schemas for all tools | Low — only meta-tools exposed |
| **Relevance quality** | High — LLM decides with names + hints | Highest — LLM sees full schemas | Medium — keyword/BM25/semantic matching |
| **Extra turns** | 1 (browse + select) | 1 (select only) | 1 (search + select) |
| **Server complexity** | Low — metadata indexing only | None | Medium — search index, optional embeddings |
| **Protocol compliance** | Pure MCP | Pure MCP | Pure MCP |

### Why LLM-driven over server-side search

Server-side search (keyword, BM25, semantic) works well for exact matches but struggles with intent. In the restaurant booking example, a keyword search for "restaurant booking" would find restaurant tools but likely miss calendar tools. The LLM understands that booking implies checking availability — it captures the user's intent, not just their words.

The progressive discovery approach lets the LLM make these connections while keeping the token cost low by presenting metadata rather than full schemas.

### Why a gateway enables this

The opportunity to provide tool discovery as a tool-based solution exists precisely because the MCP Gateway sits in front of all upstream MCP servers and has access to every tool definition. A standalone MCP server could offer something similar for its own tools, but the value is far less compelling — the problem only becomes acute when tools from many servers are aggregated. The gateway's position as a federation point means it can build a complete catalog of tool metadata (categories, hints, tool names) across all registered servers and expose that catalog through `discover_tools` without any upstream server needing to change. The gateway is the only component with the full picture, making it the natural place to solve discovery.

### References

- RAG-MCP: Mitigating Prompt Bloat in LLM Tool Selection via Retrieval-Augmented Generation (2025). Demonstrates that tool selection accuracy drops to 13.62% with large tool pools and improves to 43.13% with retrieval-based pre-filtering. Validates the core problem but solves it via a server-side retrieval layer rather than LLM-driven selection. [arxiv.org/abs/2505.03275](https://arxiv.org/abs/2505.03275)
