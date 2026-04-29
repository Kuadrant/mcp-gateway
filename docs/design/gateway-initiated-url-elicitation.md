# Gateway-Initiated URL Elicitation for Per-User Credentials

## Problem

Many upstream MCP servers require per-user credentials. Example: a user's own GitHub PAT, not a shared service account token. The gateway currently supports several per-user credential strategies:

1. **Header-based token replacement** — the MCP client sends the user's credential in a custom header and the gateway maps it to the upstream `Authorization` header. The credential passes through the MCP client, making it visible to the LLM context and client-side logging. The MCP specification explicitly [prohibits token passthrough](https://modelcontextprotocol.io/specification/2025-11-25/basic/security_best_practices#token-passthrough) for this reason.
2. **Token exchange via OAuth provider** — requires the OAuth provider to support token exchange and be configured per upstream. Requires third-party identity federation.
3. **Vault integration** — requires a Vault instance exposed to external users for credential provisioning.

URL elicitation complements these strategies by offering a server-side credential collection path that doesn't require exposing infrastructure like Vault to external users and keeps credentials out of the MCP client context and LLM context entirely.

## Summary

Enable the MCP Gateway to dynamically request per-user credentials at client tool-call/backend MCP call time if required using [URL mode elicitation](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation). The router detects a missing credential and returns a `URLElicitationRequiredError`. The client directs the user to a broker-hosted credential page. The credential is cached per session and re-elicited on upstream 401.

## Goals

