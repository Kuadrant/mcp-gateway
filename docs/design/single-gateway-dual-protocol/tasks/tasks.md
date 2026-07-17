# Single Gateway Dual Protocol — Implementation Plan

## Existing Code

The implementation builds on:

- **Router dual implementations**: `Router202511` (`internal/routing/router_202511.go`) and `Router202607` (`internal/routing/router_202607.go`) already exist behind the `Router` interface. `ExtProcAdapter` in `server.go` selects by `MCP-Protocol-Version` header.
- **Broker protocol router**: `protocolRouter` in `http_compat.go` dispatches to stateful or stateless `StreamableHTTPHandler` based on `MCP-Protocol-Version` header.
- **Upstream version detection**: `MCPServer.UsesStatelessProtocol()` in `upstream/mcp.go` returns true for `>= 2026-07-28`. `ProtocolInfo()` returns the `InitializeResult` with negotiated version.
- **Tool metadata**: every tool has `kuadrant/id` in its `Meta` map, set to the server's `config.UpstreamMCPID`.
- **UserSpecificList**: `user_specific_tools.go` fetches per-user tools with session-pooled upstream clients. Keyed by `gatewaySessionID/serverName`.
- **Filtering pipeline**: `filteringMiddleware` in `broker.go` calls `FetchUserSpecificTools` then `FilterTools` on every `tools/list`.
- **Discovery tools**: `discover_tools`/`select_tools` in `discovery.go`, gated by `discoveryConfig.enabled`, scoped by session ID.
- **protocolMode**: `ProtocolMode` on `MCPGatewayExtensionSpec` (`api/v1alpha1/`), `--protocol-mode` flag in `cmd/mcp-broker-router/main.go`, `statelessMode` bool on broker, conditional router construction in `cmd/mcp-broker-router/router.go`.

## Tasks

### Task 1: Remove protocolMode from CRD, controller, and startup

Remove the single-protocol gate so both handlers are always active.

**Files:**
- `api/v1alpha1/mcpgatewayextension_types.go` — remove `ProtocolMode` type, constants, spec field
- `internal/controller/broker_router.go` — remove `--protocol-mode` flag logic
- `internal/controller/deployment_test.go` — remove/update `TestBuildBrokerRouterDeployment_ProtocolMode`
- `cmd/mcp-broker-router/main.go` — remove `--protocol-mode` flag
- `cmd/mcp-broker-router/broker.go` — remove `statelessMode` conditional; always create both handlers
- `cmd/mcp-broker-router/router.go` — always construct both routers
- `internal/broker/broker.go` — remove `statelessMode` field, `WithStatelessMode` option
- `internal/broker/http_compat.go` — always build `protocolRouter` with both handlers
- Run `make generate-all` after CRD changes

**Acceptance criteria:**
- [x] `ProtocolMode` field removed from CRD spec
- [x] `--protocol-mode` flag removed from binary
- [x] Broker always creates both stateful and stateless handlers
- [x] Router always constructs both `Router202511` and `Router202607`
- [x] `make generate-all` succeeds
- [x] Existing unit tests updated or removed as needed

**Verification:** `make lint && make test-unit`

### Task 2: Detect and expose supported protocol versions per upstream

A single upstream can support both `2025-11-25` and `2026-07-28`. The upstream manager must detect all supported versions and expose them to the broker.

**Files:**
- `internal/broker/upstream/mcp.go` — add `supportedVersions []string` field to `MCPServer`. After `Connect`, if negotiated version `>= 2026-07-28`, call `server/discover` on the session to get `DiscoverResult.SupportedVersions`. If `server/discover` isn't available (2025-only server), set `["2025-11-25"]`. Add `SupportedVersions() []string` and `SupportsVersion(v string) bool` methods.
- `internal/broker/upstream/manager.go` — add `SupportedVersions(id) []string` method to `MCPManager` interface. Report versions via status callback.
- `internal/broker/broker.go` — maintain `serverVersions map[config.UpstreamMCPID][]string`. Update when upstream managers report status.

**Detection flow:**
1. SDK `Connect` negotiates one version (stored in `init.ProtocolVersion`)
2. If negotiated `>= 2026-07-28`: the upstream manager calls `session.Discover()` or makes a raw `server/discover` RPC to get `SupportedVersions` from `DiscoverResult`
3. If the SDK doesn't expose `Discover` on `ClientSession`: make a raw JSON-RPC call via the session, or use `session.InitializeResult()` and check if the SDK preserved `SupportedVersions` in its mapping
4. If negotiated `2025-11-25`: `supportedVersions = ["2025-11-25"]`

**Acceptance criteria:**
- [ ] Upstream server supporting both versions detected as `["2025-11-25", "2026-07-28"]` (future: dual-version detection via server/discover probe)
- [x] Upstream server supporting only 2025 detected as `["2025-11-25"]`
- [x] Upstream server supporting only 2026 detected as `["2026-07-28"]`
- [x] Broker can look up supported versions by server config ID
- [x] Map is updated when upstream manager connects/reconnects
- [ ] Unit tests cover all three cases

**Verification:** `make lint && make test-unit`

