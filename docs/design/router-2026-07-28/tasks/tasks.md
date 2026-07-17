# Router 2026-07-28 Implementation Plan

## Existing Code Analysis

**Package location:** `internal/mcp-router/` (package `mcprouter`)

**SDK:** `github.com/modelcontextprotocol/go-sdk v1.7.0-pre.1`

**What exists and gets reused:**
- `MCPRequest` type (JSON-RPC envelope) — stays, used by both protocol paths
- `HeadersBuilder` / `ResponseBuilder` — stays, used by the ext_proc adapter
- `HandleToolCall` / `HandlePromptGet` / `HandleNoneToolCall` / `HandleElicitationResponse` — become the body of `Router202511`
- `routeToUpstream` / `initializeMCPServerSession` — `2025-11-25`-only, move into `Router202511`
- `HandleResponseHeaders` / `sseRewriter` — `2025-11-25`-only response handling
- `HandleRequestHeaders` — stays in adapter (protocol-agnostic: authority rewrite + sub extraction)
- `config.MCPServer` — has Name, Hostname, Prefix, URL — maps directly to `ServerRoute`

**Broker methods the router currently uses:**

| Broker method | RoutingTable equivalent |
|---|---|
| `GetServerInfo(tool) → *config.MCPServer` | `LookupTool(name) → *ServerRoute` |
| `GetServerInfoByPrompt(prompt) → *config.MCPServer` | `LookupPrompt(name) → *ServerRoute` |
| `IsBrokerToolName(name) → bool` | `IsBrokerTool(name) → bool` |
| `ToolAnnotations(serverID, tool) → ToolHints` | `ToolAnnotations(serverID, tool) → *ToolAnnotation` |

## Package Layout

```
internal/routing/           ← NEW: shared types + RoutingTable interface
  table.go                  ← RoutingTable interface, ServerRoute, ToolAnnotation types
  mem_table.go              ← in-memory implementation (broker populates)
  mem_table_test.go

internal/mcp-router/
  router.go                 ← NEW: Router interface, RoutingRequest, RoutingDecision types
  router_202607.go          ← NEW: header-based stateless router
  router_202607_test.go
  router_202511.go          ← NEW: wraps existing stateful routing logic
  router_202511_test.go
  server.go                 ← ExtProcAdapter (refactored Process loop)
  request_handlers.go       ← MCPRequest stays; Handle* methods move to router_202511.go
  response_handlers.go      ← 2025-11-25-only, called from adapter conditionally
  ...existing files unchanged...
```

`internal/routing/` is the shared package. Router imports it for the interface. Broker imports it to populate the in-memory table. No import cycle.

## Dependency Graph

```
Task 1 (RoutingTable types)
  ↓
Task 2 (Broker populates table)
  ↓
Task 3 (Router interface + Router202511)  ← CHECKPOINT: existing behavior preserved
  ↓
Task 4 (ExtProcAdapter refactor)
  ↓
Task 5 (Router202607)
  ↓
Task 6 (Wire protocol branching)          ← CHECKPOINT: both protocols functional
  ↓
Task 7 (E2E test cases)
```

Tasks 1-4 are refactoring with no new behavior — existing tests validate correctness throughout. Task 5 adds new behavior. Task 6 wires it into the adapter. Task 7 defines verification for the full feature.

## Task 1: RoutingTable interface and in-memory implementation

**Files:** `internal/routing/table.go`, `internal/routing/mem_table.go`, `internal/routing/mem_table_test.go`

Create `internal/routing/` with:
- `RoutingTable` interface: `LookupTool(name) → (*ServerRoute, bool)`, `LookupPrompt(name) → (*ServerRoute, bool)`, `LookupPrefix(name) → (*ServerRoute, bool)`, `IsBrokerTool(name) → bool`, `ToolAnnotations(serverID, toolName) → (*ToolAnnotation, bool)`
- `ServerRoute` struct: `Name`, `Host`, `Prefix`, `Path`, `TokenURLElicitation`, `UserSpecificList`
- `ToolAnnotation` struct mirroring `upstream.ToolHints`
- In-memory `Table` struct populated by a `Build(servers, brokerTools, annotations)` method

**Acceptance criteria:**
- [ ] `RoutingTable` interface defined with all 5 methods
- [ ] `ServerRoute` contains fields needed by both router implementations
- [ ] In-memory `Table` passes lookup, prefix-match, and broker-tool tests
- [ ] `make lint` passes
- [ ] `make test-unit` passes

**Verification:** `make lint && go test ./internal/routing/...`

## Task 2: Broker populates RoutingTable

**Files:** `internal/broker/broker.go`, `internal/broker/routing_table.go` (new)

Add a method to the broker that builds a `routing.Table` from its registered servers. The broker already has `GetServerInfo`/`GetServerInfoByPrompt` which iterate server maps — the table builder does the same iteration once and caches the result. Rebuild on config change.

- Add `RoutingTable() routing.RoutingTable` to `MCPBroker` interface
- Broker rebuilds the table when upstreams change (in `OnConfigChange` or when server registration completes)
- The table is an `atomic.Pointer` — swap on rebuild, readers get a consistent snapshot

**Acceptance criteria:**
- [ ] `MCPBroker` interface has `RoutingTable()` method
- [ ] Table is rebuilt on config change
- [ ] Existing broker tests pass
- [ ] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 3: Router interface and Router202511 (wrap existing logic)

**Files:** `internal/mcp-router/router.go` (new), `internal/mcp-router/router_202511.go` (new), `internal/mcp-router/router_202511_test.go` (new), `internal/mcp-router/request_handlers.go` (refactor)

Define:
```go
type Router interface {
    RouteRequest(ctx context.Context, req *RoutingRequest) *RoutingDecision
}
```

