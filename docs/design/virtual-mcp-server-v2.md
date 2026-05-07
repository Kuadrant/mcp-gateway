# MCPVirtualServer v2: Category-Based Routing

## Problem

The current MCPVirtualServer API requires operators to enumerate every tool by name:

```yaml
spec:
  tools:
  - rs_search_restaurants
  - rs_make_reservation
  - cal_list_events
  - cal_create_event
```

This has several problems:

- **Brittle**: when an upstream server adds or removes a tool, every MCPVirtualServer referencing that server must be updated manually.
- **Verbose**: federating an entire server's tools means listing them all, even though the intent is "give me everything from this domain."
- **No routing**: virtual servers are selected via the `x-mcp-virtualserver` header, which requires the client to know the virtual server name upfront. There is no way to expose virtual servers at distinct URL paths.
- **Disconnected from discovery**: the tool discovery proposal introduces `category` and `hint` on MCPServerRegistration, but the virtual server API cannot use these to define tool sets dynamically.

## Proposal

Extend MCPVirtualServerSpec to support category-based tool selection and optional path-based routing.

### API Changes

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: dining-assistant
  namespace: mcp-test
spec:
  hint: "Tools for restaurant discovery and booking"

  # Category-based selection (new) — include all tools from servers
  # matching these categories. Categories come from MCPServerRegistration.spec.category.
  categories:
  - "dining reservations"
  - "scheduling"

  # Explicit tool list (existing) — still supported for fine-grained control.
  # When both categories and tools are specified, the result is the union.
  tools:
  - payments_charge

  # Optional route configuration (new).
  # When set, the controller creates or updates an HTTPRoute rule so this
  # virtual server is reachable at the specified path through the gateway.
  route:
    # Path under which this virtual server is exposed.
    # The broker serves MCP at this path and applies the virtual server's tool filter.
    path: /dining/mcp
```

### CRD Type Changes

```go
type MCPVirtualServerSpec struct {
    Hint string   `json:"hint,omitempty"`
    Tools       []string `json:"tools,omitempty"`

    // categories selects all tools from MCPServerRegistrations whose
    // category field matches one of these values. Case-insensitive match.
    // +optional
    Categories []string `json:"categories,omitempty"`

    // route configures path-based access to this virtual server.
    // +optional
    Route *VirtualServerRoute `json:"route,omitempty"`
}

type VirtualServerRoute struct {
    // path is the URL path where this virtual server is reachable.
    // Must start with '/'. The broker serves MCP at this path.
    // +kubebuilder:validation:Pattern=`^/`
    Path string `json:"path"`
}
```

Validation: at least one of `categories` or `tools` must be specified.

### How Category Resolution Works

Today the controller generates `VirtualServerConfig` with a flat tool list. With categories, the controller resolves category -> tools at reconcile time:

1. Controller lists all MCPServerRegistrations.
2. For each MCPVirtualServer, it collects tools from servers whose `category` matches any entry in `spec.categories`.
3. The resolved tool names (with prefixes) are merged with any explicit `spec.tools`.
4. The merged list is written to the config Secret as before.

The broker does not need to understand categories — it continues to receive a flat tool list per virtual server. Category resolution is a controller concern.

When an MCPServerRegistration's category or tool set changes, the MCPVirtualServer controller re-reconciles and updates the config. This means adding a new server with `category: "dining reservations"` automatically includes its tools in the `dining-assistant` virtual server without any manual update.

### How Path-Based Routing Works

When `spec.route.path` is set:

1. The controller adds the path to the virtual server config written to the Secret.
2. The controller creates or updates an HTTPRoute rule on the gateway's `mcp-gateway-route` that matches the path and routes to the broker.
3. The broker, on receiving a request at the configured path, looks up which virtual server owns that path and applies the corresponding tool filter — the same filter it applies today for the `x-mcp-virtualserver` header.

Both mechanisms (header and path) continue to work. Path routing is additive.

### Config Changes

The `VirtualServerConfig` in the Secret gains an optional path field:

```go
type VirtualServerConfig struct {
    Name  string   `json:"name"  yaml:"name"`
    Tools []string `json:"tools" yaml:"tools"`
    Path  string   `json:"path,omitempty" yaml:"path,omitempty"`
}
```

The broker's `VirtualServer` type gains a corresponding field:

```go
type VirtualServer struct {
    Name  string
    Tools []string
    Path  string
}
```

### Broker Path Matching

The broker today serves MCP on `/mcp`. With path routing, it needs to also serve on virtual server paths. On config change:

1. The broker builds a path -> virtual server index.
2. For incoming requests, the broker checks the request path against the index.
3. If a match is found, the virtual server ID is injected as if the `x-mcp-virtualserver` header were present.
4. The existing `FilterTools` pipeline handles the rest.

This keeps the filtering logic unified — path routing is syntactic sugar over the header mechanism.

### Example: Full Configuration

```yaml
# Upstream servers with categories
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: restaurant-service
spec:
  toolPrefix: rs_
  category: "dining reservations"
  hint: "search restaurants, make and cancel reservations"
  targetRef:
    kind: HTTPRoute
    name: restaurant-route
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: calendar-service
spec:
  toolPrefix: cal_
  category: "scheduling"
  hint: "manage calendar events and availability"
  targetRef:
    kind: HTTPRoute
    name: calendar-route
