# Security Architecture

This document describes the security architecture of the MCP Gateway. It covers what data crosses each boundary, how authentication and session isolation work, and where responsibility lies.
## Scope

The MCP Gateway extends Envoy to provide routing and aggregation for MCP servers. The router (ext_proc) parses MCP JSON-RPC request bodies to extract the method and tool name for routing decisions, and rewrites the body to strip tool prefixes before Envoy does the final routing to upstream servers. However, it does not inspect or sanitize the semantic content of tool arguments, tool responses, prompts, or resources. The gateway operates at L7 and enforces policy — authentication, authorization, routing, and session management — based on request metadata (HTTP headers, JSON-RPC method, tool name), not on the semantic content of tool arguments or responses.

The security posture of an MCP Gateway deployment depends on the user combining the gateway with:

- Secure upstream MCP servers
- Kuadrant policies (AuthPolicy, RateLimitPolicy)
- Prompt guard tooling for payload-level protection

## Architecture Overview

```
MCP Client
    │
    │ Bearer Token (OAuth2/OIDC)
    ▼
┌──────────────────────────────────────────────┐
│  Envoy Gateway                               │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │  Kuadrant AuthPolicy (Authorino)       │  │
│  │  - JWT validation (Keycloak OIDC)      │  │
│  │  - Token exchange (RFC 8693)           │  │
│  │  - Tool-level RBAC                     │  │
│  │  - x-mcp-authorized header (signed)  │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │  MCP Router (ext_proc)                 │  │
│  │  (internal/mcp-router/)                │  │
│  │  - Parses JSON-RPC method/tool name    │  │
│  │  - Routes to broker or upstream server │  │
│  │  - Manages session-to-backend mapping  │  │
│  │  - Sets routing headers                │  │
│  └────────────────────────────────────────┘  │
│                                              │
└──────────┬──────────────────────┬────────────┘
           │                      │
           ▼                      ▼
   ┌──────────────────┐     ┌──────────────────┐
   │  MCP Broker      │     │  Upstream MCP    │
   │  (internal/      │     │  Servers         │
   │  broker/)        │     │  (tools/call)    │
   │  tools/list,     │     └──────────────────┘
   │  initialize      │
   └──────────────────┘
```

### Data crossing each boundary

| Boundary | Data forwarded |
|----------|---------------|
| Client → Gateway | Bearer token, MCP JSON-RPC body, custom headers |
| Gateway → Router (ext_proc) | Request headers, buffered body |
| Gateway → Upstream MCP Server | All client headers, MCP JSON-RPC body |
| Gateway → Broker | MCP JSON-RPC body, gateway session JWT, `x-mcp-authorized` signed header |
| Upstream MCP Server → Client | MCP JSON-RPC response body (streamed as-is), backend session IDs (rewritten to gateway session IDs) |

### Information shared with upstream MCP servers (model providers)

When an MCP server sits in front of an LLM or model provider, the following data reaches it:

- The full MCP JSON-RPC request body, including tool arguments
- All client headers, barring those specifically managed by the gateway (e.g., `mcp-session-id`, pseudo-headers). This includes the `Authorization` header — the original bearer token is forwarded unless token exchange is configured, in which case a scoped token replaces it

The gateway does not add user identity claims, PII, or conversation context beyond what the client includes in the MCP request body.

## Gateway Security Boundary

The gateway is responsible for:

- **Authentication**: validating client identity via JWT/OIDC via Kuadrant AuthPolicy
- **Authorization**: enforcing tool-level access control via Kuadrant AuthPolicy
- **Routing**: directing requests to the correct upstream server
- **Session isolation**: ensuring one client's backend sessions are not accessible to another
- **Credential isolation**: backend API keys and service account tokens provided to the broker via secrets are not exposed to or used by clients of the gateway

The gateway is NOT responsible for:

- **Payload content validation**: tool arguments and outputs pass through without semantic validation or sanitization (the router only inspects the JSON-RPC method and tool name for routing)
- **Prompt injection detection**: the gateway does not inspect MCP tool arguments for injection patterns
- **Content filtering**: responses from upstream servers are streamed back as-is
- **Upstream server security**: backend MCP servers must implement their own input validation or leverage prompt guard tooling integrated independently at the gateway

