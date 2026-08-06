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

**Files:** `internal/broker/broker.go`, `internal/broker/user_specific_tools.go`, `internal/broker/protocol_filter.go`, `internal/broker/cache_aggregation.go` (new), `internal/broker/cache_aggregation_test.go` (new), `internal/broker/http_compat_test.go`, `internal/broker/protocol_filter_test.go`, `internal/tests/stateless-server/server.go`, `tests/e2e/dual_protocol_test.go`, `tests/e2e/test_cases.md`

Integrate `ProtocolHandler` into the broker:

- Add `handler2025 ProtocolHandler` and `handler2026 ProtocolHandler` fields to `mcpBrokerImpl`
- Construct handlers in `NewBroker`
- In `startManagers`: rebuild `userSpecificServers` using `ShouldFetchFresh` from the 2026 handler based on upstream cache metadata. 2026 upstreams with `cacheScope:"private"` or `ttlMs:0` join the list alongside CRD-declared `userSpecificList` servers
- In `filteringMiddleware` `tools/list` case: **before** `FilterTools` (which strips `kuadrant/id`), call `AggregateCache` on the 2026 handler with contributing upstreams' metadata and set `result.TTLMs` and `result.CacheScope`
- In `filteringMiddleware` `prompts/list` case: filter prompts by protocol version via `promptsForProtocol` (mirroring `toolsForProtocol`), then apply cache aggregation before `FilterPrompts`
- Renamed `rebuildProtocolToolCache` to `rebuildProtocolCaches`, added prompt partitioning alongside tools
- Added `promptsForProtocol` and `statefulPrompts`/`statelessPrompts` atomic caches
- Refactored `FetchUserSpecificTools` to delegate to the protocol handler selected by client version header
- Tools and prompts without `kuadrant/id` are excluded from all protocol sets with a warn log (not silently defaulted to stateful)
- `AggregateCache` returns `"public"` (not empty string) for empty input — the SDK serializes `cacheScope` without `omitempty`
- Exported `upstream.GatewayServerID` constant for cross-package use
- Added cache metadata middleware to the stateless test server (env-configurable `TTLMs`/`CacheScope`)

**Acceptance criteria:**
- [x] Broker holds two `ProtocolHandler` instances
- [x] `userSpecificServers` includes 2026 upstreams with `cacheScope:"private"` or `ttlMs:0`
- [x] 2026 `tools/list` responses include aggregated `ttlMs` and `cacheScope`
- [x] 2025 `tools/list` responses unchanged (compat handler strips fields)
- [x] `prompts/list` filtered by protocol version — 2026 clients only see prompts from 2026-capable upstreams
- [x] `prompts/list` responses include aggregated fields for 2026 clients
- [x] 2025 `prompts/list` responses unchanged
- [x] Existing `user_specific_tools_test.go`, `protocol_filter_test.go` tests pass
- [x] Unit test: 2026 `prompts/list` excludes 2025-only prompts
- [x] `make lint && make test-unit` passes
- [x] `make test-controller-integration` passes

**Verification:** `make lint && make test-unit && make test-controller-integration`

## Task 6: subscriptions/listen for 2026 upstreams

**Files:** `internal/broker/upstream/mcp.go`, `internal/broker/upstream/subscriptions_listener.go` (new), `internal/broker/upstream/subscriptions_listener_test.go` (new), `internal/broker/protocol_handler_2026.go`

Replace GET SSE `notificationWatcher` with SDK `subscriptions/listen` for 2026 upstreams:

- Create `subscriptionsListener` that uses the SDK client's `SubscriptionsListen` to subscribe to `toolsListChanged` and `promptsListChanged`
- Same backoff/retry semantics as `notificationWatcher`
- Same `notify` callback interface so the manager's event loop is unchanged
- In `MCPServer.Connect`: select notification mechanism based on `UsesStatelessProtocol()`
- Wire `ProtocolHandler2026.StartNotificationWatcher` to use `subscriptionsListener`
- 2025 upstreams continue using `notificationWatcher` unchanged

**Acceptance criteria:**
- [ ] `subscriptionsListener` subscribes to `toolsListChanged` and `promptsListChanged`
- [ ] 2026 upstreams use `subscriptionsListener` instead of `notificationWatcher`
- [ ] 2025 upstreams continue using `notificationWatcher`
- [ ] Manager event loop processes notifications from both mechanisms identically
- [ ] Unit test: subscriptions listener triggers tool refresh
- [ ] `make lint && make test-unit` passes

**Verification:** `make lint && make test-unit`

**CHECKPOINT: full feature functional. Both protocol paths work with correct cache aggregation and notification mechanisms.**

## Task 7: E2E test cases

**Files:** `docs/design/broker-2026-07-28/tasks/test_cases.md`, `tests/e2e/test_cases.md` (update)

Write integration and e2e test cases per `test_cases.md`.

**Acceptance criteria:**
- [ ] Integration test cases documented for aggregation logic, ShouldFetchFresh, and middleware behavior
- [ ] E2E test cases documented for full-stack flows
- [ ] E2E cases added to `tests/e2e/test_cases.md`
- [ ] Cases cover all job stories from the design doc

**Verification:** Review test cases cover goals G1-G5.

## Task 8: Documentation

**Files:** `docs/design/broker-2026-07-28/tasks/documentation.md`

Documentation plan per `documentation.md`.

**Acceptance criteria:**
- [ ] Documentation plan covers user-facing changes
- [ ] Security architecture updated for cache scope trust model

**Verification:** Review documentation plan covers all user-facing behavior.
