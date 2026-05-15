# Feature: MCP Prompts Federation

## Summary

Add support for federating MCP Prompts through the gateway, following the same pattern used for tools. The broker discovers prompts from upstream MCP servers, applies prefixing to avoid collisions, and exposes them to clients. The router handles `prompts/get` requests by stripping prefixes and routing to the correct upstream server. Ref: [#787](https://github.com/Kuadrant/mcp-gateway/issues/787), split from [#208](https://github.com/Kuadrant/mcp-gateway/issues/208).

## Goals

- Federate prompts from multiple upstream MCP servers through a single gateway endpoint
- Rename `toolPrefix` to `prefix` on MCPServerRegistration CRD (breaking change) and use it for both tool and prompt prefixing
- Support `prompts/list` and `prompts/get` MCP methods
- Handle `notifications/prompts/list_changed` from upstream servers
- Apply VirtualServer filtering to prompts
- Replace `x-authorized-tools` with a generalized `x-mcp-authorized` header covering tools and prompts
- Apply JWT-based authorization filtering to both tools and prompts via the generalized header

## Non-Goals

- Resource federation — tracked separately in [#788](https://github.com/Kuadrant/mcp-gateway/issues/788)
- `InvalidPromptPolicy` — prompts have no JSON schemas like tools, so there is no FilterOut/RejectServer policy. Basic validation is applied (empty prompt names, empty argument names) and invalid prompts are always filtered out silently

## Design

### Backwards Compatibility

**Breaking change**: The `toolPrefix` field on MCPServerRegistration is renamed to `prefix`. This field has always been a server-level namespace, not tool-specific, and the rename aligns the API with its actual semantics now that it applies to both tools and prompts.

**Migration**: Users must replace `toolPrefix` with `prefix` in their MCPServerRegistration manifests. Since the field has CEL immutability validation, existing resources must be deleted and recreated (not patched in-place). For bulk updates, a `sed` one-liner or `yq` edit on manifest files before `kubectl apply` is sufficient.

**Scope of rename** (57 files affected):
- CRD types: `api/v1alpha1/types.go` — rename field and JSON tag
- Config types: `internal/config/types.go` — rename `ToolPrefix` to `Prefix`
- Manager/broker/router: update all references to `GetPrefix()`, `ToolPrefix`, etc.
- CRD manifests, Helm charts, samples, docs, tests
- Run `make generate-all` to regenerate CRDs and sync Helm

**Breaking change**: The `x-authorized-tools` header is replaced with `x-mcp-authorized`. AuthPolicy configurations that set this header must be updated to use the new header name and JWT claim format. See [Generalized Authorization Header](#generalized-authorization-header).

All other changes (prompt federation, new CRD fields) are additive and non-breaking.

### Architecture Changes

No new components. The existing broker, manager, and router are extended.

```text
prompts/list flow:

  Client ──► Envoy ──► ext_proc (router) ──► HandleNoneToolCall()
                                                    │
                                              sets headers:
                                              mcp-server-name=mcpBroker
                                                    │
                                              Envoy routes to broker
                                                    │
                                              Broker's mcp-go server
                                              handles prompts/list
                                                    │
                                              AddAfterListPrompts hook
                                              applies filtering
                                                    │
                                              returns federated prompts
                                              to client


prompts/get flow:

  Client ──► Envoy ──► ext_proc (router) ──► HandlePromptGet()
                                                    │
                                              1. Extract prompt name
                                              2. GetServerInfoByPrompt()
                                              3. Strip prefix
                                              4. Set routing headers
                                              5. Init backend session
                                                    │
                                              Envoy routes to upstream
                                              MCP server
                                                    │
                                              returns prompt messages
                                              to client
```

`prompts/list` follows the same path as `tools/list` — it passes through the router to the broker's listening MCP server, which aggregates prompts from all managers and applies filtering via hooks.

`prompts/get` follows the same path as `tools/call` — the router identifies the upstream server by the prefixed prompt name, strips the prefix, sets routing headers, and forwards to the correct upstream.

### API Changes

#### MCPVirtualServer Spec

Add optional `prompts` field:

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: my-virtual-server
spec:
  description: "Scoped MCP server"
  tools:
    - weather_forecast
    - weather_alerts
  prompts:                    # new, optional
    - weather_report
    - weather_summary
```

When `prompts` is omitted, all prompts are exposed (same behavior as tools today).

### Component Changes

The implementation follows the same pattern as tools throughout. Each component that handles tools gets a parallel set of prompt logic.

#### Upstream Client (`internal/broker/upstream/mcp.go`)

Add `ListPrompts()` and `SupportsPromptsListChanged()` to the `MCP` interface. These wrap the mcp-go client methods and check the initialize response capabilities.

#### MCPManager (`internal/broker/upstream/manager.go`)

Add a `PromptsAdderDeleter` interface mirroring `ToolsAdderDeleter`. The mcp-go `server.MCPServer` already implements `AddPrompts()`/`DeletePrompts()`, so the broker's listening server satisfies this interface.

> **Note**: As of mcp-go v0.53.0, the server exposes a public `ListPrompts()` returning `map[string]*server.ServerPrompt`. The `PromptsAdderDeleter` interface uses this for conflict detection via `findPromptConflicts()`, following the same pattern as `findToolConflicts()` with `ListTools()`.

The manager gets prompt-parallel versions of the existing tool methods: discovery (`getPrompts`), prefixing (`promptToServerPrompt`), diffing (`diffPrompts`), conflict detection (`findPromptConflicts`), and cleanup (`removeAllPrompts`). These follow the same logic as their tool counterparts.

The `manage()` loop is extended to discover prompts after tools. Notifications are funneled through separate `toolEvents` and `promptEvents` channels (buffer of 1 each) into the single-goroutine event loop, ensuring a tool notification cannot block a prompt notification while preserving per-type coalescing. `registerCallbacks()` handles both `notifications/tools/list_changed` and `notifications/prompts/list_changed`. Status reporting includes `TotalPrompts`.

#### Broker (`internal/broker/broker.go`)

Enable `server.WithPromptCapabilities(true)` on the listening MCP server. Register an `AddAfterListPrompts` hook that calls `FilterPrompts()`. Add `GetServerInfoByPrompt()` to the `MCPBroker` interface — same pattern as `GetServerInfo()` for tools, searching managers by prefixed prompt name.

The manager constructor receives the listening server as both `ToolsAdderDeleter` and `PromptsAdderDeleter`.

#### Authorization Header Generalization (`internal/broker/filtered_tools_handler.go`)

The existing `x-authorized-tools` header and `allowed-tools` JWT claim are replaced with a generalized `x-mcp-authorized` header and `allowed-capabilities` claim. The JWT payload type changes from `map[string][]string` to `map[string]map[string][]string` (capability type → server name → names). See [Generalized Authorization Header](#generalized-authorization-header) for the full format.

The parsing function (`parseAuthorizedToolsJWT` → `parseAuthorizedCapabilitiesJWT`) unmarshals the top-level map once, then each filter handler receives its `map[string][]string` slice via the appropriate key (`capabilities["tools"]`, `capabilities["prompts"]`). The `filterToolsByServerMap` function signature is unchanged.

The `enforceToolFilter` flag is generalized to `enforceCapabilityFilter` — when set, a missing `x-mcp-authorized` header denies all capabilities (tools and prompts). The `--enforce-tool-filter` CLI flag is renamed to `--enforce-capability-filter`.

#### Prompt Filtering (`internal/broker/filtered_prompts_handler.go`)

New file mirroring `filtered_tools_handler.go`. Applies both JWT-based authorization filtering (via `capabilities["prompts"]` from the `x-mcp-authorized` header) and VirtualServer filtering. Strips `kuadrant/id` gateway metadata from prompts before returning to clients.

`filterPromptsByServerMap` follows the same pattern as `filterToolsByServerMap` — receives `map[string][]string` (server name → prompt names), looks up each server's managed prompts, and returns only those in the allow list.

#### Router (`internal/mcp-router/request_handlers.go`)

`prompts/list` needs no router changes — it falls through to `HandleNoneToolCall()` and the broker handles it via mcp-go, same as `tools/list`.

`prompts/get` gets a new `HandlePromptGet()` handler following the same pattern as `HandleToolCall()`: extract prompt name, look up upstream server by prefix, strip prefix, manage backend session, set routing headers, forward via Envoy. A `PromptName()` method is added to `MCPRequest` mirroring `ToolName()`.

#### Config and CRD Types

- `internal/config/types.go`: Rename `ToolPrefix` to `Prefix` on `MCPServer`. Add `Prompts []string` to `VirtualServer`.
- `api/v1alpha1/types.go`: Rename `toolPrefix` to `prefix` on MCPServerRegistration spec. Add `prompts` to MCPVirtualServer spec.

### Security Considerations

- Prompt filtering reuses the existing VirtualServer mechanism. Prompts not listed in a VirtualServer's `prompts` field are not exposed.
- The `kuadrant/id` metadata added to prompts during federation is stripped before returning to clients, same as tools.
- `prompts/get` routing uses the same client authentication flow as `tools/call` — the client provides credentials via AuthPolicy, and the gateway forwards the Authorization header to the upstream server. `credentialRef` is only used for broker-to-upstream connections (discovering tools/prompts), not for client-facing auth.
- **Authorization header generalization**: The `x-authorized-tools` header is replaced with `x-mcp-authorized` as part of this implementation. See [Generalized Authorization Header](#generalized-authorization-header) for format and semantics.
- **Capability isolation**: Tools and prompts are distinct capabilities — authorization for tools on a server does not grant access to prompts on the same server. The `allowed-capabilities` JWT claim encodes them separately.

### Generalized Authorization Header

The current `x-authorized-tools` header carries a JWT with a single `allowed-tools` claim containing a `map[string][]string` (server name → tool names). As prompts and later resources are federated, adding a new header per capability (`x-authorized-prompts`, `x-authorized-resources`) doesn't scale.

Replace `x-authorized-tools` with a single `x-mcp-authorized` header. The JWT claim changes from `allowed-tools` to `allowed-capabilities`, and the value type changes from `map[string][]string` to `map[string]map[string][]string` (capability type → server name → names). This is implemented as part of the prompts federation work, not as a follow-up.

**Current format** (`x-authorized-tools` JWT, `allowed-tools` claim):

```json
{
  "weather": ["get_forecast", "get_temperature"],
  "github": ["list_repos"]
}
```

**Proposed format** (`x-mcp-authorized` JWT, `allowed-capabilities` claim):

```json
{
  "tools": {
    "weather": ["get_forecast", "get_temperature"],
    "github": ["list_repos"]
  },
  "prompts": {
    "weather": ["weather_summary"],
    "github": ["pr_review", "issue_triage"]
  },
  "resources": {
    "github": ["repo://org/repo"]
  }
}
```

**Go type change**: The JWT parsing function signature stays the same per-capability — each filter handler (`filterToolsByServerMap`, `filterPromptsByServerMap`) still receives `map[string][]string`. The change is in the parsing layer, which unmarshals `map[string]map[string][]string` and hands each capability key to the relevant filter.

**Enforcement semantics**: A missing capability key (e.g. no `"prompts"` key in the JWT) means the JWT makes no assertion about that capability — behavior depends on the enforcement flag, same as today. An empty map (`"prompts": {}`) explicitly denies all prompts.

**Migration**: Since `x-authorized-tools` is a trusted internal header set by AuthPolicy (not by clients directly), migration is a coordinated update of the AuthPolicy Rego/Wasm and the broker's parsing code — no client-facing API change. AuthPolicy configurations that currently set `x-authorized-tools` with the `allowed-tools` claim must be updated to set `x-mcp-authorized` with `allowed-capabilities`. The authorization guide (`docs/guides/authorization.md`) is updated accordingly.

**Scope of header rename**:
- `internal/broker/filtered_tools_handler.go`: rename header constant, rename parsing function, update claim key and unmarshal type
- `internal/broker/filtered_tools_handler_test.go`: update all test JWT payloads to use the new claim structure
- `internal/broker/filtered_prompts_handler.go`: new file, reads `capabilities["prompts"]`
- `internal/broker/broker.go`: rename `enforceToolFilter` to `enforceCapabilityFilter`
- `cmd/mcp-broker-router/main.go`: rename CLI flag
- `docs/guides/authorization.md`: update header name and JWT examples
- `config/samples/oauth-token-exchange/tools-list-auth.yaml`: update AuthPolicy sample

#### Keycloak Role Convention

Per-capability authorization is driven by a naming convention on Keycloak client roles. Each MCP server is a Keycloak client. Currently, roles on that client map directly to tool names (e.g. `get_forecast`). To distinguish between capability types, roles are prefixed with the capability type and a colon:

- `tool:get_forecast` — grants access to the `get_forecast` tool
- `tool:get_temperature` — grants access to the `get_temperature` tool
- `prompt:weather_summary` — grants access to the `weather_summary` prompt

The Keycloak JWT `resource_access` claim carries these prefixed roles:

```json
{
  "resource_access": {
    "weather-server": {
      "roles": [
        "tool:get_forecast",
        "tool:get_temperature",
        "prompt:weather_summary"
      ]
    },
    "github-server": {
      "roles": [
        "tool:list_repos",
        "prompt:pr_review",
        "prompt:issue_triage"
      ]
    }
  }
}
```

The AuthPolicy OPA Rego policy splits roles by prefix to build the `allowed-capabilities` map:

```rego
capabilities = {
  "tools": { server: tools |
    server := object.keys(input.auth.identity.resource_access)[_]
    tools := [substring(r, count("tool:"), -1) |
      r := input.auth.identity.resource_access[server].roles[_]
      startswith(r, "tool:")
    ]
  },
  "prompts": { server: prompts |
    server := object.keys(input.auth.identity.resource_access)[_]
    prompts := [substring(r, count("prompt:"), -1) |
      r := input.auth.identity.resource_access[server].roles[_]
      startswith(r, "prompt:")
    ]
  }
}
```

Authorino then packages this into the `allowed-capabilities` claim of the `x-mcp-authorized` wristband JWT.

**Migration for existing deployments**: Existing Keycloak roles (e.g. `get_forecast`) must be renamed to include the `tool:` prefix (e.g. `tool:get_forecast`). This is a one-time change in the Keycloak admin console or via the Keycloak API. Users without prompt roles are unaffected — the `prompts` key will be an empty map, and behavior depends on the enforcement flag.

- No new RBAC or privilege escalation concerns — prompts follow the same access path as tools.

## Testing Strategy

- **Unit tests**: MCPManager prompt discovery, diffing, conflict detection, prefix handling. Broker `FilterPrompts` hook. Router `PromptName()` extraction and `HandlePromptGet()` routing logic. `parseAuthorizedCapabilitiesJWT` parsing with the new `allowed-capabilities` claim structure. `filterPromptsByServerMap` filtering. Combined `x-mcp-authorized` + VirtualServer filtering for both tools and prompts. Mirror existing tool test patterns in `manager_test.go`, `broker_test.go`, `request_handlers_test.go`.
- **Integration tests**: VirtualServer filtering applies to prompts.
- **E2E tests**: Register servers with prompts, verify `prompts/list` returns prefixed names, call `prompts/get` and verify response, unregister and verify cleanup. Test with multiple servers to verify cross-server prefix isolation. Test virtual server prompt filtering.

## Implementation Notes

Behavioral decisions made during implementation:

- **Independent discovery**: Tool and prompt discovery run independently in `manage()`. Tool failures (getTools error, RejectServer, conflicts) do not block prompt discovery. Both errors are joined via `errors.Join` in status reporting.
- **Transient failure handling**: A `getPrompts` or `getTools` listing failure preserves existing capabilities. Capabilities are only removed on connect/ping failure (server unreachable) or graceful shutdown.
- **Conflict handling**: Both tool and prompt conflicts preserve existing capabilities and set error status, rather than removing all capabilities.
- **Notification granularity**: `notifications/tools/list_changed` and `notifications/prompts/list_changed` are delivered via separate channels (`toolEvents`, `promptEvents`) with buffer of 1 each, preventing cross-type interference. A tool notification only re-fetches tools, not prompts, and vice versa.
- **Fetch optimization**: `shouldFetchPrompts` mirrors `shouldFetchTools`. When a server supports `prompts/list_changed`, prompts are not re-fetched on timer ticks if already discovered.
- **Shared routing**: `HandleToolCall` and `HandlePromptGet` share a `routeToUpstream` method for session lookup, lazy initialization, body marshaling, path resolution, and response building.
- **Hairpin headers**: During lazy session initialization, `x-mcp-toolname` and `x-mcp-promptname` are only set when non-empty, preventing AuthPolicy rules from firing on irrelevant capability types.

## References

- [MCP Prompts Specification](https://modelcontextprotocol.io/specification/latest/server/prompts)
- [mcp-go server.MCPServer API](https://pkg.go.dev/github.com/mark3labs/mcp-go/server)
- [Issue #787 — Add support for MCP Prompts federation](https://github.com/Kuadrant/mcp-gateway/issues/787)
- [Issue #208 — Investigate support for Resources and Prompts](https://github.com/Kuadrant/mcp-gateway/issues/208)
- [Notifications design doc](notifications.md)

## Change Log

- **2026-05-06**: Implementation complete. Used `gatewayServer.ListPrompts()` for conflict detection (available since mcp-go v0.50.0) instead of the aggregation workaround originally described.
- **2026-05-12**: Added implementation notes documenting behavioral decisions: independent tool/prompt discovery, transient failure handling, conflict preservation, notification granularity, fetch optimization, shared routing, and hairpin header fixes.
- **2026-05-14**: Upgraded mcp-go to v0.53.0 (`ListPrompts()` now returns pointers). Adapted to manager channel-based event loop refactor (`ActiveMCPServer` interface). Split single `events` channel into separate `toolEvents`/`promptEvents` channels to prevent cross-type notification drops.