### Security properties

- The router (`internal/mcp-router/`) never accesses `credentialRef` values; it only reads routing headers and the JSON-RPC method/tool name from the request body
- The broker (`internal/broker/`) never writes `credentialRef` values to client-facing responses, logs, or headers forwarded through Envoy
- The controller (`internal/controller/`) scopes generated Secrets to the operator namespace and sets owner references for garbage collection. The controller's ClusterRole grants cluster-wide secrets access, but the informer cache is filtered by label (`mcp.kuadrant.io/secret: "true"`) to limit the working set
- Routing headers set by the router (tool name, session ID, server name) are derived from parsed JSON-RPC bodies or gateway-issued JWTs, not from raw client input. Client-supplied headers are proxied through to upstream backends as-is — header filtering is the responsibility of the gateway HTTPRoute configuration and AuthPolicy
- The router strips or rewrites gateway-internal headers (`mcp-session-id`, `mcp-init-host`, `router-key`) before traffic reaches upstream backends

## Authentication and Authorization

Authentication and authorization are enforced via [Kuadrant AuthPolicy](https://docs.kuadrant.io/1.2.x/kuadrant-operator/doc/reference/authpolicy/) backed by Authorino, applied at the Gateway HTTPRoute level. See [auth-phase-1.md](auth-phase-1.md) and [auth-phase-2.md](auth-phase-2.md) for detailed design.

### Client authentication

Clients authenticate via OAuth2/OIDC. The gateway serves MCP-spec resource metadata at `/.well-known/oauth-protected-resource/mcp`, pointing clients to the authorization server. JWT tokens are validated against the OIDC provider.

### Token exchange

The client's Authorization header is always forwarded to upstream servers, allowing them to authenticate and authorize access as needed. As a best practice, Authorino can be configured to perform OAuth2 token exchange (RFC 8693) as part of the AuthPolicy pipeline, replacing the client's token with one of reduced scope and specific audience via a confidential client. This limits the blast radius if a backend server is compromised. The gateway components themselves contain no token exchange logic — it is entirely handled by Authorino.

### Tool-level RBAC

AuthPolicy can enforce per-tool access control using the `x-mcp-toolname` and `x-mcp-servername` headers set by the router. JWT claims (e.g., `resource_access`) are matched against the requested tool to produce an `x-mcp-authorized` JWT signed header via Authorino. The broker verifies this signature and filters tool lists accordingly.

### Security properties

- Client identity validation (JWT/OIDC) and token exchange (RFC 8693) are delegated to Authorino via AuthPolicy — the gateway contains no custom logic for these flows. URL token elicitation (`internal/broker/`, `internal/elicitation/`) is a separate path where the gateway stores and injects per-user tokens for upstream requests (see [URL Token Elicitation](#url-token-elicitation))
- The `x-mcp-authorized` header is a signed JWT produced by Authorino, verified by the broker (`internal/broker/`) using an ECDSA public key; unsigned or tampered values are rejected
- The router sets `x-mcp-toolname` and `x-mcp-servername` from the parsed JSON-RPC body before AuthPolicy evaluation — these values drive authorization decisions and must not be settable by the client

## Session Isolation

The MCP protocol is stateful. The gateway manages three session types to prevent cross-user data leakage. See [session-mgmt.md](session-mgmt.md) for the full design.

- **Gateway session**: a signed JWT issued by the broker on `initialize`, unique per client. Used as the key for backend mcp server session lookups.
- **Client-backend session**: a separate MCP session negotiated per client per upstream server. Cached by gateway session ID + server name. One client cannot access another client's backend sessions.
- **Broker session**: a long-lived service account session between the broker and each upstream server, used only for tool discovery and notifications — not used during client interactions or exposed to the client in any way.

Gateway sessions expire (default 24 hours), and all associated backend sessions are closed on expiry. Session IDs from backends are never exposed to clients — the router rewrites them to the gateway session ID.

### Security properties

- Gateway session JWTs (`internal/jwt/`) are signed with the `GATEWAY_SIGNING_KEY` HMAC key; the broker and router refuse to start if the key is absent
- Backend session IDs are never exposed to clients — the router (`internal/mcp-router/response_handlers.go`) rewrites `mcp-session-id` headers in responses to the gateway session ID
- Session-to-backend mappings (`internal/session/`) are keyed by gateway session ID + server name; one client cannot address another client's backend session
- All backend sessions associated with a gateway session are closed on session expiry

## URL Token Elicitation

URL elicitation (`tokenURLElicitation`) enables per-user token collection at tool-call time (see `internal/broker/elicitation_handler.go`, `internal/elicitation/`). An AuthPolicy on the gateway route is required to ensure per-user token binding — without one, the token page has no identity check. The router (`internal/mcp-router/elicitation.go`) triggers the MCP spec's `-32042 URLElicitationRequired` flow for elicitation-capable clients.

### Token data boundaries

| Boundary | Data | Isolation |
|----------|------|-----------|
| Client → Gateway (token page) | User-submitted token via HTML form POST | Bound to a single-use elicitation ID tied to the gateway session |
| Gateway session cache | Token stored by session ID + server name | Per-session, per-server. One client cannot access another client's cached token |
| Router → Upstream | Cached token injected as `Authorization` header | Only injected for the specific server that triggered elicitation |

### Separation from `credentialRef`

`credentialRef` and `tokenURLElicitation` serve distinct purposes and never overlap:

- `credentialRef` is used exclusively by the broker for tool discovery and upstream session management. It is never injected into client `tools/call` requests.
- `tokenURLElicitation` collects per-user tokens at tool-call time via the router. These tokens are cached per session and injected by the router into `tools/call` requests.

### Encryption at rest

User tokens in the session cache (added via URL elicitation) are encrypted when using an external cache backend (Redis). Encryption uses AES-256-GCM with keys derived from the session signing key via HKDF (RFC 5869). The in-memory backend stores tokens in plaintext since the data is process-local.

### CSRF protection

The token page uses a cookie-based CSRF token to prevent cross-site form submissions. On GET, the broker generates a random token, sets it as an `HttpOnly` cookie, and includes a matching hidden field in the HTML form. On POST, the broker validates that the cookie value and form field match using constant-time comparison.

### Security properties

- **Single-use elicitation IDs**: each elicitation entry is consumed atomically on first use (`Claim` semantics), preventing replay.
- **Identity verification (`sub` claim)**: the broker compares the `sub` claim from the browser request's JWT with the `sub` stored in the elicitation entry. The broker implicitly trusts that the JWT has been verified by the AuthPolicy — it does not repeat signature or expiry validation.
- **No credential leakage**: the broker's `credentialRef` is never exposed to clients or injected into the `tools/call` path.
- **Token page served over gateway**: the built-in `/tokens` page is served by the broker behind Envoy, so gateway-level policies (TLS, rate limiting) apply. External URL overrides delegate security to the external service.

### Known risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Cached tokens persist for the gateway session lifetime | Low | Tokens are deleted on session expiry; 401 invalidation planned ([#830](https://github.com/Kuadrant/mcp-gateway/issues/830)) |

## Prompt Injection and Context Pollution

### Gateway position

The router parses MCP JSON-RPC bodies to extract the method and tool name for routing, but tool arguments, tool responses, and any embedded prompts pass through without semantic inspection. The gateway cannot distinguish between legitimate tool arguments and injected prompts. The Gateway doesn't directly integrate with agents/models. It is the users responsibility to configure a prompt guard capability that works with the Gateway to intercept and semantically evaluate the payload.


### Context pollution

Context pollution — where untrusted data from one tool call influences subsequent LLM reasoning — is similarly outside the gateway's scope. The gateway ensures session isolation (one user's sessions are not shared with another), but it does not control how an LLM client aggregates tool responses into its context window.

### Security properties

- The router (`internal/mcp-router/`) parses JSON-RPC request bodies to extract `method`, `params.name`, and elicitation `result.action` for routing decisions, and rewrites the body to strip tool/prompt prefixes. It also inspects response headers (`:status`, `mcp-session-id`) to manage session lifecycle (404 invalidation, 401 token eviction, session ID rewriting). The SSE rewriter (`internal/mcp-router/elicitation.go`) additionally parses response bodies to rewrite backend elicitation IDs to gateway-scoped IDs. Beyond these specific transforms, the router does not inspect or transform tool arguments, tool response content, or prompt payloads
- No component in the gateway inspects MCP tool arguments for injection patterns; this is an explicit non-goal documented above
- Session isolation prevents cross-user context pollution at the transport layer, but the gateway does not control how clients aggregate tool responses

### Recommendations

- Deploy a prompt guard (e.g., input/output scanning) as an additional plugin to Envoy, to inspect MCP payloads before they reach tool servers or return to clients
- Upstream MCP servers should also validate and sanitize their own inputs

## Known Risks and Mitigations

| Risk | Severity | Current status | Mitigation |
|------|----------|---------------|------------|
| No payload-level prompt injection detection | Medium | By design — gateway for tool calls is just a routing layer | Use prompt guard tooling alongside the gateway |
| Tool responses streamed as-is from backends | Medium | By design | Upstream servers must validate outputs; prompt guards can inspect, reject, modify responses |
| Client custom headers forwarded to backends | Low | `mcp-session-id` replaced with specific target mcp backend session; `mcp-init-host` and `router-key` are stripped by the router before traffic reaches a backend | Review header forwarding policy if backends are untrusted |
| No response size limits on SSE streams | Low | Not implemented | Envoy buffer limits and timeouts provide partial mitigation |
| JWT signing key absent | High | Mitigated | The broker/router refuses to start if `GATEWAY_SIGNING_KEY` is not set. In controller-managed deployments the controller generates a 32-byte random key and injects it via `env.valueFrom.secretKeyRef` ([#714](https://github.com/Kuadrant/mcp-gateway/issues/714)). |
| Hairpin backend-init request bypass | Critical | Mitigated (GHSA-g53w-w6mj-hrpp) | The router only accepts an `mcp-init-host` rewrite when accompanied by a short-lived JWT (30s, `aud=mcp-router`, `purpose=backend-init`, host-bound, `jti`) signed by the gateway's HMAC session signing key. The static `--mcp-router-key` flag and `MCP_ROUTER_API_KEY` env var have been removed. |
| MCPVirtualServer only hides tools from listing, does not prevent calling | Medium | By design — virtual servers filter tools/list but authorized clients can still call any tool directly | Use AuthPolicy with tool-level RBAC to enforce access control; virtual servers control visibility, not authorization. Document this clearly in virtual server docs |
| Client OAuth tokens forwarded to backends without token exchange | Medium | By design — all headers forwarded | Configure token exchange via AuthPolicy to replace client tokens with scoped tokens |
| TLS configurations are not included in examples | Low | Relies on infrastructure layer | Deploy behind Istio or configure TLS on Gateway listeners for production |

## Recommendations for Secure Deployment

1. **Use Kuadrant AuthPolicy** on all gateway routes — do not expose the gateway without authentication
2. **Configure token exchange** to avoid forwarding client tokens to upstream servers
3. **Use prompt guard tooling** to inspect MCP payloads for injection and content policy violations
4. **Secure upstream MCP servers** — the gateway enforces transport policy, but backends must validate their own inputs
5. **Enable TLS** on Gateway listeners and ensure backend communication is encrypted
7. **Deploy Redis Compatible Data Store** for session caching in multi-replica deployments to maintain session isolation across instances
8. **Apply RateLimitPolicy** via Kuadrant to protect against abuse

## Infrastructure Deduplication

The MCP Gateway reuses existing infrastructure rather than duplicating it:

- **Authentication/Authorization**: delegates to Kuadrant AuthPolicy (Authorino) — no custom auth implementation
- **Traffic management**: uses Envoy via Gateway API — no custom proxy
- **Service discovery**: uses Kubernetes Gateway API (HTTPRoute) — no custom service registry
- **External service routing**: uses Istio ServiceEntry/DestinationRule — no custom DNS or routing
- **Secret storage**: uses Kubernetes secrets — no custom vault
- **TLS**: delegates to infrastructure layer (Istio, cert-manager) — no custom certificate management
