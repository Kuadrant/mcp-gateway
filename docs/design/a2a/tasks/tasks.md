# A2A Protocol Support — Implementation Plan

## Existing Code Analysis

The following primitives exist in the codebase and are reused directly by the A2A implementation:

| Primitive | Location | Reused for |
|---|---|---|
| ext_proc Process() loop | `internal/mcp-router/server.go` | A2A traffic detection and routing |
| ResponseBuilder | `internal/mcp-router/response_builder.go` | Building all ext_proc responses |
| HeadersBuilder | `internal/mcp-router/headers.go` | Setting routing headers |
| sseRewriter | `internal/mcp-router/elicitation.go` | Template for a2aSSEObserver (read-only line reader) |
| idmap.Map | `internal/idmap/map.go` | Template for the task-record store (same in-memory/Redis duality) |
| session.Cache | `internal/session/cache.go` | Extended with task-record methods |
| JWTManager.Validate() | `internal/session/jwt.go` | Session validation for A2A requests |
| idmap Redis TTL pattern | `internal/idmap/redis.go` | Fixed safety-net TTL + explicit cleanup; the A2A task-store TTL is decoupled from JWT/session expiry, not derived from it |
| config.Observer | `internal/config/types.go` | A2A broker registers as observer |
| MCPServersConfig.Notify() | `internal/config/types.go` | Triggers A2A broker config updates |
| SecretReaderWriter | `internal/config/config_writer.go` | Extended with UpsertA2AAgent/RemoveA2AAgent |
| MCPReconciler | `internal/controller/mcpserverregistration_controller.go` | Template for A2AReconciler |
| HTTPRouteWrapper | `internal/controller/httproute_wrapper.go` | Used directly in A2AReconciler |
| buildGatewayHTTPRoute() | `internal/controller/broker_router.go` | Modified to add /a2a prefix and /.well-known/api-catalog rules |
| ModeOverride (SSE) | `internal/mcp-router/response_handlers.go` | A2A streaming passthrough |

---

## Phase 1: Foundation (Weeks 1–4)

### Task 1: Design Document + Gap Analysis