With `RoutingRequest` and `RoutingDecision` types per the design doc.

`Router202511` wraps the existing `HandleToolCall`, `HandlePromptGet`, `HandleNoneToolCall`, `HandleElicitationResponse`, `routeToUpstream`, `initializeMCPServerSession` logic. It uses `routing.RoutingTable` instead of `broker.MCPBroker` for lookups. It retains `SessionCache`, `JWTManager`, `singleflight`, `InitForClient`, `HairpinClientPool`, `ElicitationMap`, `TokenElicitationMap`.

The existing methods move from `ExtProcServer` to `Router202511`. `request_handlers.go` keeps `MCPRequest` and its methods but the Handle* functions move.

**This is the largest task.** The key constraint: existing unit tests must pass with minimal changes (updating the receiver type).

**Acceptance criteria:**
- [ ] `Router` interface defined in `router.go`
- [ ] `Router202511` implements `Router` interface
- [ ] `Router202511` uses `routing.RoutingTable` — zero `broker.MCPBroker` usage
- [ ] `ExtProcServer.Broker` field removed
- [ ] All existing request_handlers tests pass (adapted for new receiver)
- [ ] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

**CHECKPOINT: all existing tests pass with the refactored architecture. No new protocol support yet.**

## Task 4: ExtProcAdapter refactor

**Files:** `internal/mcp-router/server.go`, `internal/mcp-router/server_test.go`

Refactor `ExtProcServer.Process()` to be the adapter:
- Hold two `Router` fields: `router202607` and `router202511`
- In header phase: read `MCP-Protocol-Version`, store on request context
- In body phase: select router by protocol version, call `RouteRequest`
- Translate `RoutingDecision` → ext_proc `ProcessingResponse`
- Response phase: branch by protocol version (2025-11-25 does session mapping + elicitation rewriting; 2026-07-28 passes through)

Initially wire only `Router202511` — `Router202607` is nil and all traffic falls through to 202511.

**Acceptance criteria:**
- [ ] `Process` loop uses `Router` interface
- [ ] Protocol version read from `MCP-Protocol-Version` header
- [ ] `Router202511` handles all traffic (202607 not yet wired)
- [ ] Response handling branches by protocol version
- [ ] Existing server_test.go tests pass
- [ ] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 5: Router202607 implementation

**Files:** `internal/mcp-router/router_202607.go` (new), `internal/mcp-router/router_202607_test.go` (new)

Implement the `2026-07-28` router:
- Route by `Mcp-Method` + `Mcp-Name` headers (no body parsing for routing decisions)
- `tools/call`: lookup tool in `RoutingTable`, set `:authority` to server hostname
- `prompts/get`: lookup prompt, set `:authority`
- Prefix stripping: when `ServerRoute.Prefix` is set, strip from `Mcp-Name` header AND body `name` field, set `BodyMutation`
- Header-body validation: after prefix stripping, verify `Mcp-Name` header matches body `name` (return `HeaderMismatch` error on mismatch)
- No session management, no hairpin init, no elicitation ID rewriting
- No `SessionCache`, `JWTManager`, or `singleflight` dependencies
- Unknown methods → route to broker (set `x-mcp-servername: mcpBroker`)
- Host validation: verify `:authority` matches gateway hostname

**Acceptance criteria:**
- [ ] `Router202607` implements `Router` interface
- [ ] Header-only routing works (no body access when no prefix)
- [ ] Prefix stripping rewrites both header and body
- [ ] HeaderMismatch validation rejects mismatched header/body
- [ ] Unknown tools return tool-not-found error
- [ ] Broker meta-tools route to broker
- [ ] Non-tool methods route to broker
- [ ] Unit tests cover: tool lookup, prompt lookup, prefix stripping, header-body mismatch, unknown tool, broker tool, host validation
- [ ] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 6: Wire protocol branching

**Files:** `internal/mcp-router/server.go`, `cmd/mcp-broker-router/main.go`

- Wire `Router202607` into `ExtProcAdapter`
- `MCP-Protocol-Version: 2026-07-28` → use `Router202607`
- Missing or `2025-11-25` → use `Router202511`
- For `2026-07-28` without prefix: skip body phase entirely (set `BodyMutation: nil` in decision, adapter sends header-only response to Envoy)
- For `2026-07-28` response phase: pass-through (no session mapping, no elicitation rewriting)
- Update `cmd/mcp-broker-router/main.go` to construct both routers and pass broker's `RoutingTable()`

**Acceptance criteria:**
- [ ] `2026-07-28` requests routed via `Router202607`
- [ ] `2025-11-25` requests routed via `Router202511`
- [ ] Body phase skipped for `2026-07-28` header-only routing
- [ ] Response phase simplified for `2026-07-28`
- [ ] `make lint && make test-unit` passes
- [ ] `make test-controller-integration` passes

**Verification:** `make lint && make test-unit && make test-controller-integration`

**CHECKPOINT: both protocol paths functional. Ready for e2e testing.**

## Task 7: E2E test cases and documentation

**Files:** `docs/design/router-2026-07-28/tasks/e2e_test_cases.md`, `tests/e2e/test_cases.md` (update)

Write e2e test cases per the design doc's test plan:
- `2026-07-28` tool call without prefix (header-only routing)
- `2026-07-28` tool call with prefix (body rewrite)
- `2026-07-28` header-body mismatch rejection
- `2026-07-28` unknown tool error
- `2026-07-28` prompts/get routing
- `2025-11-25` backward compatibility (existing tests should cover)
- Protocol version detection (missing header → 2025-11-25)

**Acceptance criteria:**
- [ ] E2E test cases documented
- [ ] Test cases added to `tests/e2e/test_cases.md`

**Verification:** Review test cases cover all job stories from the design doc.
