# URL Elicitation Implementation Plan

## Context

The MCP Gateway needs to support per-user credential collection for upstream MCP servers where credentials are disconnected from the gateway's identity provider (e.g., gateway uses RH-SSO but upstream needs a GitHub PAT). The design uses the MCP spec's URLElicitationRequiredError (-32042) flow: router detects missing credential, returns error with URL, user provides credential on broker-hosted page, client retries.

Jira: CONNLINK-991 (stories: CONNLINK-995 through CONNLINK-999)
Design: `docs/design/gateway-url-token-elicitation/gateway-url-token-elicitation-design.md`
Branch: `URL-Elicitation-User-Credentials`

## Existing Code to Build On

### Client elicitation capability flow (already implemented)
1. **Parsed**: `MCPRequest.clientSupportsElicitation()` (`request_handlers.go:134-149`) — checks `capabilities.elicitation` in initialize params
2. **Stored**: `HandleResponseHeaders` (`response_handlers.go:26-35`) — on initialize response, calls `SessionCache.SetClientElicitation(ctx, sessionID)` to persist the flag
3. **Read**: `initializeMCPSeverSession` (`request_handlers.go:537-544`) — reads `SessionCache.GetClientElicitation()` and sets `mcpReq.clientElicitation`
4. **Forwarded**: `clients.Initialize()` (`internal/clients/clients.go:40-42`) — if client declared elicitation, gateway declares it to backend too via `mcp.ElicitationCapability{}`
5. **Cache impl**: `internal/session/cache.go:96-120` — stores as `clientelicitation:<sessionID>` key

The credential resolution (Task 5) will reuse this flow — call `GetClientElicitation()` to determine whether to return `-32042` or a standard error.

### Other existing code
- `SessionCache` interface (`internal/mcp-router/server.go:23-31`) — already has `SetClientElicitation`/`GetClientElicitation`
- `HandleToolCall` (`internal/mcp-router/request_handlers.go:242-425`) — where credential resolution logic will be added
- `MCPServerRegistrationSpec` (`api/v1alpha1/types.go:38-65`) — CRD spec to extend
- `config.MCPServer` (`internal/config/types.go:59-67`) — config type to extend
- `mcpserverregistration_controller.go:386` — where config is built from CRD
- SSE rewriter + idmap package — existing elicitation infrastructure (for upstream-initiated form-mode elicitation, not URL elicitation)
- `headers.go` — `WithAuth` method for authorization header injection
- `MCPRequest.clientElicitation` field (`request_handlers.go:85`) — bool on the request struct, already populated during session init

## Implementation Order

Tasks are ordered by dependency. Each task maps to a Jira story.

Tasks 1 and 2 are complete on branch `ute-crds-feature-flag` and will merge after the remaining tasks are done. Tasks 3–8 build on `main` and will be rebased onto Tasks 1+2 before final merge.

### Task 1: Feature flag `--enable-url-elicitation` (part of CONNLINK-997) — DONE

Branch: `ute-crds-feature-flag`

**Files:**
- `cmd/mcp-broker-router/main.go` — add `--enable-url-elicitation` flag (default: false), pass to ExtProcServer and Broker
- `internal/mcp-router/server.go` — add `ElicitationEnabled bool` field to `ExtProcServer`
- `internal/broker/broker.go` — add `ElicitationEnabled bool` field

**Acceptance criteria:**
- [ ] Flag parsed and plumbed to router and broker
- [ ] When disabled, no elicitation behavior changes (verified by existing tests passing)

**Verification:** `make test-unit` passes, `--help` shows the flag

---

### Task 2: CRD + config types (CONNLINK-995) — DONE

Branch: `ute-crds-feature-flag`

**Files:**
- `api/v1alpha1/types.go` — add `TokenURLElicitation *TokenURLElicitationConfig` to `MCPServerRegistrationSpec`
- `api/v1alpha1/types.go` — add `TokenURLElicitationConfig` struct with `URL string`
- `internal/config/types.go` — add `TokenURLElicitation *TokenURLElicitationConfig` to `MCPServer`
- `internal/config/types.go` — add `TokenURLElicitationConfig` struct
- `internal/controller/mcpserverregistration_controller.go:386` — propagate field from CRD to config
- Run `make generate-all` to regenerate deepcopy, CRDs, sync Helm

**Acceptance criteria:**
- [ ] CRD accepts `tokenURLElicitation` with optional `url` field
- [ ] Controller propagates to config Secret
- [ ] Unit test: controller includes elicitation config when set, omits when not set

**Verification:** `make generate-all && make lint && make test-unit`

---

### Task 3: User token cache with encryption (CONNLINK-996)