### Task 3: Pre-cache tools by protocol version

Instead of filtering per request, maintain two cached tool sets — stateful and stateless — rebuilt when tools change. The middleware swaps in the correct set based on the client's protocol.

**Files:**
- `internal/broker/broker.go` — add `statefulTools` and `statelessTools` cached sets (e.g. `atomic.Pointer[[]*mcp.Tool]`). Rebuild on tool add/remove events and when upstream manager reports version changes. In `filteringMiddleware`, replace the SDK result's tools with the cached set before `FetchUserSpecificTools` and `FilterTools` run.
- `internal/broker/filtered_tools_handler.go` — add cache rebuild logic: partition tools by server's `supportedVersions`, include broker meta-tools only in stateful set
- `internal/broker/filtered_tools_handler_test.go` — unit tests

**Cache rebuild logic:**
1. For each tool registered on the gateway server, read `kuadrant/id` from metadata
2. Look up server's `supportedVersions`
3. Add to stateful set if server supports `2025-11-25`, stateless set if `2026-07-28`
4. Dual-version server's tools go in both sets
5. Add `discover_tools`/`select_tools` to stateful set only

**Request path:**
1. Read `MCP-Protocol-Version` header
2. Swap the SDK result's tool list with the pre-cached set (stateful or stateless)
3. `FetchUserSpecificTools` and `FilterTools` run as before on the narrowed set

**Acceptance criteria:**
- [x] Stateful client sees tools from servers supporting `2025-11-25` + broker meta-tools
- [x] Stateless client sees tools from servers supporting `2026-07-28`, no broker meta-tools
- [x] Dual-version server's tools appear for both client types
- [x] Cache is rebuilt when tools are added or removed
- [x] Cache is rebuilt when upstream manager reports version change
- [x] No per-tool map lookups on the request path
- [ ] Unit tests cover: mixed backends, dual-version server, all-stateful, all-stateless, cache rebuild on tool change

**Verification:** `make lint && make test-unit`

### Task 4: Protocol-aware UserSpecificList fetching

Make `FetchUserSpecificTools` query only backends matching the client's protocol version, with stateless fetch for 2026 backends.

**Files:**
- `internal/broker/user_specific_tools.go` — add `supportedVersions` field to `userSpecificServer`, filter servers by client protocol, implement stateless fetch path
- `internal/broker/broker.go` — populate `supportedVersions` on `userSpecificServer` during `OnConfigChange`

**Stateless fetch path:**
1. Skip session pool — no `getOrCreateUserSession`
2. Create `mcp.Client` with `MCP-Protocol-Version: 2026-07-28` header
3. Connect with user's auth headers via `DynamicHeaderRoundTripper`
4. Call `ListTools`, collect results
5. Close connection immediately — no pool entry
6. No session ID caching (no sessions)

**Changes to `FetchUserSpecificTools`:**
1. Determine client protocol from headers
2. Filter `userSpecificServers` to only servers supporting the client's version
3. A dual-version server is queried by both client types
4. For stateful clients hitting a server: existing session-pooled path (unchanged)
5. For stateless clients hitting a server: new stateless fetch path
6. Both paths merge tools into the same result

**Acceptance criteria:**
- [x] Stateful client only queries servers supporting `2025-11-25`
- [x] Stateless client only queries servers supporting `2026-07-28`
- [x] Dual-version server queried by both client types (stateful path for 2025 client, stateless path for 2026 client)
- [x] Stateless fetch creates and closes connection per request
- [x] Stateless fetch forwards client auth headers
- [x] Stateless fetch does not write to session pool or session cache
- [ ] Unit tests cover both paths and dual-version server

**Verification:** `make lint && make test-unit`

### Task 5: Enable discover_tools for stateful clients in dual mode

