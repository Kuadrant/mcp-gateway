# Broker 2026-07-28 Implementation Plan

## Existing Code Analysis

**Package location:** `internal/broker/` (package `broker`), `internal/broker/upstream/` (package `upstream`)

**SDK:** `github.com/modelcontextprotocol/go-sdk` — needs bump from `v1.7.0-pre.3` to `v1.7.0` stable

**What exists and gets reused:**

- `FetchUserSpecificTools` — already branches on protocol version (stateful vs stateless fetch). Becomes the body of `ProtocolHandler.FetchUserSpecificTools`
- `perRequestServers` — precomputed in `OnConfigChange` from `UserSpecificList` CRD field. Extended to include `cacheScope:"private"` and `ttlMs:0` servers
- `filteringMiddleware` — `tools/list` middleware that calls `toolsForProtocol`, `FetchUserSpecificTools`, `FilterTools`. Extended to call `AggregateCache`
- `protocolRouter` in `http_compat.go` — dispatches 2026 to stateless handler, 2025 to compat handler. Unchanged
- `compatHandler.rewriteToolsList` / `rewritePromptsList` — strips `ttlMs`/`cacheScope` from 2025 responses. Unchanged (verification only)
- `notificationWatcher` — GET SSE stream watcher for upstream notifications. Stays for 2025 upstreams
- `MCPServer.UsesStatelessProtocol()` — protocol version check. Used for notification mechanism selection
- `serverVersions` sync.Map — tracks protocol versions per upstream. Used by `ShouldFetchFresh` for version-aware decisions

**Notification flow:**
- `MCPServer.startNotificationWatcher` creates a `notificationWatcher` that opens GET SSE to upstream
- On `tools/list_changed` or `prompts/list_changed`, it calls `up.notify(method)` which triggers the manager's event loop
- Manager re-lists tools/prompts and updates the gateway server via `AddTools`/`DeleteTools`
- `gatewayServer.TriggerToolsListChanged` sends notifications to subscribed clients

## Dependency Graph

```text
Task 1 (SDK bump to v1.7.0)
  ↓
Task 2 (cacheMetadata + upstream storage)
  ↓
Task 3 (ProtocolHandler interface + 2025 impl)  ← CHECKPOINT: existing behavior preserved
  ↓
Task 4 (ProtocolHandler2026 impl)
  ↓
Task 5 (Wire handlers into broker)
  ↓
Task 6 (subscriptions/listen for 2026)          ← CHECKPOINT: full feature functional
  ↓
Task 7 (E2E test cases)
  ↓
Task 8 (Documentation)
```

Tasks 1-3 are plumbing with no new behavior — existing tests validate correctness throughout. Task 4 adds new aggregation and fetch logic. Task 5 wires it. Task 6 replaces the notification mechanism for 2026. Tasks 7-8 are test and doc artifacts.

## Task 1: SDK bump to v1.7.0 ✅

**Files:** `go.mod`, `go.sum`

Bump `github.com/modelcontextprotocol/go-sdk` from `v1.7.0-pre.3` to `v1.7.0` stable. This provides `CacheableResult` types with `TTLMs` and `CacheScope` fields, `SubscriptionsListenParams`, and stable `server/discover`.