---
# Virtual server using categories + route
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: dining-assistant
  namespace: mcp-test
spec:
  hint: "Tools for restaurant discovery and booking"
  categories:
  - "dining reservations"
  - "scheduling"
  route:
    path: /dining/mcp
```

Result: `POST https://mcp.example.com/dining/mcp` returns tools from both `restaurant-service` and `calendar-service`, without listing individual tools.

### Interaction with Tool Discovery

#### Virtual servers in `discover_tools` response

Today `discover_tools` returns a flat list of servers. With virtual servers, agents need a way to discover which scoped endpoints are available. The `discover_tools` response gains an optional `virtualServers` field:

```json
{
  "servers": [
    {
      "name": "restaurant-service",
      "category": "dining reservations",
      "hint": "search restaurants, make and cancel reservations",
      "tools": ["rs_search_restaurants", "rs_make_reservation", "rs_get_menu"]
    },
    {
      "name": "calendar-service",
      "category": "scheduling",
      "hint": "manage calendar events and availability",
      "tools": ["cal_list_events", "cal_create_event"]
    }
  ],
  "virtualServers": [
    {
      "name": "mcp-test/dining-assistant",
      "hint": "Tools for restaurant discovery and booking",
      "categories": ["dining reservations", "scheduling"],
      "path": "/dining/mcp",
      "tools": ["rs_search_restaurants", "rs_make_reservation", "rs_get_menu", "cal_list_events", "cal_create_event", "payments_charge"]
    }
  ]
}
```

This lets an agent understand that a pre-curated scope exists. A discovery-aware agent seeing this response could:

1. Decide the `dining-assistant` virtual server already covers its needs.
2. Call `select_tools` with just those tools, or connect directly to `/dining/mcp` if path routing is configured.
3. Skip manual tool selection entirely — the operator already did it.

The response type changes:

```go
type discoverToolsResponse struct {
    Servers        []serverInfo        `json:"servers"`
    VirtualServers []virtualServerInfo `json:"virtualServers,omitempty"`
}

type virtualServerInfo struct {
    Name        string   `json:"name"`
    Hint        string   `json:"hint,omitempty"`
    Categories  []string `json:"categories,omitempty"`
    Path        string   `json:"path,omitempty"`
    Tools       []string `json:"tools"`
}
```

#### Auth filtering on `discover_tools`

**Not yet implemented**: `handleDiscoverTools` currently iterates directly over `broker.mcpServers`, bypassing the `FilterTools` pipeline. A client calling `discover_tools` sees metadata for every upstream server — including tools it is not authorized to access.

The fix: `handleDiscoverTools` must apply the same filtering stages as `FilterTools`:

1. Build the full server/tool list.
2. Apply auth filtering (`x-authorized-tools` JWT) and virtual server filtering (`x-mcp-virtualserver` header).
3. Remove any server or virtual server entry whose tools were all filtered out.

A client cannot discover tools it cannot call.

> **Note**: the handler receives `mcp.CallToolRequest`, not `http.Request`, so HTTP headers need to be threaded through via context or the MCP request's header field.

#### Composition

- Categories provide operator-defined coarse grouping; discovery provides agent-driven fine-grained selection.
- `select_tools` further narrows within the visible tool set.

### Trade-offs

| Approach | Maintenance | Flexibility | Routing |
|---|---|---|---|
| Current (explicit tools) | High — manual tool list updates | Full — any combination of tools | Header only |
| Categories only | Low — auto-resolves from server metadata | Medium — server-level granularity | Header only |
| Categories + tools | Low for bulk, precise where needed | Full — union of both | Header only |
| Categories + tools + route | Low for bulk, precise where needed | Full | Header + path |

### Migration

The existing `tools`-only API continues to work unchanged. Categories and route are additive. No migration required.

### Open Questions

1. **Path conflict detection**: what happens if two virtual servers claim the same path? The controller should reject duplicates via status conditions.
2. **Wildcard categories**: should we support glob patterns like `dining*`? Probably not initially — exact match is simpler and categories are free-text anyway.
3. **Category normalization**: should categories be case-insensitive and whitespace-trimmed at the CRD level (CEL validation) or at resolution time in the controller?