**Files:**
- `internal/session/cache.go` — extends existing `SessionCache` with user token methods:
  ```go
  SetUserToken(ctx context.Context, sessionID, serverName, token string) error
  GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error)
  DeleteUserToken(ctx context.Context, sessionID, serverName string) error
  ```
- Stores as `token:<serverName>` field on session hash
- Redis backend uses AES-GCM encryption; encryption key derived from session signing key via HKDF (RFC 5869)
- In-memory backend stores plaintext (no encryption overhead)
- Unit tests in existing session cache test files

**JWT Expiry Check:**
On `GetUserToken`, attempt to parse the token as a JWT (three dot-separated base64url segments). If it parses successfully, check the `exp` claim — if expired, delete the token and return a cache miss. If the token doesn't parse as a JWT (e.g. opaque PAT like a GitHub token), skip expiry checking and return it as-is for upstream use.

**Acceptance criteria:**
- [x] Set/get/delete works for both backends
- [x] Redis protocol compliant backend encrypts values (not plaintext in store)
- [x] In-memory backend stores plaintext (no encryption overhead)
- [x] Token deleted when session hash is deleted
- [x] JWT tokens checked for expiry on get — expired tokens deleted and treated as cache miss
- [x] Non-JWT tokens (opaque PATs) returned as-is without expiry check

**Verification:** `make test-unit` — tests cover encrypt/decrypt round-trip, set/get/delete, missing key returns false, expired JWT returns miss, opaque token returned without expiry check

---

### Task 4: Router token resolution + elicitation trigger (CONNLINK-997)

Depends on: Task 3. Core router logic — can be unit-tested with mock cache before the broker page exists.

**Files:**
- `internal/mcp-router/request_handlers.go` — modify `HandleToolCall` to add token resolution:
  1. Check if server has `TokenURLElicitation` config — if not, skip (existing behavior)
  2. If `--enable-url-elicitation` is false, skip (existing behavior)
  3. Check `Authorization` header from client request — if present, use as-is (no token injection needed)
  4. Check `UserTokenCache.GetUserToken(sessionID, serverName)` — if hit, inject via `headers.WithAuth()`
  5. On cache miss, check client elicitation capability via `SessionCache.GetClientElicitation(ctx, gatewaySessionID)` (already stored during initialize handshake in `HandleResponseHeaders`)
  6. If client supports elicitation → return `-32042` SSE immediate response with token page URL
  7. If client does not support elicitation → return standard error (non-interactive agent path)
- `internal/mcp-router/request_handlers.go` — add helper to build `-32042` response with URL (broker page or external URL from config)
- `internal/mcp-router/request_handlers_test.go` — unit tests for each path
- `internal/mcp-router/server.go` — add `UserTokenCache` field to `ExtProcServer`

**Acceptance criteria:**
- [x] Existing Authorization header used as-is (no regression)
- [x] Cached token injected on hit
- [x] -32042 returned on miss with elicitation-capable client
- [x] Standard error returned on miss without capability
- [ ] Feature flag disabled → existing behavior unchanged
- [x] URL in -32042 uses external URL when `tokenURLElicitation.url` is set

**E2E test cases (from `e2e_test_cases.md`):**
- `[Happy,URLElicitation] URL elicitation triggers on missing token for elicitation-capable client`
- `[Happy,URLElicitation] Token provided via broker page is used for upstream auth`
- `[Happy,URLElicitation] Full elicitation round-trip with token validation`
- `[URLElicitation] Cached token reused across multiple tool calls`
- `[URLElicitation] Elicitation with external URL returns configured URL`
- `[URLElicitation] Non-elicitation-capable client gets standard error on missing token`
- `[Happy,URLElicitation] Non-elicitation-capable client with Authorization header succeeds`
- `[URLElicitation] Elicitation-capable client with Authorization header bypasses cache`
- `[URLElicitation] Elicitation disabled via feature flag has no effect`
- `[Happy,URLElicitation] Server without tokenURLElicitation is unaffected`

**Documentation (from `documentation.md`):**
- Guide section: "When I want to securely collect per-user tokens for an upstream MCP server" — adding `tokenURLElicitation`, enabling the feature, user experience flow, prerequisites
- Guide section: "When I want to use my own credential UI instead of the built-in page" — external URL config, AuthPolicy on upstream route, differences from default flow
- Guide section: "When I have automated agents that can't use a browser" — automatic capability-based behavior, agents passing Authorization header, 401 handling for agents

**Verification:** `make test-unit && make test-e2e`

---

### Task 5: Broker token page (CONNLINK-998)

Depends on: Task 3. Independent of Task 4 — touches broker, not router.

