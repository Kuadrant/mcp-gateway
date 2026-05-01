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

### Task 1: Feature flag `--enable-elicitation` (part of CONNLINK-997)

Add the flag early so all subsequent work is gated behind it.

**Files:**
- `cmd/mcp-broker-router/main.go` — add `--enable-elicitation` flag (default: false), pass to ExtProcServer and Broker
- `internal/mcp-router/server.go` — add `ElicitationEnabled bool` field to `ExtProcServer`
- `internal/broker/broker.go` — add `ElicitationEnabled bool` field

**Acceptance criteria:**
- [ ] Flag parsed and plumbed to router and broker
- [ ] When disabled, no elicitation behavior changes (verified by existing tests passing)

**Verification:** `make test-unit` passes, `--help` shows the flag

---

### Task 2: CRD + config types (CONNLINK-995)

**Files:**
- `api/v1alpha1/types.go` — add `CredentialURLElicitation *CredentialURLElicitationConfig` to `MCPServerRegistrationSpec`
- `api/v1alpha1/types.go` — add `CredentialURLElicitationConfig` struct with `URL string`
- `internal/config/types.go` — add `CredentialURLElicitation *CredentialURLElicitationConfig` to `MCPServer`
- `internal/config/types.go` — add `CredentialURLElicitationConfig` struct
- `internal/controller/mcpserverregistration_controller.go:386` — propagate field from CRD to config
- Run `make generate-all` to regenerate deepcopy, CRDs, sync Helm
- `docs/reference/mcpserverregistration.md` — update API reference

**Acceptance criteria:**
- [ ] CRD accepts `credentialURLElicitation` with optional `url` field
- [ ] Controller propagates to config Secret
- [ ] Unit test: controller includes elicitation config when set, omits when not set

**Verification:** `make generate-all && make lint && make test-unit`

---

### Task 3: User token cache with encryption (CONNLINK-996)

**Files:**
- `internal/mcp-router/user_token_cache.go` (new) — `UserTokenCache` interface:
  ```go
  type UserTokenCache interface {
      SetUserToken(ctx context.Context, sessionID, serverName, token string) error
      GetUserToken(ctx context.Context, sessionID, serverName string) (string, bool, error)
      DeleteUserToken(ctx context.Context, sessionID, serverName string) error
  }
  ```
- `internal/mcp-router/user_token_cache_memory.go` (new) — in-memory implementation (no encryption)
- `internal/mcp-router/user_token_cache_redis.go` (new) — redis protocol compliant backend with AES-GCM encryption
  - Store as `usercred:<serverName>` field on session hash
  - Encryption key derived from session signing key via HKDF (RFC 5869)
  - Reuse existing Redis connection from session cache
- `internal/mcp-router/user_token_cache_test.go` (new) — unit tests for both backends

**JWT Expiry Check:**
On `GetUserToken`, attempt to parse the token as a JWT (three dot-separated base64url segments). If it parses successfully, check the `exp` claim — if expired, delete the token and return a cache miss. If the token doesn't parse as a JWT (e.g. opaque PAT like a GitHub token), skip expiry checking and return it as-is for upstream use.

**Acceptance criteria:**
- [ ] Set/get/delete works for both backends
- [ ] Redis protocol compliant backend encrypts values (not plaintext in store)
- [ ] In-memory backend stores plaintext (no encryption overhead)
- [ ] Token deleted when session hash is deleted
- [ ] JWT tokens checked for expiry on get — expired tokens deleted and treated as cache miss
- [ ] Non-JWT tokens (opaque PATs) returned as-is without expiry check

**Verification:** `make test-unit` — tests cover encrypt/decrypt round-trip, set/get/delete, missing key returns false, expired JWT returns miss, opaque token returned without expiry check

---

### Task 4: Broker token page (CONNLINK-998)

**Files:**
- `internal/broker/credentials.go` (new) — HTTP handler for token page
  - `GET /credentials?server=<name>&elicitation_id=<id>` — renders HTML form
  - `POST /credentials` — stores token in cache, returns success
- `internal/broker/credentials_test.go` (new) — unit tests
- `cmd/mcp-broker-router/main.go` — register `/credentials` endpoint (gated behind `--enable-elicitation`)
- `internal/broker/broker.go` — broker needs access to `UserTokenCache`