**Files:**
- `docs/design/a2a/a2a-design.md` (this document's companion)
- `docs/design/a2a/tasks/tasks.md` (this file)
- `docs/design/a2a/tasks/e2e_test_cases.md`

**Acceptance criteria:**
- [ ] Design doc covers all sections per `docs/design/CLAUDE.md` structure
- [ ] Mermaid diagrams for agent card discovery, SendMessage routing, task lifecycle
- [ ] Open design questions surfaced as explicit tradeoff analyses
- [ ] `make spell` passes
- [ ] Mentors approve design before Task 2 begins

**Verification:**
```bash
make spell
```

---

### Task 2: A2A Test Server

**Files:**
- `tests/servers/a2a-server/main.go`
- `tests/servers/a2a-server/Dockerfile`
- `config/test-servers/a2a-server-deployment.yaml`
- `config/test-servers/a2a-server-service.yaml`
- `config/test-servers/a2a-server-httproute.yaml`
- `config/test-servers/kustomization.yaml` (updated)

**Acceptance criteria:**
- [ ] `GET /.well-known/agent-card.json` (v1.0 well-known path) returns a valid v1.0 AgentCard (`supportedInterfaces` carrying the server's own address, named `securitySchemes`) configurable via `AGENT_NAME`, `SKILLS`, `AGENT_PREFIX` env vars; optionally JWS-signed to exercise the verbatim-serving path
- [ ] `POST /a2a` dispatches `SendMessage` (blocking by default in v1.0), `GetTask`, `CancelTask`, `SubscribeToTask`
- [ ] SSE streaming via `SendStreamingMessage` (the v1.0 streaming method, a distinct JSON-RPC method — NOT `SendMessage` + `Accept`): three `working` events then `completed`, task IDs at the envelope identity field (the task `id`, then `taskId` on updates)
- [ ] Kubernetes manifests follow `config/test-servers/server1-deployment.yaml` pattern
- [ ] Server added to `config/test-servers/kustomization.yaml`

**Verification:**
```bash
curl http://a2a-test-server.mcp-test.svc.cluster.local:9090/.well-known/agent-card.json
curl -X POST http://a2a-test-server.mcp-test.svc.cluster.local:9090/a2a \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"user","parts":[{"text":"hello"}]}}}'
```

---

### Task 3: A2AAgentRegistration CRD Finalization

*Depends on: Task 1 (design doc approved, API group question answered)*

**Files:**
- `api/v1alpha1/a2aagentregistration_types.go`
- `api/v1alpha1/zz_generated.deepcopy.go` (regenerated)
- `config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml` (regenerated)
- `charts/mcp-gateway/crds/mcp.kuadrant.io_a2aagentregistrations.yaml` (regenerated)
- `docs/reference/a2aagentregistration.md`

**Acceptance criteria:**
- [ ] `agentPrefix` immutability CEL rule passes `make lint`
- [ ] `agentCardURL` URL format validation present
- [ ] `targetRef` uses `omitzero` (kubeapilinter requirement)
- [ ] `make generate-all` produces no diff after this PR
- [ ] `kubectl apply -f config/crd/...a2aagentregistrations.yaml` succeeds against Kind cluster
- [ ] kubeapilinter passes in CI

**Verification:**
```bash
make generate-all
git diff --exit-code
kubectl apply -f config/crd/mcp.kuadrant.io_a2aagentregistrations.yaml
make lint
```

---

### Task 4: A2AReconciler Scaffold

**Files:**
- `internal/controller/a2aagentregistration_controller.go` (new — scaffold only)
- `cmd/main.go` (register A2AReconciler)

**Acceptance criteria:**
- [ ] `A2AReconciler` struct has `Client`, `Scheme`, `DirectAPIReader`, `ConfigReaderWriter`, `MCPExtFinderValidator` fields
- [ ] `Reconcile()` returns `ctrl.Result{}` — skeleton only
- [ ] `SetupWithManager()` watches `A2AAgentRegistration`, `HTTPRoute`, `Secret` with same predicates as `MCPReconciler`
- [ ] Uses distinct finalizer `"mcp.kuadrant.io/a2a-finalizer"`
- [ ] `make build` passes
- [ ] Controller starts without errors against Kind cluster

**Verification:**
```bash
make build
make deploy
kubectl logs -n mcp-system deployment/mcp-gateway-controller
```

---

### Task 5: A2AReconciler Reconcile Logic + Tests

*Depends on: Task 4*

**Files:**
- `internal/controller/a2aagentregistration_controller.go` (fill in reconcile logic)
- `internal/controller/a2aagentregistration_controller_test.go` (new)
- `internal/controller/a2aagentregistration_controller_integration_test.go` (new)

**Acceptance criteria:**
- [ ] Finalizer added on create, removed only after config is cleaned up
- [ ] `getTargetHTTPRoute()` resolves HTTPRoute using `WrapHTTPRoute()` + `Validate()`, honoring `targetRef.namespace` (defaulting to the registration's namespace) — and the HTTPRoute field index resolves the namespace identically, so cross-namespace watches fire
- [ ] Cross-namespace `targetRef` requires a `ReferenceGrant` in the route's namespace (`from`: `A2AAgentRegistration`, `to`: `HTTPRoute`), mirroring `MCPGatewayExtension`'s grant check; no grant → `Ready=False` and the agent's config is withdrawn (revoking consent revokes the exposure, not just the status); `ReferenceGrant` changes trigger re-reconcile via a `ReferenceGrant` watch
- [ ] `buildA2AAgentConfig()` handles `IsHostnameBackend()` and `IsServiceBackend()` using existing helpers
- [ ] `UpsertA2AAgent()` called for each valid MCPGatewayExtension namespace
- [ ] Status conditions: `Ready=True` (reason `Ready`) when config is written, mirroring `MCPServerRegistration` — `Ready` is not a promise the agent is reachable or serving, and no discovered card content (skills, card fields) appears in status
- [ ] `state: Disabled`: config is still written carrying `state: Disabled` (the broker acts on the flag), and status is `Ready=False, Reason=Disabled`, mirroring `MCPServerRegistration`; re-enabling restores `Ready=True`
- [ ] Controller integration tests: new registration → Ready=True; missing HTTPRoute → Ready=False; deletion removes config; cross-namespace `targetRef` with a `ReferenceGrant` resolves and reconciles; cross-namespace without a grant → Ready=False, no config written; revoking the grant withdraws previously written config

**Verification:**
```bash
make test-controller-integration
```

---

## Phase 2: Core Components (Weeks 5–8, buffer at Week 6)

### Task 6: Config Plumbing + Hot-Reload

**Files:**
- `internal/config/types.go`
- `internal/config/a2a_types.go` (new)
- `internal/config/config_writer.go`

**Acceptance criteria:**
- [ ] `A2AAgents []*A2AAgent` added to `MCPServersConfig` with RWMutex protection
- [ ] `SetA2AAgents()`, `ListA2AAgents()` follow `SetServers()`/`ListServers()` pattern exactly
- [ ] `UpsertA2AAgent()` and `RemoveA2AAgent()` in `SecretReaderWriter` with retry-on-conflict
- [ ] `BrokerConfig` YAML schema gains `a2aAgents` key
- [ ] `Notify()` passes A2A agents to observers alongside MCP servers
- [ ] `go test -race ./internal/config/...` passes

**Verification:**
```bash
go test -race ./internal/config/...
make test-unit
```

---

### Task 7: A2A Broker — Observer Wiring

*Depends on: Task 6*

**Files:**
- `internal/a2a/broker.go` (finalize PoC, wire Observer)
- `internal/a2a/broker_test.go` (extend)

**Acceptance criteria:**
- [ ] `a2a.Broker` implements `config.Observer`: `OnConfigChange()` calls `SetAgents(cfg.ListA2AAgents())`
- [ ] `ServeAPICatalog()` has OTel span `"a2a.ServeAPICatalog"` with `agent.count` attribute, following `HandleToolCall()` pattern
- [ ] `A2AAgentManager` caches the upstream card with a ticker refresh (mirroring `MCPManager`), serving stale-on-error; `ServeAgentCard()` serves the cached card **verbatim** — a signed card's JWS signature must survive byte-for-byte, so the card is not rewritten; the catalog is what advertises the gateway path — not a per-request upstream proxy
- [ ] Card refresh is poll-only (A2A has no card-change push): ticker re-fetch with conditional GET (`If-None-Match`/`If-Modified-Since`) + `version`/SHA-256 change detection; act only on change (in-memory cache swap under RWMutex, no Secret write); staleness bound = ticker interval (reuse `managerTickerInterval`, default 1 min)
- [ ] No discovered card content is surfaced in registration status (`Ready` = config written); the broker's ticker refresh is the only live card sync
- [ ] `credentialRef` is used by the broker ONLY for the card fetch (discovery), never injected into client `SendMessage`/`tasks/*` (router has no `credentialRef` access; invocation auth = forwarded client bearer or RFC 8693 token exchange via AuthPolicy)
- [ ] Unit tests: `OnConfigChange` triggers `SetAgents`; `ServeAgentCard` with unreachable upstream skips gracefully; `ServeAgentCard` serves the signed card verbatim (no url rewrite); `GetAgentByPath` lookup
- [ ] `go test -race ./internal/a2a/...` passes

**Verification:**
```bash
go test -race ./internal/a2a/...
make test-unit
```

---

### Task 8: A2A Broker — Binary Wiring

*Depends on: Task 7*

**Files:**
- `cmd/mcp-broker-router/main.go`
- `cmd/mcp-broker-router/broker.go`
- `cmd/mcp-broker-router/router.go`

**Acceptance criteria:**
- [ ] `a2aBroker` initialized in `main.go` and registered as observer: `cfg.RegisterObserver(a2aBroker)`
- [ ] `/.well-known/api-catalog` (Content-Type `application/linkset+json`) and `/a2a/{namespace}/{prefix}/.well-known/agent-card.json` registered in `setUpHTTPServer()` after `/.well-known/oauth-protected-resource`
- [ ] `A2ABroker a2a.Broker` field added to `ExtProcServer` struct in `createRouter()`
- [ ] `make build` passes

**Verification:**
```bash
make build
make deploy
curl http://mcp.127-0-0-1.sslip.io:8001/.well-known/api-catalog
# expect: {"links":[]} (no agents registered yet)
```

---

### Task 9: Router — A2A Traffic Detection

*Depends on: Task 8*

**Files:**
- `internal/mcp-router/server.go`
- `internal/mcp-router/headers.go`
- `internal/headers/headers.go`
- `internal/controller/broker_router.go`

**Acceptance criteria:**
- [ ] `isA2A` bool set in `Process()` at `RequestHeaders` phase via a **segment-aware** path match (`/a2a/` prefix or exact `/a2a` — a bare `HasPrefix("/a2a")` would also match `/a2ax`)
- [ ] At `RequestHeaders` phase: extract (namespace, prefix) from `:path`, call `A2ABroker.GetAgentByPath()`, set `:authority` to agent hostname + `x-a2a-agent`. Method-specific work (ownership lookup, task-record binding) is deferred to `RequestBody` — the JSON-RPC method is known only there
- [ ] A2A header constants defined in `internal/headers/headers.go`: `A2AAgentHeader`, `A2ATaskIDHeader`, `A2AMethodHeader`
- [ ] `WithA2AAgent()`, `WithA2ATaskID()`, `WithA2AMethod()` added to `HeadersBuilder`
- [ ] `x-a2a-agent`, `x-a2a-task-id`, and `x-a2a-method` added to `internalOnlyHeaders` and the `stripRouterHeaders` filter
- [ ] `buildGatewayHTTPRoute()` gains `/a2a` prefix rule with `stripRouterHeaders` and `/.well-known/api-catalog` rule
- [ ] Stub `RouteA2ARequest()` returning empty pass-through
- [ ] Unit tests: mock ext_proc stream with `/a2a/mcp-test/weather` path → `isA2A=true`, prefix "weather" extracted; `/mcp` path → `isA2A=false`
- [ ] `make test-unit` passes

**Verification:**
```bash
make test-unit
make lint
```

---

### Task 10: Router — A2A Request Routing

*Depends on: Task 9*

**Files:**
- `internal/mcp-router/request_handlers.go`
- `internal/mcp-router/request_handlers_test.go`

**Acceptance criteria:**
- [ ] `A2ARequest` struct: `ID any`, `JSONRPC string`, `Method string`, `Params map[string]any`
- [ ] `parseA2ARequest(body []byte) (*A2ARequest, error)`
- [ ] `RouteA2ARequest()`: authenticates via OAuth principal (`ExtractSubClaim`), switches on `SendMessage`/`SendStreamingMessage`/`GetTask`/`CancelTask`/`SubscribeToTask`
- [ ] `HandleA2ATaskSend()`: does not mint or rewrite any task ID (the agent assigns it); a send whose `message.taskId`/`referenceTaskIds` name an existing task is ownership-checked like `GetTask` before forwarding; at `ResponseHeaders` the method picks the `ModeOverride` — `STREAMED` for `isStreamingMethod()` (`SendStreamingMessage`/`SubscribeToTask`), `BUFFERED` for `SendMessage` so the response body is observable at all (the filter's default `response_body_mode` is `NONE`); `GetTask`/`CancelTask` set no override
- [ ] Errors are `application/json` JSON-RPC (NOT SSE-framed): unknown method → `-32601`; deferred v1.0 methods (`ListTasks`, extended card, `pushNotificationConfig`) → `-32004 UnsupportedOperationError`, never forwarded; missing/expired/mismatched ownership record → `-32001 TaskNotFoundError` (fail closed, nothing forwarded); missing/invalid bearer rejected by AuthPolicy at the edge, empty principal → fail closed
- [ ] A2A spans carry `a2a.task.id` (the agent-assigned task ID), `a2a.method`, `a2a.agent` attributes (`a2aSpanAttributes`, analog of `spanAttributes`) so operators correlate an async task's lifecycle across separate requests; task ID is a span attribute only, never a metric label
- [ ] MCP path (`/mcp` traffic) completely unaffected — regression tests pass
- [ ] Unit tests cover all branches above

**Verification:**
```bash
make test-unit
# deploy and test end-to-end:
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/a2a/mcp-test/weather \
  -H "Authorization: Bearer <oauth-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{...}}}'
```

---

## Phase 3: Integration & Hardening (Weeks 9–12)

### Task 11: Task Ownership Records

*Depends on: Task 10*

**Files:**
- `internal/session/cache.go`
- `internal/session/cache_test.go`

**Note:** The `a2a-task-routing-infra` branch has an existing partial implementation built for
gateway-owned task IDs (a `StoreTaskRoute` that maps a gateway ID to an upstream ID). Under
passthrough there is no ID mapping — task IDs are the agent's and pass through unchanged — so that
branch is superseded: reuse only its in-memory/Redis plumbing, keyed by `(agent, taskID)` and holding
the owning principal rather than a route.

**Acceptance criteria:**
- [ ] `taskRecords sync.Map` field added to `Cache` (separate from `inmemory`), keyed by `(agent, taskID)`
- [ ] `StoreTaskRecord(ctx, agentName, taskID string, rec TaskRecord) error` implemented for in-memory and Redis
- [ ] `LookupTaskRecord(ctx, agentName, taskID string) (TaskRecord, bool, error)` implemented for in-memory and Redis
- [ ] `DeleteTaskRecord(ctx, agentName, taskID string) error` implemented for in-memory and Redis
- [ ] `SessionCache` interface in `internal/mcp-router/server.go` updated with the above signatures
- [ ] Redis key prefix `a2atask:{agent}/{taskID}`, **fixed retention TTL decoupled from the JWT** (idmap pattern), sized ≥ the agents' task-retention window; records are NOT deleted on terminal states (tasks stay retrievable after completion)
- [ ] `StoreTaskRecord()` is insert-only via `LoadOrStore` (in-memory) / `SET NX` (Redis), returning new-insert / same-owner / different-owner / store-unavailable; the response or first task-creating event is withheld until binding succeeds
- [ ] Parallel insert-only `(agent, contextId) -> principal` record, bound from the first task/message response or stream event; both send methods verify context ownership when a request carries a `contextId`
- [ ] Card validation rejects non-`http(s)` scheme, non-`JSONRPC` binding, and non-v1 `protocolVersion` in addition to path and host; catalog eligibility requires a currently cached validated card
- [ ] `TaskRecord.Principal` set from the OAuth `sub`; `LookupTaskRecord()` callers verify the requesting principal owns the task (routing is by path, so the record is used for ownership only)
- [ ] `HandleA2ATaskSend()` updated: read `result.task.id` from the response (v1.0 `SendMessageResponse` oneof — the `result.message` variant creates no task and stores nothing), call `StoreTaskRecord()`; the response body is forwarded unchanged (no rewrite)
- [ ] `HandleA2ATaskGet()`/`HandleA2ATaskCancel()`/`SubscribeToTask` — and sends naming an existing task — call `LookupTaskRecord()` and verify principal ownership; a missing/expired/mismatched record fails closed with `-32001` (no ID rewrite anywhere)
- [ ] Concurrency test: 100 goroutines reading and writing task records with `-race`
- [ ] `go test -race ./internal/session/...` passes

**Verification:**
```bash
go test -race ./internal/session/...
make test-unit
```

---

### Task 12: SSE Streaming Passthrough

*Depends on: Task 11*

**Files:**
- `internal/mcp-router/elicitation.go` (add `a2aSSEObserver`)
- `internal/mcp-router/response_handlers.go`
- `internal/mcp-router/server.go`

**Acceptance criteria:**
- [ ] `a2aSSEObserver` struct with `Process(ctx, chunk []byte) []byte` and `Flush(ctx) []byte`; `Process()` returns each `data:` line **unchanged** (read-only tap)
- [ ] `Process()` reads the streaming event identity field in `data:` lines — `result.task.id` on the initial `task` event, `result.statusUpdate.taskId`/`result.artifactUpdate.taskId` on updates (v1.0 has no `kind` discriminator; the variant is which oneof member is present) — and uses it only to `StoreTaskRecord()` (insert-only) on the first `task` event; terminal states end the stream, the record persists to the retention TTL
- [ ] Envelope-only parsing: read the JSON-RPC envelope + result identity fields only; never decode `status`/`artifact`/`history`/`parts` (incl. `FilePart.file.bytes`/`DataPart.data`); no re-marshal, so cost is O(envelope) and Part content is untouched by construction
- [ ] `HandleResponseHeaders()` sets `ModeOverride ResponseBodyMode=STREAMED` when `isA2A && isStreamingA2AMethod()` (`SendStreamingMessage`/`SubscribeToTask`), and `BUFFERED` for `SendMessage` — required for observation because the filter's default `response_body_mode` is `NONE` (without the override the router never receives `ResponseBody`); `GetTask`/`CancelTask` set no override (bare-`Task` responses, nothing to observe)
- [ ] `SendMessage` reads `result.task.id` in `ResponseBody` for the ownership record (`result.message` variant stores nothing), then forwards the body unchanged — no `content-length` surgery, since nothing is mutated (the content-length removal the spike proved is needed only by a body rewrite; that half stays in reserve)
- [ ] `Process()` loop invokes `a2aSSEObserver` in `ResponseBody` phase; an A2A flag gates it continuing into `ResponseBody` (today it only continues when `rewriter != nil`)
- [ ] Unit tests: SSE chunks pass through byte-for-byte; the first-event ownership record is stored (insert-only) and survives the terminal state; non-SSE responses unaffected

**Verification:**
```bash
make test-unit
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/a2a \
  -H "Authorization: Bearer <oauth-token>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"SendStreamingMessage","params":{...}}'
# expect: SSE stream forwarded unchanged (agent-assigned task IDs intact in all events)
```

---

### Task 13: E2E Tests — Discovery + Task Routing

*Depends on: Task 12*

**Files:**
- `tests/e2e/a2a_discovery_test.go`
- `tests/e2e/a2a_task_test.go`
- `tests/e2e/test_cases.md` (updated)

**Acceptance criteria:**
- [ ] Agent card discovery: `GET /.well-known/api-catalog` returns an RFC 9727 catalog (RFC 9264 Linkset) with agent links; `GET /a2a/mcp-test/weather/.well-known/agent-card.json` returns the test server's agent card served verbatim (a signed card's JWS signature intact), with the catalog link — not a rewritten card URL — routing the client to the gateway path
- [ ] Task send: `SendMessage` to `/a2a/{namespace}/{prefix}` routes to correct upstream, returns the agent-assigned task ID unchanged
- [ ] Task get: `GetTask` with that same task ID routes by path and returns the upstream result
- [ ] Task cancel: `CancelTask` propagates to upstream, returns canceled state
- [ ] Agent deregistration: deleting `A2AAgentRegistration` removes the agent from the API catalog within one reconcile cycle

**Verification:**
```bash
ginkgo -v --label-filter="A2A" ./tests/e2e/...
```

---

### Task 14: E2E Tests — Streaming + Auth + Error + Regression

*Depends on: Task 13*

**Files:**
- `tests/e2e/a2a_discovery_test.go` (extend)
- `tests/e2e/a2a_task_test.go` (extend)

**Acceptance criteria:**
- [ ] Streaming: `SendStreamingMessage` delivers SSE chunks with the agent-assigned task IDs passed through unchanged (task `id`, then `taskId` on updates); a per-principal ownership record is created on the first event
- [ ] Auth: request without a valid OAuth bearer returns 401 (AuthPolicy) before reaching upstream
- [ ] Unknown path: `SendMessage` to unregistered `/a2a/{namespace}/{prefix}` returns JSON-RPC `-32602`
- [ ] MCP regression: `tools/list` and `tools/call` work correctly after all A2A changes
- [ ] All E2E tests pass: `ginkgo -v ./tests/e2e/... -- --gateway-host=mcp.127-0-0-1.sslip.io:8001`

**Verification:**
```bash
ginkgo -v ./tests/e2e/... -- --gateway-host=mcp.127-0-0-1.sslip.io:8001
```

---

### Task 15: Documentation + Final Polish

**Files:**
- `docs/guides/a2a-agent.md`
- `docs/guides/README.md` (updated)

**Acceptance criteria:**
- [ ] Guide follows `docs/CLAUDE.md` conventions: goal-oriented, numbered steps, verification commands, no internal references
- [ ] Covers: prerequisites, Step 1 (HTTPRoute), Step 2 (A2AAgentRegistration), Step 3 (verify agent card), Step 4 (send a task), credentialRef usage
- [ ] Links to authentication guide for AuthPolicy on the `/a2a` path
- [ ] `make spell` passes
- [ ] Guide reviewed and approved by mentor

**Verification:**
```bash
make spell
```
