## Implementation Plan

### Existing Code

The implementation builds on:

- **CRD types**: `api/v1alpha1/types.go` — `MCPServerRegistrationSpec` has four fields (targetRef, prefix, path, credentialRef). The new `userSpecificList` field follows the same pattern.
- **Config types**: `internal/config/types.go` — `MCPServer` struct carries server config through the config secret to the broker. New `UserSpecificList` field propagated here.
- **Config writer**: `internal/config/config_writer.go` — `SecretReaderWriter` serializes `MCPServer` to the config secret. New field included automatically via struct serialization.
- **Controller**: `internal/controller/mcpserverregistration_controller.go` — builds `config.MCPServer` from the CRD spec. Needs to copy `UserSpecificList` field.
- **MCPManager**: `internal/broker/upstream/manager.go` — `MCPManager.manage()` calls `getTools()` which calls `ListTools()` and then `AddTools()`. For userSpecificList servers, this path must be skipped.
- **Broker FilterTools hook**: `internal/broker/filtered_tools_handler.go` — `FilterTools()` is the `AfterListTools` hook for JWT and virtual server filtering. `FetchUserSpecificTools()` runs before it in the same hook chain.
- **Upstream MCP client**: `internal/broker/upstream/mcp.go` — `MCPServer.ListTools()` calls the upstream. The broker needs a way to call `ListTools` for user-specific servers using the user's headers rather than the service account.

### Task 1: Add `userSpecificList` field to CRD and config types

**Files:**
- `api/v1alpha1/types.go` (modify — add `UserSpecificList` field to `MCPServerRegistrationSpec`)
- `internal/config/types.go` (modify — add `UserSpecificList` field to `MCPServer`)
- `docs/reference/mcpserverregistration.md` (modify — document new field)

**Acceptance criteria:**
- [x] `UserSpecificList UserSpecificListPolicy` enum field on `MCPServerRegistrationSpec` with `json:"userSpecificList,omitempty"` tag
- [x] `+optional` and `+default="Disabled"` kubebuilder markers, `+kubebuilder:validation:Enum=Enabled;Disabled`
- [x] CEL validation: `prefix` required when `userSpecificList` is `Enabled`
- [x] `UserSpecificList bool` field on `config.MCPServer` with json and yaml tags
- [x] `make generate-all` succeeds (deepcopy, CRDs, Helm sync)
- [x] CRD reference doc updated with field description

**Verification:** `make generate-all && make lint`

### Task 2: Controller propagates `userSpecificList` to config secret

**Files:**
- `internal/controller/mcpserverregistration_controller.go` (modify — copy `UserSpecificList` from CRD spec to `config.MCPServer`)

**Acceptance criteria:**
- [x] `MCPServer.UserSpecificList` set from `MCPServerRegistration.Spec.UserSpecificList == UserSpecificListEnabled` during reconciliation (enum → bool conversion)
- [x] Field included in config secret serialization (automatic via struct tags)
- [x] Existing controller tests still pass

**Verification:** `make test-controller-integration`

### Task 3: MCPManager skips tool caching for userSpecificList servers

**Files:**
- `internal/broker/upstream/mcp.go` (modify — add `UserSpecificList` to `GetConfig()` copy)
- `internal/broker/upstream/manager.go` (modify — skip `getTools()` / `AddTools()` when `UserSpecificList=true`)
- `internal/broker/upstream/manager_test.go` (modify — add test for userSpecificList skip behavior)

**Acceptance criteria:**
- [x] `GetConfig()` includes `UserSpecificList` in the returned copy
- [x] When `MCP.GetConfig().UserSpecificList` is true, `manage()` skips `getTools()` call
- [x] Health checks (connect, ping) still run for userSpecificList servers
- [x] Server status still reported via `GetStatus()`
- [x] Tools from userSpecificList servers are NOT added to the broker's `listeningMCPServer`
- [x] Existing manage tests unchanged for non-userSpecificList servers

**Verification:** `make test-unit`