**Files:**
- `internal/broker/tokens.go` (new) — HTTP handler for token page
  - `GET /tokens?elicitation_id=<id>` — renders HTML form
  - `POST /tokens` — stores token in cache, returns success
- `internal/broker/tokens_test.go` (new) — unit tests
- `cmd/mcp-broker-router/main.go` — register `/tokens` endpoint (gated behind `--enable-url-elicitation`)
- `internal/broker/broker.go` — broker needs access to `UserTokenCache`

**Acceptance criteria:**
- [x] GET renders form showing server name
- [x] POST verifies the session JWT on the request matches the session ID in the elicitation ID before storing
- [x] POST stores token in cache keyed by session ID and server name
- [x] Invalid/missing elicitation_id returns error
- [x] Session mismatch between request JWT and elicitation ID returns error
- [ ] Endpoint only registered when `--enable-url-elicitation` is true

**E2E test cases (from `e2e_test_cases.md`):**
- `[URLElicitation] Broker token page rejects invalid elicitation ID`
- `[URLElicitation,Security] Broker token page rejects session mismatch`
- `[URLElicitation,Security] Token page rejects mismatched session (phishing prevention)`

**Documentation (from `documentation.md`):**
- Guide section: "When I want to protect the token page from unauthorized access" — session JWT binding, custom header CORS protection, AuthPolicy as additional layer

**Verification:** `make test-unit && make test-e2e`

---

### Task 6: Router 401 invalidation (CONNLINK-999)

Depends on: Task 4.

**Files:**
- `internal/mcp-router/response_handlers.go` — modify `HandleResponseHeaders` to handle 401:
  1. If status is 401 and server has `TokenURLElicitation` config and `--enable-url-elicitation`:
     - Delete cached token via `SessionCache.DeleteUserToken`
     - Pass the 401 through to the client as-is
  2. On the client's next retry, the existing cache-miss path in `HandleToolCall` triggers `-32042` re-elicitation
- `internal/mcp-router/response_handlers_test.go` — unit tests

**Acceptance criteria:**
- [x] 401 from upstream with elicitation-configured server → token deleted, 401 passed through
- [x] Next retry after 401 → cache miss triggers -32042 re-elicitation
- [x] 401 from non-elicitation server → no change (existing behavior)
- [x] Feature flag disabled → 401 passed through as-is, no token deletion

**E2E test cases (from `e2e_test_cases.md`):**
- `[Happy,URLElicitation] 401 from upstream invalidates cached token and re-triggers elicitation`

**Documentation (from `documentation.md`):**
- Guide section: "When a user's token expires and they need to provide a new one" — 401 invalidation, JWT expiry detection, user experience
- Security Architecture Update (`docs/design/security-architecture.md`): token data flow, encryption at rest, session scoping, identity verification, known risks

**Verification:** `make test-unit && make test-e2e`

---

### Task 7: Documentation review and assembly (part of CONNLINK-998)

Documentation sections are written inline with their implementation tasks (Tasks 4, 5, 6). This task assembles the final `docs/guides/url-elicitation.md` from those sections and does a consistency pass.

**Files:**
- `docs/guides/url-elicitation.md` (new) — assemble from sections written in Tasks 4–6
- `docs/guides/README.md` — add entry for new guide
- `docs/reference/mcpserverregistration.md` — update API reference with `tokenURLElicitation` object, `url` field, relationship to `credentialRef`, examples

**Acceptance criteria:**
- [ ] Guide sections from Tasks 4–6 assembled into coherent guide
- [ ] Guide follows `docs/CLAUDE.md` conventions (numbered steps, prerequisites, next steps)
- [ ] API reference updated (done in Task 2)
- [ ] Security architecture updated (done in Task 6)
- [ ] Guide added to `docs/guides/README.md`

### Task 8: Remaining e2e test cases

Cross-cutting tests that span multiple components and are best added after Tasks 3–6 are complete.

**Files:**
- `tests/e2e/elicitation_test.go` — add remaining e2e test cases

**E2E test cases (from `e2e_test_cases.md`):**
- `[URLElicitation] Expired JWT token treated as cache miss`
- `[URLElicitation] Opaque token not subject to expiry check`

**Acceptance criteria:**
- [ ] Expired JWT stored in cache → next tool call deletes it, returns -32042
- [ ] Opaque PAT stored in cache → returned without expiry check, forwarded to upstream

**Verification:** `make test-e2e`

---

## Verification (full)

```bash
make generate-all    # CRD regeneration
make lint            # Style checks
make test-unit       # All unit tests
make test-e2e        # E2E with Kind cluster
```