Currently `discover_tools`/`select_tools` are disabled when `statelessMode` is true. In dual mode, they should be enabled but only visible to stateful clients (handled by Task 3's filtering). This task ensures the discovery tools are always registered.

**Files:**
- `cmd/mcp-broker-router/broker.go` — remove the `protocolMode == "stateless"` gate on `WithDiscoveryToolsEnabled`
- `internal/broker/discovery.go` — verify `discover_tools` response only includes servers matching the client's protocol (if protocol filtering is needed here too)

**Acceptance criteria:**
- [x] `discover_tools` and `select_tools` are always registered on the broker
- [x] Stateless clients don't see them in `tools/list` (covered by Task 3)
- [x] Stateful clients can use them as before
- [x] `discover_tools` results filtered by protocol version (only show stateful servers to stateful clients)

**Verification:** `make lint && make test-unit`

### Task 6: E2E tests for dual-protocol gateway

Add e2e tests that verify a single gateway serves both protocol versions correctly.

**Files:**
- `tests/e2e/` — new test file for dual-protocol scenarios
- `tests/servers/` — may need a test server that speaks 2026-07-28 (may already exist from router work)

**Test cases:**
1. Gateway serves tools/list to 2025 client — sees only 2025 backend tools
2. Gateway serves tools/list to 2026 client — sees only 2026 backend tools
3. 2025 client doesn't see 2026-only tools, and vice versa
4. UserSpecificList server with 2025 client — per-user tools returned
5. UserSpecificList server with 2026 client — per-user tools returned (stateless fetch)
6. discover_tools visible to 2025 client, not to 2026 client
7. tools/call works for both protocol versions to their respective backends

**Acceptance criteria:**
- [x] All test cases pass
- [x] Tests added to `tests/e2e/test_cases.md`

**Verification:** `make test-e2e` (or relevant subset)

## Task Order

```
Task 1 (remove protocolMode) ✓ done
Task 2 (expose protocol version) ✓ done (dual-version detection deferred)
Task 3 (filter tools/list) ✓ done (unit tests remaining)
Task 4 (protocol-aware UserSpecificList) ✓ done (unit tests remaining)
Task 5 (enable discover_tools) ✓ done
Task 6 (e2e dual-protocol tests) ✓ done
Task 7 (version-aware server/discover) ✓ done (unit tests + blockDiscoverTransport removal remaining)
Task 8 (protocol-specific routes) ✓ done (unit tests remaining)
Task 9 (e2e discover + routes) ✓ mostly done (test case 1 remaining)
Task 10 (documentation) — NOT DONE
```

### Task 7: Version-aware server/discover response

The broker's `server/discover` response must advertise only protocol versions that have backend support. An SDK client connecting to a 2025-only gateway should negotiate 2025 naturally.

**Files:**
- `internal/broker/broker.go` — compute union of `supportedVersions` across all upstream servers. Update on config change and upstream connect/disconnect.
- `internal/broker/http_compat.go` — wire computed versions into the `mcp.Server`'s capabilities or `server/discover` response. Check how the SDK server exposes `supportedVersions`.
- `internal/broker/protocol_filter.go` — add `computeSupportedVersions()` method

**Acceptance criteria:**
- [x] Gateway with only 2025 backends: `server/discover` returns `supportedVersions: ["2025-11-25"]`
- [x] Gateway with only 2026 backends: `server/discover` returns `supportedVersions: ["2026-07-28"]`
- [x] Gateway with both: returns `["2025-11-25", "2026-07-28"]`
- [x] SDK client connecting to 2025-only gateway negotiates 2025 without client-side workarounds
- [ ] Unit tests cover all three scenarios
- [ ] Existing e2e tests on the shared gateway pass without `blockDiscoverTransport`

**Verification:** `make lint && make test-unit`

### Task 8: Protocol-specific routes

Expose `/mcp/stateful` and `/mcp/stateless` path routes that force a specific protocol version regardless of client negotiation.

**Files:**
- `internal/broker/http_compat.go` — register path handlers for `/mcp/stateful` and `/mcp/stateless` that override protocol dispatch
- `internal/broker/broker.go` — path-based protocol selection in `filteringMiddleware` (read path from request, override protocol version)
- `internal/mcp-router/server.go` or `ext_proc_adapter.go` — if path routing needs ext_proc awareness, read the path and select router accordingly
- Controller — add HTTPRoute rule for `/mcp/` prefix (or verify existing rule covers sub-paths)

**Acceptance criteria:**
- [x] `/mcp/stateful` always returns 2025-compatible tools, regardless of client `MCP-Protocol-Version`
- [x] `/mcp/stateless` always returns 2026-compatible tools, regardless of client `MCP-Protocol-Version`
- [x] `/mcp` continues to negotiate normally via `server/discover`
- [x] tools/call via `/mcp/stateful` routes through Router202511
- [x] tools/call via `/mcp/stateless` routes through Router202607
- [ ] Unit tests for path-based dispatch

**Verification:** `make lint && make test-unit`

### Task 9: E2E tests for version-aware discover and protocol routes

**Files:**
- `tests/e2e/dual_protocol_test.go` — add test cases for version-aware discover and protocol routes
- `tests/e2e/mcp_client.go` — remove `blockDiscoverTransport` once version-aware discover works; replace legacy client with `/mcp/stateful` route

**Test cases:**
1. [ ] Gateway with only 2025 backends: SDK client negotiates 2025 via `server/discover` (not yet tested — shared gateway uses `blockDiscoverTransport` instead of natural negotiation)
2. [x] Gateway with both: SDK client negotiates 2026 via `server/discover`
3. [x] `/mcp/stateful` returns only 2025 tools to a 2026 SDK client
4. [x] `/mcp/stateless` returns only 2026 tools to a 2025 SDK client
5. [x] tools/call via `/mcp/stateful` succeeds for 2025 tools
6. [x] tools/call via `/mcp/stateless` succeeds for 2026 tools

**Remaining:** `blockDiscoverTransport` still used in `mcp_client.go` — should be removable once test case 1 is verified.

**Verification:** `make test-e2e` (or relevant subset)

### Task 10: Documentation updates — NOT DONE

Update guides and API reference.

**Files:**
- `docs/guides/multi-protocol-support.md` — still references `protocolMode` and separate gateway instances, needs full rewrite
- `docs/reference/mcpgatewayextension.md` — still lists `protocolMode` in spec table, needs removal

See [documentation.md](documentation.md) for the full documentation plan.
