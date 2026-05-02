# Feature: MCP Resources Federation

## Summary

Add support for federating MCP Resources (and Resource Templates) through the gateway, following the same pattern used for tools. The broker discovers resources from upstream MCP servers, namespaces them by rewriting the URI scheme, and exposes them to clients via a single `resources/list` and `resources/templates/list`. The router handles `resources/read` requests by recovering the upstream URI and routing to the correct server. Ref: [#788](https://github.com/Kuadrant/mcp-gateway/issues/788), split from [#208](https://github.com/Kuadrant/mcp-gateway/issues/208).

This document deliberately mirrors [prompts-federation.md](prompts-federation.md): the broker, manager, and router each gain a parallel set of resource methods. The only fundamentally new design problem is **URI namespacing**, which §[Design / URI Namespacing](#uri-namespacing) addresses.

## Goals

- Federate resources from multiple upstream MCP servers through a single gateway endpoint
- Federate resource **templates** alongside concrete resources
- Reuse the existing per-server prefix (`toolPrefix`, slated to be renamed to `prefix` in [#842](https://github.com/Kuadrant/mcp-gateway/pull/842)) as a URI-scheme prefix for resources — no new CRD fields
- Document collision semantics: different `toolPrefix` values guarantee distinct federated URIs; duplicate-prefix overlap on the **same federated resource URI** is rejected via **`findResourceConflicts`** and broker **`checkResourceURIConflicts`** (same intent as tool conflicts — see [Conflict detection](#conflict-detection))
- Surface a `status.discoveredResources` counter on `MCPServerRegistration` and an aggregate `totalResources` per server on the broker `/status` endpoint
- Strip gateway-internal `_meta` (`kuadrant/id`) from federated resources before they reach clients

## Non-Goals

This PR delivers federation, not access control. The following are **out of scope** and tracked as follow-ups so reviewers do not need to evaluate them here:

- **`resources/subscribe` and `notifications/resources/updated`** — explicitly listed as unsupported in [notifications.md](notifications.md). Subscriptions cross session boundaries and need a broker-side fan-out design that is independent of federation.
- **`notifications/resources/list_changed` brokering to clients** — the upstream-facing manager already re-discovers on this notification (matching tools); forwarding to gateway-connected clients is deferred so this PR does not have to touch the broker's outbound notification path.
- **JWT-based per-resource authorization** (`capabilities["resources"]` in the `x-mcp-authorized` header) — the [generalized authorization JWT shape](prompts-federation.md#generalized-authorization-header) already reserves `"resources"` as a top-level key. List hooks strip internal `_meta` today; wiring JWT claims into an allow/deny path for `resources/read` is left for a follow-up PR.
- **`MCPVirtualServer.spec.resources`** — the virtual-server allow-list will land in the same PR as the JWT filter, since both share the `applyVirtualServerFilter` pattern.
- **Resource validation** — resources have no JSON schema (unlike tools), so `invalidToolPolicy` does not apply. We rely on the upstream server to honor its declared resources.

## Design

### URI Namespacing

The hard problem in resource federation is identifier collision. Two backends can both expose a resource with URI `file:///config.json` or `db://users`. Tools solve this with a flat per-server name prefix because tool names are opaque strings. Resource URIs are not opaque — clients dereference them via `resources/read` and they may also be RFC 6570 templates. The federation strategy must therefore be **lossless and reversible**: the gateway must be able to look at any URI a client passes back to it and recover (a) which backend owns it, and (b) the original upstream URI.

**Adopted strategy: scheme-plus prefix.** When a resource is federated, the gateway rewrites its scheme from `<scheme>` to `<prefix>+<scheme>`. The plus character is legal in URI schemes per [RFC 3986 §3.1](https://datatracker.ietf.org/doc/html/rfc3986#section-3.1):

> `scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )`

Examples (assume `toolPrefix: weather_`):

| Upstream URI | Federated URI |
|---|---|
| `file:///forecast.json` | `weather_+file:///forecast.json` |
| `embedded:info` *(opaque)* | `weather_+embedded:info` |
| `db://users/{id}` *(template)* | `weather_+db://users/{id}` |
| `https://api.weather.test/v1/zones` | `weather_+https://api.weather.test/v1/zones` |

When the prefix is empty (`toolPrefix: ""`) the URI is forwarded unchanged; this matches the tool federation behavior. Round-tripping is `prefixed → strip leading "<prefix>+" from scheme → upstream URI`.

#### Why not the alternatives

- **Path prefix** (`file:///weather/forecast.json` instead of `file:///forecast.json`): only works for hierarchical URIs that have an authority/path. Breaks opaque URIs (`embedded:info` has no path to prefix), custom schemes, and URI templates that target absolute paths. Also asymmetrically conflicts with backends that already use a top-level path component.
- **Query parameter** (`file:///forecast.json?__gw=weather`): hides the namespace in metadata clients are not required to preserve. Two resources from different servers would render as the same URI in any UI that drops query strings. Easy to misuse.
- **Custom gateway scheme wrapping** (`mcp+gw://weather/file:///forecast.json`): clients must learn a gateway-specific scheme. Resource templates become double-templated. Round-trips through any tool that does generic URI parsing (e.g. logging, observability) corrupt the inner URI.

The scheme-plus strategy is the only option that is RFC-conformant, scheme-agnostic, template-friendly, and bijective. It also keeps the federation transparent: a client that knows nothing about the gateway can paste a federated URI back into `resources/read` and the gateway routes it correctly.

#### Conflict detection

Two backends with **different prefixes** cannot collide on federated URIs (the rewritten scheme differs).

For **duplicate-prefix** registrations exposing the same federated URI as resources already served by another upstream, **`findResourceConflicts`** consults the broker: each manager registers candidates against **`checkResourceURIConflicts`**, which scans sibling managers’ `servedResourcesMap` keys before `AddResources` runs — analogous spirit to **`findToolConflicts`** / `ListTools()` on tools.

#### Resource templates on the shared gateway server

Concrete resources use `AddResources` / `DeleteResources`, so removals propagate. **Templates**: each upstream manager stores its federated `ServerResourceTemplate` slice locally; after every refresh or teardown it notifies the broker’s **`reconcileGatewayResourceTemplates`**, which merges all upstream snapshots and calls **`SetResourceTemplates(...)`** on the listening `server.MCPServer`. That replaces the entire gateway template registry every reconcile so upstream removals disappear exactly like concrete resources (see mcp-go `SetResourceTemplates`).

### Architecture Changes

No new components. The existing broker, manager, and router are extended.

```text
resources/list flow:

  Client ──► Envoy ──► ext_proc (router) ──► HandleNoneToolCall()
                                                    │
                                              sets headers:
                                              x-mcp-servername=mcpBroker
                                                    │
                                              Envoy routes to broker
                                                    │
                                              Broker's mcp-go server
                                              handles resources/list
                                                    │
                                              AddAfterListResources hook
                                              strips kuadrant/id meta
                                                    │
                                              returns federated resources
                                              to client


resources/read flow:

  Client ──► Envoy ──► ext_proc (router) ──► HandleResourceRead()
                                                    │
                                              1. Extract URI
                                              2. GetServerInfoByResourceURI()
                                              3. Strip "<prefix>+" from scheme
                                              4. Set routing headers
                                              5. Init backend session (reused)
                                                    │
                                              Envoy routes to upstream
                                                    │
                                              backend returns
                                              ResourceContents to client
```

`resources/list` and `resources/templates/list` follow the same path as `tools/list` — they pass through the router to the broker's listening MCP server, which aggregates resources from all managers and applies the meta-stripping hook.

`resources/read` follows the same path as `tools/call` — the router identifies the upstream by the scheme prefix, rewrites the URI, sets routing headers, and forwards to the correct upstream.

### API Changes

**None.** No new CRD fields, no breaking changes. The existing `MCPServerRegistration.spec.toolPrefix` (or `.spec.prefix` after [#842](https://github.com/Kuadrant/mcp-gateway/pull/842)) is reused as the URI scheme prefix. Documenting this dual-purpose semantics in the field godoc is part of this change.

`MCPServerRegistration.status` gains a `discoveredResources` integer counter mirroring `discoveredTools`. This is additive; existing kubectl printcolumns are untouched.

The broker `/status` JSON adds a `totalResources` field per `ServerValidationStatus` entry.

### Component Changes

The implementation follows the same pattern as tools throughout. Each component that handles tools gets a parallel set of resource logic.

#### Upstream Client (`internal/broker/upstream/mcp.go`)

Add `ListResources()`, `ListResourceTemplates()`, `ReadResource()`, and `SupportsResourcesListChanged()` to the `MCP` interface. These wrap the mcp-go client methods and check `up.init.Capabilities.Resources.ListChanged` for the notification capability.

#### MCPManager (`internal/broker/upstream/manager.go`)

Add a `ResourcesAdderDeleter` interface mirroring tools resource mutation (`AddResources`, `DeleteResources`, `AddResourceTemplates`, `SetResourceTemplates`). The mcp-go `server.MCPServer` satisfies it.

> **Note**: mcp-go does **not** expose a public `ListResources()` on the server for reads (matching the prompts gap noted in [prompts-federation.md](prompts-federation.md)). The manager maintains its own `resourcesMap` and `servedResourcesMap`; **`findResourceConflicts`** delegates cross-manager checks to the broker callback described above.

The manager gets resource-parallel discovery (`getResources`, `getResourceTemplates`), URI prefixing (`resourceToServerResource`, `templateToServerTemplate`, `prefixedURI`), diffing (`diffResources`), **`findResourceConflicts`**, cleanup (`removeAllResources`), and **`scheduleTemplateReconcile`** so templates stay aligned via **`SetResourceTemplates`**.

The `manage()` loop is extended to discover resources after tools, and `registerCallbacks()` adds a handler for `notifications/resources/list_changed` (re-discovery only; not yet forwarded to clients — see Non-Goals). Status reporting adds `TotalResources`.

The manager constructor receives the listening server as both `ToolsAdderDeleter` and `ResourcesAdderDeleter`.

#### Broker (`internal/broker/broker.go`)

Implements **`reconcileGatewayResourceTemplates`** (merge all managers → **`listeningMCPServer.SetResourceTemplates`**) and **`checkResourceURIConflicts`** wired into each `NewUpstreamMCPManager`.

Enable `server.WithResourceCapabilities(false, false)` on the listening MCP server (subscribe and listChanged set to `false` until those follow-ups land). Register `AddAfterListResources` and `AddAfterListResourceTemplates` hooks that strip `kuadrant/id` from each resource's `_meta` before returning to clients. Add `GetServerInfoByResourceURI(uri string)` to the `MCPBroker` interface — same pattern as `GetServerInfo()` for tools, searching managers by federated URI.

#### Router (`internal/mcp-router/`)

`resources/list` and `resources/templates/list` need no router changes — they fall through to `HandleNoneToolCall()` and the broker handles them via mcp-go, same as `tools/list`.

`resources/read` gets a new `HandleResourceRead()` handler following the same pattern as `HandleToolCall()`: extract URI, look up upstream by scheme prefix, strip prefix, manage backend session, set routing headers, forward via Envoy. A `ResourceURI()` accessor is added to `MCPRequest` mirroring `ToolName()`. A `WithMCPResourceURI()` builder method is added to `HeadersBuilder` so the upstream sees the unprefixed URI in `x-mcp-resource-uri` for observability/policy.

The dispatcher `RouteMCPRequest` switch grows one case for `resources/read`.

#### Config and CRD Types

- `internal/config/types.go`: no field changes — the existing `ToolPrefix` is reused. Doc comment is updated to describe its dual role.
- `api/v1alpha1/types.go`: no field changes to spec. Add `DiscoveredResources int32` to `MCPServerRegistrationStatus`. Update the `ToolPrefix` godoc to clarify it also prefixes resource URI schemes.

### Backwards Compatibility

Fully additive. Pre-existing `MCPServerRegistration` resources whose backends do not advertise resources continue to behave exactly as today. Resources that do exist on backends become available as federated resources transparently, prefixed by the same string already used for tool prefixing.

There is no client-facing API change beyond `resources/list`/`resources/read` returning data from federated backends instead of an empty list. Clients that don't speak resources are unaffected.

### Security Considerations

- **No new privileges.** The federation reuses the existing broker-to-upstream credential channel (`credentialRef`) and the existing client-to-gateway authentication flow. No new auth surfaces.
- **Internal metadata isolation.** The `kuadrant/id` metadata added to each resource during federation is stripped by the `AddAfterListResources` and `AddAfterListResourceTemplates` hooks before returning to clients, same as tools.
- **URI rewriting is bijective.** A client cannot smuggle a request to a different backend by crafting a URI: the scheme prefix unambiguously identifies the owning server, and the stripped tail is what the backend originally advertised. Backend cannot receive a URI it did not produce.
- **Reading without authorization is the next PR.** Until the JWT filter for resources lands (see Non-Goals), all federated resources are visible to any client that can list them. Operators should treat this PR as discovery + routing only, and gate it behind their existing AuthPolicy on the `resources/list` and `resources/read` paths if they need access control today.

## Testing Strategy

- **Unit tests**:
    - URI prefix / strip helpers (round-trip across opaque and hierarchical URIs and URI templates)
    - `MCPManager` resource discovery and diff
    - `Broker.GetServerInfoByResourceURI` for hits, misses, and prefix collisions
    - `removeGatewayMetaResources` strips `kuadrant/id` and tolerates nil meta
    - Router `ResourceURI()` extraction, `HandleResourceRead` happy path, prefix strip, missing URI, unknown URI
    - `HeadersBuilder.WithMCPResourceURI`
    - `findResourceConflicts` / broker `checkResourceURIConflicts` duplicate federated URI
    - Template reconcile (`scheduleTemplateReconcile` → `SetResourceTemplates`)
- **E2E** (`tests/e2e/test_cases.md`):
    - List federated resources from two backends with different prefixes; verify both appear with prefixed schemes
    - Read a federated resource end-to-end and verify the upstream sees the unprefixed URI
    - Two backends sharing a prefix that advertise the same concrete federated resource URI; verify the second registration fails resource discovery / surfaces conflict (parity with tool conflicts where applicable)

## References

- [MCP Resources Specification](https://modelcontextprotocol.io/specification/latest/server/resources)
- [RFC 3986 — URI Generic Syntax §3.1 (Scheme)](https://datatracker.ietf.org/doc/html/rfc3986#section-3.1)
- [RFC 6570 — URI Template](https://datatracker.ietf.org/doc/html/rfc6570)
- [mcp-go server.MCPServer API](https://pkg.go.dev/github.com/mark3labs/mcp-go/server)
- [Issue #788 — Add support for MCP Resources federation](https://github.com/Kuadrant/mcp-gateway/issues/788)
- [Issue #208 — Investigate support for Resources and Prompts](https://github.com/Kuadrant/mcp-gateway/issues/208)
- [Prompts Federation design](prompts-federation.md)
- [Notifications design](notifications.md)