**Acceptance criteria:**
- [ ] GET renders form showing server name
- [ ] POST verifies the session JWT on the request matches the session ID in the elicitation ID before storing
- [ ] POST stores token in cache keyed by session ID and server name
- [ ] Invalid/missing elicitation_id returns error
- [ ] Session mismatch between request JWT and elicitation ID returns error
- [ ] Endpoint only registered when `--enable-elicitation` is true
- [ ] E2E: hit `/credentials` endpoint, store a token, verify it's retrievable from cache

**Verification:** `make test-unit && make test-e2e`

---

### Task 5: Router token resolution + elicitation trigger (CONNLINK-997)

**Files:**
- `internal/mcp-router/request_handlers.go` — modify `HandleToolCall` to add token resolution:
  1. Check if server has `CredentialURLElicitation` config — if not, skip (existing behavior)
  2. If `--enable-elicitation` is false, skip (existing behavior)
  3. Check `Authorization` header from client request — if present, use as-is (no token injection needed)
  4. Check `UserTokenCache.GetUserToken(sessionID, serverName)` — if hit, inject via `headers.WithAuth()`
  5. On cache miss, check client elicitation capability via `SessionCache.GetClientElicitation(ctx, gatewaySessionID)` (already stored during initialize handshake in `HandleResponseHeaders`)
  6. If client supports elicitation → return `-32042` SSE immediate response with token page URL
  7. If client does not support elicitation → return standard error (non-interactive agent path)
- `internal/mcp-router/request_handlers.go` — add helper to build `-32042` response with URL (broker page or external URL from config)
- `internal/mcp-router/request_handlers_test.go` — unit tests for each path
- `internal/mcp-router/server.go` — add `UserTokenCache` field to `ExtProcServer`

**Acceptance criteria:**
- [ ] Existing Authorization header used as-is (no regression)
- [ ] Cached token injected on hit
- [ ] -32042 returned on miss with elicitation-capable client
- [ ] Standard error returned on miss without capability
- [ ] Feature flag disabled → existing behavior unchanged
- [ ] URL in -32042 uses external URL when `credentialURLElicitation.url` is set
- [ ] E2E: full flow — call without token → get -32042 → provide token via page → retry succeeds

**Verification:** `make test-unit && make test-e2e`

---

### Task 6: Router 401 invalidation + re-elicitation (CONNLINK-999)

**Files:**
- `internal/mcp-router/response_handlers.go` — modify `HandleResponseHeaders` to handle 401:
  1. If status is 401 and server has `CredentialURLElicitation` config and `--enable-elicitation`:
     - Delete cached token via `UserTokenCache.DeleteUserToken`
     - Check client capability via `SessionCache.GetClientElicitation(ctx, gatewaySessionID)` (same flow as Task 5)
     - If client supports elicitation → return `-32042` immediate response (reuse helper from Task 5)
     - If not → pass 401 through as-is
- `internal/mcp-router/response_handlers_test.go` — unit tests

**Acceptance criteria:**
- [ ] 401 from upstream with elicitation-configured server → token deleted + -32042 returned
- [ ] 401 without elicitation capability → standard 401 pass-through
- [ ] 401 from non-elicitation server → no change (existing behavior)
- [ ] Feature flag disabled → 401 passed through as-is
- [ ] E2E: cached token is invalid → upstream returns 401 → token deleted → re-elicitation triggered

**Verification:** `make test-unit && make test-e2e`

---

### Task 7: Documentation (part of CONNLINK-998)

**Files:**
- `docs/guides/url-elicitation.md` (new) — user-facing guide
- `docs/design/security-architecture.md` — update with token data boundaries
- `docs/reference/mcpserverregistration.md` — update API reference for `credentialURLElicitation` field

**Acceptance criteria:**
- [ ] Guide covers both broker page and external URL patterns
- [ ] Security doc covers token isolation and known risks
- [ ] API reference updated

## Verification (full)

```bash
make generate-all    # CRD regeneration
make lint            # Style checks
make test-unit       # All unit tests
make test-e2e        # E2E with Kind cluster
```
