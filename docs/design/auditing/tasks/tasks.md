## Implementation Plan

### Existing Code

The implementation builds on:

- **Router headers**: `internal/mcp-router/headers.go` — `HeadersBuilder` already sets `x-mcp-method`, `x-mcp-toolname`, `x-mcp-servername`, and `mcp-session-id` via `ProcessingResponse` header mutations.
- **Router request handling**: `internal/mcp-router/request_handlers.go` — MCP request body is already parsed (method, tool name, server name extracted). New audit headers plug into the same `HeadersBuilder` chain.
- **Operator EnvoyFilter**: `internal/controller/mcpgatewayextension_controller.go` — `buildEnvoyFilter()` (line ~719) builds an EnvoyFilter with a single `HTTP_FILTER` ConfigPatch for ext_proc. The access log patch adds a second `NETWORK_FILTER` ConfigPatch to the same EnvoyFilter.
- **Operator deployment**: `internal/controller/broker_router.go` — `buildBrokerRouterDeployment()` builds the router deployment with env vars. `managedEnvVarNames` controls which env vars the operator owns. `mergeEnvVars()` preserves user-added env vars during reconciliation.
- **CRD types**: `api/v1alpha1/mcpgatewayextension_types.go` — existing patterns for optional nested config: `SessionStore`, `TrustedHeadersKey` (pointer to struct, conditional env var injection).
- **OTel tracing**: `internal/mcp-router/tracing.go` — span attributes include `mcp.method.name`, `mcp.session.id`, `gen_ai.tool.name`. Audit headers complement these with access-log-native fields.

### Task 1: Baggage parsing and identity extraction

**Files:**
- `internal/mcp-router/baggage.go` (new)
- `internal/mcp-router/baggage_test.go` (new)

**Acceptance criteria:**
- [ ] Parse `baggage` header per W3C Baggage spec, extract `user.id` and `agent.id` keys
- [ ] URL-decode extracted values
- [ ] Strip control characters (CR, LF, null bytes) from decoded values to prevent header injection
- [ ] Handle malformed baggage, missing baggage, missing keys gracefully (return empty string)
- [ ] When `user.id` absent from baggage, check headers from `MCP_AUDIT_IDENTITY_HEADERS` env var (default: `x-forwarded-email,x-auth-user`) in order
- [ ] First non-empty fallback value used; empty string if none found

**Verification:** `make test-unit`

### Task 2: Opt-in parameter extraction

**Files:**
- `internal/mcp-router/audit.go` (new)
- `internal/mcp-router/audit_test.go` (new)

**Acceptance criteria:**
- [ ] When `MCP_AUDIT_LOG_PARAMS=true`, extract `params.arguments` from parsed MCP request body
- [ ] Serialize as JSON string
- [ ] Truncate to 1KB
- [ ] When env var is unset or `false`, return empty string
- [ ] Handle missing `params.arguments` gracefully

**Verification:** `make test-unit`

### Task 3: Audit headers on ProcessingResponse

**Files:**
- `internal/mcp-router/headers.go` (modify — add `WithMCPUserID`, `WithMCPAgentID`, `WithMCPToolParams` methods)
- `internal/mcp-router/request_handlers.go` (modify — call baggage parsing and wire audit headers into the builder chain)
- `internal/mcp-router/request_handlers_test.go` (modify)

**Acceptance criteria:**
- [ ] `x-mcp-user-id` set on all tool call ProcessingResponses
- [ ] `x-mcp-agent-id` set on all tool call ProcessingResponses
- [ ] `x-mcp-tool-params` set only when `MCP_AUDIT_LOG_PARAMS=true`
- [ ] Headers set to `-` when source data is unavailable
- [ ] Existing `x-mcp-*` headers unchanged

**Verification:** `make test-unit`

### Task 4: AuditConfig CRD type and env var wiring

**Files:**
- `api/v1alpha1/mcpgatewayextension_types.go` (modify — add `AuditConfig` struct and `Audit` field to spec)
- `internal/controller/broker_router.go` (modify — inject `MCP_AUDIT_LOG_PARAMS` and `MCP_AUDIT_IDENTITY_HEADERS` env vars, add to `managedEnvVarNames`)
- `docs/reference/mcpgatewayextension.md` (modify — add audit field documentation)

**Acceptance criteria:**
- [ ] `AuditConfig` struct with `ParameterLogging` (`ParameterLoggingPolicy` enum: `Enabled`/`Disabled`) and `IdentityHeaders` ([]string)
- [ ] `Audit *AuditConfig` optional pointer field on `MCPGatewayExtensionSpec`
- [ ] When `spec.audit` is set, operator injects `MCP_AUDIT_LOG_PARAMS` and `MCP_AUDIT_IDENTITY_HEADERS` env vars into the router deployment
- [ ] `ParameterLogging` enum translated to env var: `Enabled` -> `"true"`, `Disabled`/empty -> `"false"`
- [ ] When `spec.audit` is nil, no audit env vars are injected
- [ ] `MCP_AUDIT_LOG_PARAMS` and `MCP_AUDIT_IDENTITY_HEADERS` added to `managedEnvVarNames`
- [ ] `make generate-all` succeeds (deepcopy, CRDs, Helm sync)
- [ ] CRD reference doc updated

**Verification:** `make generate-all && make test-controller-integration`

### Task 5: Operator access log ConfigPatch

**Files:**
- `internal/controller/mcpgatewayextension_controller.go` (modify — add `NETWORK_FILTER` ConfigPatch to `buildEnvoyFilter()`)
- `internal/controller/mcpgatewayextension_controller_test.go` (modify)

**Acceptance criteria:**
- [ ] When `spec.audit` is set, EnvoyFilter includes a second ConfigPatch with `ApplyTo: NETWORK_FILTER`
- [ ] Patch targets `envoy.filters.network.http_connection_manager` on the same listener port as the ext_proc patch (`listenerConfig.Port`)
- [ ] Patch operation is `MERGE` (modifying existing HCM, not inserting a new filter)
- [ ] Access log format contains all `%REQ(...)%` fields from the design doc
- [ ] When `spec.audit` is nil, no access log patch is added (existing behavior preserved)
- [ ] EnvoyFilter update propagates when MCPGatewayExtension changes

**Verification:** `make test-controller-integration`

### Task 6: E2E tests

**Files:**
- `tests/e2e/` (new test file)
- `tests/e2e/test_cases.md` (modify — add Auditing test cases)

**Acceptance criteria:**
- [ ] All 7 E2E scenarios from `tasks/e2e_test_cases.md` pass
- [ ] Existing E2E tests unaffected

**Verification:** E2E test suite in CI

### Task 7: Auditing guide

**Files:**
- `docs/guides/auditing.md` (new)
- `docs/guides/README.md` (modify — add auditing guide link)

**Acceptance criteria:**
- [ ] Guide covers all sections from `tasks/documentation.md`
- [ ] Follows conventions in `docs/CLAUDE.md` (how-to style, numbered steps, no repo-internal refs)
- [ ] AuthPolicy integration documented with example YAML

**Verification:** Manual review