### Task 4: FetchUserSpecificTools with init-on-first-list

**Files:**
- `internal/broker/user_specific_tools.go` (new — `FetchUserSpecificTools()` method with init-on-first-list logic)
- `internal/broker/user_specific_tools_test.go` (new — tests for fetch, init, session caching, merge, error handling)
- `internal/broker/broker.go` (modify — call `FetchUserSpecificTools()` before `FilterTools` in tools/list handling)

**Acceptance criteria:**
- [x] `FetchUserSpecificTools()` is a dedicated method called before `FilterTools` in `AddAfterListTools`
- [x] Identifies userSpecificList servers from registered MCPServers
- [x] For each userSpecificList server, creates a short-lived per-user MCP client with user headers baked into the transport
- [x] On first `tools/list`: create client → init → list → cache upstream session ID (client discarded without Close to avoid HTTP DELETE)
- [x] On subsequent `tools/list`: create client with cached session via `transport.WithSession()` → list (skips init round-trip)
- [x] If cached session is stale (upstream returns error), clear cache and retry with fresh init
- [x] Concurrent fetch from multiple userSpecificList servers using `errgroup`
- [x] Each fetch has the configurable timeout applied independently
- [x] User headers forwarded minus internal headers (`mcp-session-id`, `x-mcp-virtualserver`, `x-mcp-authorized`, `x-mcp-*`)
- [x] Fetched tools have prefix applied (matching existing prefix behavior)
- [x] Fetched tools have `invalidToolPolicy` applied (matching existing validation)
- [x] Fetched tools merged into result before `FilterTools` runs
- [x] Errors from individual userSpecificList servers logged but do not fail the response
- [x] Gateway meta (`kuadrant/id`) added to fetched tools for routing consistency
- [x] When no userSpecificList servers exist, behavior is identical to current
- [x] `FilterTools` remains a pure in-memory filter — no network I/O

**Verification:** `make test-unit`

### Task 5: User-specific test server

**Files:**
- `tests/servers/user-specific-server/main.go` (new)
- `tests/servers/user-specific-server/Dockerfile` (new)
- `internal/tests/user-specific-server/server.go` (new — reusable server logic)
- `internal/tests/user-specific-server/server_test.go` (new)
- `config/test-servers/user-specific-server-deployment.yaml` (new)
- `config/test-servers/user-specific-server-httproute.yaml` (new)
- `config/test-servers/user-specific-server-httproute-ext.yaml` (new)
- `config/test-servers/user-specific-server-mcpserverregistration.yaml` (new)

**Acceptance criteria:**
- [x] MCP server that inspects auth headers and returns different tools per user
- [x] User A gets `list_repos`, `create_issue`; User B gets `run_pipeline`; no auth gets common tools only
- [x] All users get `server_info` and `headers` tools
- [x] Deployed with Service, HTTPRoute, and k8s manifests
- [x] Server builds and runs locally

**Verification:** Manual test with `curl` or MCP client

### Task 6: E2E tests

**Files:**
- `tests/e2e/user_specific_list_test.go` (new)
- `config/test-servers/user-specific-server-mcpserverregistration.yaml` (new — with `userSpecificList: Enabled`)

**Acceptance criteria:**
- [x] Test cases from `tasks/e2e_test_cases.md` implemented
- [x] MCPServerRegistration for user-specific-server has `userSpecificList: Enabled`
- [x] Tests verify tool merge, per-user scoping, prefix application, graceful degradation
- [x] Tests clean up resources before creating them (existing pattern)

**Verification:** E2E test suite passes

### Task 7: Documentation

**Files:**
- See `tasks/documentation.md` for details
- `docs/guides/user-specific-tools.md` (new user guide)
- `docs/reference/mcpserverregistration.md` (already updated in Task 1)

**Acceptance criteria:**
- [ ] User guide explains when and how to use `userSpecificList`
- [ ] Prerequisites for user auth configuration documented
- [ ] Limitations and behavior during upstream failures documented

**Verification:** Review