- Per-user credential acquisition without exposing credentials to the client configuration
- Protocol compliant as per [URLElicitationRequiredError flow](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation#url-mode-with-elicitation-required-error-flow) from the MCP specification
- Cache credentials encrypted in the shared session cache (Redis / in-memory)
- Invalidate cached credentials on upstream 401 to trigger re-elicitation
- Maintain capability of using OIDC authentication on the main broker gateway route

## Non-Goals

- Replace `credentialRef` (still used by the broker for tool discovery)
- Form mode elicitation for credentials (prohibited by the MCP spec for sensitive data)
- Full OAuth client in the broker

## Prerequisites

- MCP client must declare `elicitation.url` capability during the [initialize handshake](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation#capabilities) (MCP spec 2025-11-25). Clients without this capability can still use elicitation-configured servers — the router uses the `Authorization` header as-is and returns standard errors on 401 instead of triggering elicitation (see [Non-Interactive Agents](#non-interactive-agents-service-accounts)).
- MCP Gateway accessible over HTTPS for the credential page


## Design

### Flow

```mermaid
sequenceDiagram
    participant User
    participant Browser
    participant Client as MCP Client
    participant Gateway as Envoy
    participant Router as MCP Router (ext_proc)
    participant Broker as MCP Broker
    participant Cache as Session Cache
    participant Upstream as Upstream MCP Server

    Client->>Gateway: POST /mcp tools/call (github_get_me)
    Gateway->>Router: ext_proc request
    Router->>Router: Check Authorization header → none
    Router->>Router: Check client elicitation.url capability → supported
    Router->>Cache: GetUserCredential(sessionID, "github")
    Cache-->>Router: empty (no credential)
    Router-->>Client: URLElicitationRequiredError (-32042)
    Note over Router,Client: includes URL to broker credential page

    Client->>User: Show credential page URL, ask consent
    User->>Client: Consent
    Client->>Browser: Open credential page URL

    Browser->>Broker: GET /credentials?server=github&elicitation_id=...
    Broker-->>Browser: Render credential form
    User->>Browser: Enter GitHub PAT
    Browser->>Broker: POST /credentials (PAT)
    Broker->>Cache: SetUserCredential(sessionID, "github", PAT)
    Broker-->>Browser: Success page

    Note over Client: User closes browser, retries
    Client->>Gateway: POST /mcp tools/call (github_get_me) [retry]
    Gateway->>Router: ext_proc request
    Router->>Router: Check Authorization header → none
    Router->>Cache: GetUserCredential(sessionID, "github")
    Cache-->>Router: PAT
    Router->>Router: Set Authorization: Bearer PAT
    Router-->>Gateway: Route to upstream
    Gateway->>Upstream: POST /mcp tools/call (with PAT)
    Upstream-->>Client: Tool result
```

### Credential Invalidation on 401

```mermaid
sequenceDiagram
    participant Client as MCP Client
    participant Gateway as Envoy
    participant Router as MCP Router (ext_proc)
    participant Cache as Session Cache
    participant Upstream as Upstream MCP Server

    Client->>Gateway: POST /mcp tools/call
    Gateway->>Router: ext_proc request
    Router->>Router: Check Authorization header → none
    Router->>Cache: GetUserCredential → expired PAT
    Router->>Router: Set Authorization: Bearer PAT
    Router-->>Gateway: Route to upstream
    Gateway->>Upstream: POST /mcp tools/call (with expired PAT)
    Upstream-->>Gateway: 401 Unauthorized
    Gateway->>Router: ext_proc response (401)
    Router->>Cache: DeleteUserCredential(sessionID, "github")
    Router-->>Client: URLElicitationRequiredError (-32042)
    Note over Client,Router: Client prompts user to re-enter credential
```

### Component Responsibilities

| Component | Role |
|-----------|------|
| **Router** | (1) If `Authorization` header present, use as-is for upstream routing. (2) If absent, check cache — inject cached credential on hit. (3) On cache miss, return `-32042` if client declares `elicitation.url` capability, otherwise return standard error. (4) On upstream 401, invalidate cached credential and re-elicit or error per client capability. |
| **Broker** | Hosts `/credentials` page, writes credential to cache |
| **Cache** | Shared storage for per-user, per-server credentials |
| **Controller** | Propagates `credentialURLElicitation` from CRD to config |

### API Changes

#### MCPServerRegistration

New optional object `credentialURLElicitation`. When present, it signals that this server requires per-user credentials and that the router should use the URL elicitation flow to collect them from capable clients.

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: github
  namespace: mcp-test
spec:
  toolPrefix: github_
  targetRef:
    kind: HTTPRoute
    name: github-mcp-external
  credentialRef:                  # broker-only: used for tool discovery
    name: github-token
    key: token
  credentialURLElicitation: {}    # enables per-user credential collection
```

`credentialRef` and `credentialURLElicitation` serve different purposes: `credentialRef` gives the broker a credential for tool discovery, while `credentialURLElicitation` enables per-user credential collection at tool-call time.

When present, the router checks the session cache for a per-user credential before routing tool calls. On cache miss, it returns `URLElicitationRequiredError` with a URL to the broker's credential page (if the client declares `elicitation.url` capability).

| Field | Type | Description |
|-------|------|-------------|
| `url` | string | Optional. Overrides the default broker credential page URL. Allows operators to direct users to an external UI (e.g., Vault web UI). |

Example with external URL:

```yaml
spec:
  credentialURLElicitation:
    url: "https://vault.example.com/ui/vault/secrets/mcp/create"
```

When no `url` is set, the router generates a URL pointing to the broker's built-in credential page. In the future, if OAuth fields are added (client ID, authorize endpoint, etc.), their presence on the object will imply an OAuth flow.

#### Config Type

`MCPServer` in `internal/config/types.go` gains:
- `CredentialURLElicitation *CredentialURLElicitationConfig` (optional, nil means no elicitation)

```go
type CredentialURLElicitationConfig struct {
    URL string `json:"url,omitempty"`
}
```

### Credential Delivery Patterns

The elicitation URL determines how the credential reaches the upstream request. Two patterns are supported:

#### Pattern 1: Broker Credential Page (default)

When no `credentialURLElicitation.url` is set, the router generates a URL pointing to the broker's `/credentials` page. The user enters a credential on the broker page, the broker writes it to the session cache, and the router reads from cache on retry to inject the `Authorization` header.

```text
Router → -32042 (broker URL) → User enters PAT → Broker stores in cache → Router reads cache → sets header
```

The router is responsible for credential injection.

#### Pattern 2: External UI with AuthPolicy

When `credentialURLElicitation.url` is set to an external UI (e.g., Vault web UI), the user stores their credential there directly. An AuthPolicy on the upstream HTTPRoute reads the credential from the external store and injects it into the `Authorization` header. The router does not need to read from cache — it only needs to detect whether a credential is missing (upstream 401) and re-trigger elicitation.

```text
Router → -32042 (external URL) → User stores PAT in Vault → AuthPolicy reads from Vault → sets header
```

AuthPolicy handles credential injection. The router's role simplifies to:
1. If `credentialURLElicitation` is set and the upstream returns 401, return `-32042` with the configured URL
2. No cache read/write needed for this server

This pattern is useful when operators already have credential infrastructure (e.g., Vault) and want to avoid duplicating storage in the session cache.

> **Note:** Unlike Pattern 1, there is no completion callback from the external UI, so `notifications/elicitation/complete` cannot be sent. The client retries and either succeeds or gets another 401.

### Credential Storage

Per-user credentials are written by the broker and read by the router. The storage backend is abstracted behind an interface.

| Backend | Description |
|---------|-------------|
| **Session cache** (Redis / in-memory) | Initial implementation. Credentials are session-scoped and lost on session expiry or cache eviction. |
| **Vault** | Stores credentials in Vault keyed by user identity. Provides encrypted storage, audit logging, and credential lifecycle management. See [Vault integration](../guides/vault-integration.md). |

#### Encryption at Rest

When an external cache is configured, credentials are encrypted using AES-GCM before storage. The encryption key is derived from the existing session signing key (`--mcp-session-signing-key`) using HKDF (HMAC-based Key Derivation Function, [RFC 5869](https://datatracker.ietf.org/doc/html/rfc5869)), so no additional configuration is required. HKDF derives a cryptographically strong key using a context-specific salt, ensuring the encryption key is distinct from the signing key even though both originate from the same secret.

Encryption is only applied when using an external cache store as the storage backend — it protects credentials in an external store that may be shared or persisted to disk. For the in-memory backend, encryption adds no value since a process memory dump would reveal the encryption key alongside the ciphertext and to be used to call a backend the token credential has to be in plain text in memory.

#### Cache Schema

User credentials are stored as fields on the existing gateway session hash, using the prefix `usercred:` to distinguish them from upstream session IDs.

| Operation | Key | Field | Value |
|-----------|-----|-------|-------|
| Set | `jwt-abc-123` | `usercred:github` | AES-GCM encrypted credential |
| Get | `jwt-abc-123` | `usercred:github` | AES-GCM encrypted credential |
| Delete (on 401) | `jwt-abc-123` | `usercred:github` | — |
| Delete (on session invalidation) | `jwt-abc-123` | `usercred:github` | — |


### URLElicitationRequiredError Response

Returned as an SSE-formatted immediate response (HTTP 200, `text/event-stream`). The `url` field uses `credentialURLElicitation.url` from the server config if set, otherwise defaults to the broker's `/credentials` page:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32042,
    "message": "User credential required for github",
    "data": {
      "elicitations": [
        {
          "mode": "url",
          "elicitationId": "<sessionID>:<serverName>",
          "url": "https://<gateway-host>/credentials?server=github&elicitation_id=<id>",
          "message": "Please provide your credential for github"
        }
      ]
    }
  }
}
```

### Credential Page

The broker serves a simple HTML form at `/credentials`:

- **GET** `/credentials?server=<name>&elicitation_id=<id>` — renders credential entry form
- **POST** `/credentials` — stores credential in cache, keyed by session and server

## Security Considerations

### Credential Page Must Be Auth-Protected

The `/credentials` endpoint must be behind the gateway route's OAuth/OIDC policy. Without authentication, an attacker with a valid or guessed elicitation ID could brute-force inject a malicious credential into another user's session. The OIDC token from the authenticated request also provides the identity needed to bind the credential to the correct session.

### Identity Verification

The MCP specification [warns about phishing attacks](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation#phishing) where an attacker could trick another user into completing an elicitation on their behalf. The credential page must verify that the user opening the URL is the same user who triggered the elicitation.

Identity verification is handled by the AuthPolicy attached to the gateway route where the credential page is served. The AuthPolicy validates the OIDC token from the request, ensuring the user opening the URL is the same user who triggered the elicitation.

### Non-Interactive Agents (Service Accounts)

Non-interactive agents (CI/CD pipelines, automated MCP clients, agent-to-agent calls) cannot complete browser-based elicitation or OAuth flows. No special configuration is needed — the router uses the client's initialize handshake to determine behavior.

If the client does not declare `elicitation.url` in its capabilities, the router never returns `URLElicitationRequiredError` (-32042). Instead:

1. The `Authorization` header from the request is used as-is for upstream routing
2. If the upstream returns 401, the router returns a standard error

If the upstream MCP server shares the same identity provider as the gateway, only one credential is needed — the gateway's `Authorization` header is valid for both. When the upstream expects a different credential, an AuthPolicy on the MCP's route reads the credential from an additional header or external store (e.g., Vault) and sets the `Authorization` header before the request reaches the upstream.

## Relationship to Existing Approaches

| Approach | When to Use |
|----------|-------------|
| **credentialRef** (static secret) | Broker-only credential for tool discovery and caching |
| **Header-based token replacement** ([guide](../guides/external-mcp-server-with-token-replacement.md)) | Client supports custom headers, simple setup |
| **Vault token exchange** ([guide](../guides/vault-token-exchange.md)) | Centralized credential management, admin-provisioned per-user secrets |
| **URL elicitation + broker page** (this design) | Self-service per-user credentials, no client configuration, no external infrastructure |
| **URL elicitation + external UI** (this design) | Self-service per-user credentials with existing credential infrastructure (e.g., Vault), AuthPolicy handles injection |

## Future Considerations

### OAuth Callback via Credential Page

The credential page could initiate an OAuth flow instead of rendering a form. The router would construct the OAuth authorize URL dynamically, encoding the elicitation ID in the OAuth `state` parameter. After the user consents, the provider redirects back to a gateway callback endpoint with the authorization code and `state`. The broker extracts the elicitation ID from `state`, exchanges the code for a token, and stores it in the session cache.

This would add OAuth fields to the `credentialURLElicitation` object (client ID, authorize endpoint, scopes, plus a referenced secret for the client secret). Their presence implies an OAuth flow. The router would compute the authorize URL per-elicitation rather than using the stored `url` verbatim.

The MCP spec [calls this out explicitly](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation#url-mode-elicitation-for-oauth-flows) as a primary use case for URL mode elicitation. The existing abstractions (storage interface, credential page, elicitation ID) would support this without major structural changes.

## Execution

### Todo

- [ ] Add `CredentialURLElicitation *CredentialURLElicitationConfig` field to `MCPServer` config type
- [ ] Add `credentialURLElicitation` object to MCPServerRegistration CRD and regenerate
- [ ] Add credential storage interface with cache implementation (set/get/delete + AES-GCM encryption)
- [ ] Router: credential resolution in `HandleToolCall` — (1) use existing `Authorization` header if present, (2) check cache and inject on hit, (3) return `-32042` on miss if client declares `elicitation.url` capability, otherwise return standard error
- [ ] Router: invalidate cached credential on upstream 401 in `HandleResponseHeaders`, return `-32042` if client supports elicitation, otherwise standard error
- [ ] Broker: implement `/credentials` page handler (GET form + POST storage)
- [ ] Register `/credentials` endpoint in `cmd/mcp-broker-router/main.go`
- [ ] Controller: propagate `credentialURLElicitation` from CRD to config
- [ ] Document that operators must configure AuthPolicy on the credential page route (identity verification / anti-phishing)
- [ ] `notifications/elicitation/complete` — broker notifies client after credential stored
- [ ] Vault storage backend
- [ ] E2E test with a test server that validates per-user credentials
- [ ] Update API reference docs for `credentialURLElicitation` field
- [ ] User-facing guide for configuring URL mode elicitation
- [ ] Update security-architecture.md (data boundaries, credential isolation, known risks)

### Completed