**Acceptance criteria:**
- [x] `go.mod` references `v1.7.0`
- [x] `CacheableResult` type accessible from the SDK
- [x] `make lint` passes (pre-existing issues only)
- [x] `make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 2: Upstream cache metadata storage

**Files:** `internal/broker/upstream/mcp.go`, `internal/broker/upstream/manager.go`, `internal/broker/upstream/mcp_test.go`

Add `cacheMetadata` to the upstream `MCPServer` and populate it from `ListTools`/`ListPrompts` responses.

```go
type cacheMetadata struct {
    TTLMs            int
    CacheScope       string // "public" or "private"
    UserSpecificList bool   // from CRD, carried through for scope aggregation
}
```

- Add `toolsCacheMeta` and `promptsCacheMeta` fields to `MCPServer`, guarded by `clientMu`
- After `ListTools`, store metadata in `toolsCacheMeta`; after `ListPrompts`, store in `promptsCacheMeta`
- Set `UserSpecificList` from the CRD config when constructing the upstream, so `AggregateCache` has all scope signals in one struct
- Defaults: `TTLMs: 0`, `CacheScope: ""`, `UserSpecificList: false` (CacheScope populated to `"public"` after first upstream list call via SDK defaults)
- Add `ToolsCacheMetadata()` and `PromptsCacheMetadata()` to the `MCP` and `ActiveMCPServer` interfaces

**Acceptance criteria:**
- [x] `cacheMetadata` struct defined
- [x] `MCPServer` stores metadata per result type (`toolsCacheMeta`, `promptsCacheMeta`)
- [x] `MCP` and `ActiveMCPServer` interfaces expose `ToolsCacheMetadata()` and `PromptsCacheMetadata()`
- [x] Defaults are `TTLMs:0`, `CacheScope:""` before first list (populated to `"public"` after first upstream list call)
- [x] Unit test: tools and prompts metadata populated independently from their list results
- [x] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 3: ProtocolHandler interface and ProtocolHandler2025

**Files:** `internal/broker/protocol_handler.go` (new), `internal/broker/protocol_handler_2025.go` (new), `internal/broker/protocol_handler_2025_test.go` (new)

Define the `ProtocolHandler` interface:

```go
type ProtocolHandler interface {
    FetchUserSpecificTools(ctx context.Context, servers []perRequestServer, headers http.Header, result *mcp.ListToolsResult)
    ShouldFetchFresh(srv perRequestServer, meta *cacheMetadata) bool
    AggregateCache(contributing []cacheMetadata) (ttlMs int, cacheScope string)
    StartNotificationWatcher(ctx context.Context, server *upstream.MCPServer)
}
```

`ProtocolHandler2025` wraps existing behavior:
- `FetchUserSpecificTools`: stateful fetch via session pool (moves logic from `broker.FetchUserSpecificTools` 2025 branch)
- `ShouldFetchFresh`: returns `srv.UserSpecificList` (CRD field only)
- `AggregateCache`: returns `(0, "")` — compat handler strips these fields
- `StartNotificationWatcher`: calls `MCPServer.startNotificationWatcher` (existing GET SSE)

**Key constraint:** existing unit tests for `FetchUserSpecificTools` and `filteringMiddleware` must pass with minimal changes.

**Acceptance criteria:**
- [x] `ProtocolHandler` interface defined in `protocol_handler.go`
- [x] `ProtocolHandler2025` implements all 4 methods
- [x] `ShouldFetchFresh` uses `UserSpecificList` CRD field only
- [x] `AggregateCache` returns zero values (no aggregation for 2025)
- [x] Existing `user_specific_tools_test.go` tests pass
- [x] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

**CHECKPOINT: all existing tests pass with the new interface extracted. No new behavior yet.**

## Task 4: ProtocolHandler2026

**Files:** `internal/broker/protocol_handler_2026.go` (new), `internal/broker/protocol_handler_2026_test.go` (new)

Implement the 2026 protocol handler:

- `FetchUserSpecificTools`: stateless connect-list-close (moves logic from `broker.FetchUserSpecificTools` 2026 branch)
- `ShouldFetchFresh`: returns `true` when `meta.CacheScope == "private"` or `meta.TTLMs == 0`
- `AggregateCache`: `min(non-zero TTLMs)` across contributing upstreams; `"private"` if any upstream is private, has `userSpecificList`, or has `TTLMs == 0`; `"public"` otherwise
- `StartNotificationWatcher`: placeholder (wired in Task 6)

**Acceptance criteria:**
- [x] `ProtocolHandler2026` implements all 4 methods
- [x] `ShouldFetchFresh` triggers on `cacheScope:"private"` or `ttlMs:0`
- [x] `AggregateCache` computes correct `min(non-zero ttlMs)`
- [x] `AggregateCache` returns `"private"` when any upstream is private or has `userSpecificList`
- [x] `AggregateCache` returns `(0, "private")` when any TTLMs is 0 — zero TTL forces uncacheable/private
- [x] Unit tests cover: all-public, mixed, all-private, ttlMs-zero, single server, empty input
- [x] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

## Task 5: Wire protocol handlers into broker ✅

**Files:** `internal/broker/broker.go`, `internal/broker/user_specific_tools.go`, `internal/broker/protocol_filter.go`, `internal/broker/cache_aggregation.go` (new), `internal/broker/cache_aggregation_test.go` (new), `internal/broker/discovery.go`, `internal/broker/gateway_server.go`, `internal/broker/session_resurrection.go`, `internal/broker/http_compat_test.go`, `internal/broker/protocol_filter_test.go`, `internal/broker/upstream/manager.go`, `internal/tests/stateless-server/server.go`, `tests/e2e/dual_protocol_test.go`, `tests/e2e/test_cases.md`

Integrate `ProtocolHandler` into the broker:

- Add `handler2025 ProtocolHandler` and `handler2026 ProtocolHandler` fields to `mcpBrokerImpl`
- Construct handlers in `NewBroker`
- CRD-declared `userSpecificList` servers precomputed in `startManagers`; 2026 cache-metadata-driven servers (`cacheScope:"private"` or `ttlMs:0`) precomputed in `rebuildProtocolCaches` as `freshFetchServers` on `protocolCacheEntry` — avoids per-request iteration and races with connecting managers
- `FetchUserSpecificTools` reads `freshFetchServers` from the stateless tools cache for 2026 clients
- In `filteringMiddleware` `tools/list` case: **before** `FilterTools` (which strips `kuadrant/id`), call `AggregateCache` on the 2026 handler with contributing upstreams' metadata and set `result.TTLMs` and `result.CacheScope`
- In `filteringMiddleware` `prompts/list` case: filter prompts by protocol version via `promptsForProtocol`, then apply cache aggregation before `FilterPrompts`
- Renamed `rebuildProtocolToolCache` to `rebuildProtocolCaches`, added prompt partitioning alongside tools
- `protocolCacheEntry[T]` generic struct holds items, contributing serverIDs (for cache aggregation without per-tool Meta extraction), and `freshFetchServers` (for 2026 per-request fetch without per-request iteration)
- `toolsForProtocol` / `promptsForProtocol` take `isStateless bool` instead of full headers
- `isStatelessProtocol(headers)` centralises the version check for future extensibility
- `getVisibleToolNames` simplified to use `toolsForProtocol` instead of `collectAllPrefixedTools` (removed)
- `NotifyMetadataChanged()` added to `ToolsAdderDeleter` interface — manager calls it when cache metadata changes without a tool diff, triggering `rebuildProtocolCaches` to recompute `freshFetchServers`
- Tools and prompts without `kuadrant/id` are excluded from all protocol sets with a warn log
- `AggregateCache` returns `"public"` (not empty string) for empty input — the SDK serializes `cacheScope` without `omitempty`
- Exported `upstream.GatewayServerID` constant for cross-package use
- Added cache metadata middleware to the stateless test server (env-configurable `TTLMs`/`CacheScope`, auto-private when Authorization present)
- Updated `docs/design/overview.md` and `docs/design/security-architecture.md` for dual-protocol and cacheScope security properties

**Acceptance criteria:**
- [x] Broker holds two `ProtocolHandler` instances
- [x] `freshFetchServers` includes 2026 upstreams with `cacheScope:"private"` or `ttlMs:0`
- [x] `freshFetchServers` rebuilt when cache metadata changes without tool changes (`NotifyMetadataChanged`)
- [x] 2026 `tools/list` responses include aggregated `ttlMs` and `cacheScope`
- [x] 2025 `tools/list` responses unchanged (compat handler strips fields)
- [x] `prompts/list` filtered by protocol version — 2026 clients only see prompts from 2026-capable upstreams
- [x] `prompts/list` responses include aggregated fields for 2026 clients
- [x] 2025 `prompts/list` responses unchanged
- [x] Existing `user_specific_tools_test.go`, `protocol_filter_test.go` tests pass
- [x] No data races (`make test-unit` runs with `-race`)
- [x] `make lint && make test-unit` passes
- [x] `make test-controller-integration` passes

**Verification:** `make lint && make test-unit && make test-controller-integration`

## Task 6: subscriptions/listen for 2026 upstreams ✅

**Files:** `internal/broker/upstream/mcp.go`, `internal/broker/protocol_handler.go`, `internal/broker/protocol_handler_2025.go`, `internal/broker/protocol_handler_2026.go`, `internal/tests/stateless-server/server.go`, `internal/tests/stateless-server/server_test.go`, `tests/e2e/dual_protocol_test.go`, `tests/e2e/mcp_client.go`

Enable the SDK's built-in `subscriptions/listen` for 2026 upstreams. No custom `subscriptionsListener` needed — the SDK opens the stream automatically during `Connect` when notification handlers are set on the client.

- Set `ToolListChangedHandler` and `PromptListChangedHandler` on `mcp.ClientOptions` so the SDK opens `subscriptions/listen` for 2026 upstreams
- Skip `startNotificationWatcher` (GET SSE) for 2026 upstreams — they use the SDK's stream instead
- The existing receiving middleware already intercepts `notifications/tools/list_changed` and `notifications/prompts/list_changed` and calls `up.notify(method)`, so the manager event loop works without changes
- 2025 upstreams continue using `notificationWatcher` unchanged
- `DisableStandaloneSSE: true` stays — it prevents the SDK's GET SSE (irrelevant to `subscriptions/listen`)
- `StartNotificationWatcher` removed from `ProtocolHandler` interface — both protocols handle notifications inside `MCPServer.Connect`, not through the handler
- Add `/admin/addTool` and `/admin/deleteTool` endpoints to the stateless test server so e2e tests can trigger `tools/list_changed` over `subscriptions/listen`
- E2E test: add tool via admin endpoint, verify broker picks up the change AND the connected 2026 client receives a `tools/list_changed` notification

**Acceptance criteria:**
- [x] SDK opens `subscriptions/listen` for 2026 upstreams automatically
- [x] 2026 upstreams do not start `notificationWatcher` (GET SSE)
- [x] 2025 upstreams continue using `notificationWatcher`
- [x] Manager event loop processes notifications from both mechanisms identically
- [x] Stateless test server supports `add_tool` MCP tool and `/admin/addTool`, `/admin/deleteTool` HTTP endpoints
- [x] E2E test: 2026 upstream tool change propagates to broker and triggers client notification
- [x] `StartNotificationWatcher` removed from `ProtocolHandler` interface
- [x] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

**CHECKPOINT: full feature functional. Both protocol paths work with correct cache aggregation and notification mechanisms.**

## Task 7: E2E test cases ✅

**Files:** `docs/design/broker-2026-07-28/tasks/test_cases.md`, `tests/e2e/test_cases.md` (update)

Documented and verified test coverage against design goals G1-G6.

**Coverage summary:**
- G1 (ttlMs/cacheScope on list responses): covered by `[Happy,Broker2026]` tools/list and prompts/list e2e tests
- G2 (cacheScope private triggers per-user): unit-tested (`TestFreshFetchServers_*`); e2e gap noted — needs stateless server with `MCP_TOOLS_CACHE_SCOPE=private` without CRD flag
- G3 (subscriptions/listen): covered by `[Broker2026] upstream tool change propagates` e2e test
- G4 (2025 unchanged): covered by `[Happy,Broker2026] 2025 client tools/list has no ttlMs` e2e test
- G5 (interface isolation): architecture, no e2e needed
- G6 (prompts filtered): covered by `[Happy,Broker2026] prompts/list excludes 2025-only prompts` e2e test

**Gaps documented:**
- `[Happy,Broker2026] cacheScope private triggers per-user tool fetch` — unit-tested, e2e not yet implemented
- `[Broker2026,Security] Private scope prevents cross-user tool list leak` — not yet implemented, requires AuthPolicy

**Acceptance criteria:**
- [x] Integration test cases documented for aggregation logic, ShouldFetchFresh, and middleware behavior
- [x] E2E test cases documented for full-stack flows
- [x] E2E cases added to `tests/e2e/test_cases.md`
- [x] Cases cover all job stories from the design doc (gaps documented with status)

**Verification:** Review test cases cover goals G1-G6.

## Task 8: Documentation ✅

**Files:** `docs/design/broker-2026-07-28/tasks/documentation.md`, `docs/design/overview.md`, `docs/design/security-architecture.md`

No new user-facing guide required — the feature is transparent to operators. Documentation updates completed in task 5:

- `docs/design/security-architecture.md`: added `cacheScope` correctness (pessimistic aggregation), `filterUserHeaders` credential stripping, broker→upstream per-user fetch boundary in data crossing table
- `docs/design/overview.md`: added dual-protocol support to broker responsibilities with pointer to design doc
- `docs/design/broker-2026-07-28/tasks/documentation.md`: updated with completion status
- No API reference changes (`userSpecificList` stays for backward compat, no new CRD fields)

**Acceptance criteria:**
- [x] Documentation plan covers user-facing changes (none needed — feature is transparent)
- [x] Security architecture updated for cache scope trust model
- [x] Overview updated for dual-protocol support

**Verification:** Review documentation plan covers all user-facing behavior.
