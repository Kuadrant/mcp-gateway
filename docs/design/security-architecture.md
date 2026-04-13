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
│  │  - x-authorized-tools header (signed)  │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │  MCP Router (ext_proc)                 │  │
│  │  - Parses JSON-RPC method/tool name    │  │
│  │  - Routes to broker or upstream server │  │
│  │  - Manages session-to-backend mapping  │  │
│  │  - Sets routing headers                │  │
│  └────────────────────────────────────────┘  │
│                                              │
└──────────┬──────────────────────┬────────────┘
           │                      │
           ▼                      ▼
   ┌──────────────┐     ┌──────────────────┐
   │  MCP Broker  │     │  Upstream MCP    │
   │  (tools/list,│     │  Servers         │
   │  initialize) │     │  (tools/call)    │
   └──────────────┘     └──────────────────┘
```

### Data crossing each boundary

| Boundary | Data forwarded |
|----------|---------------|
| Client → Gateway | Bearer token, MCP JSON-RPC body, custom headers |
| Gateway → Router (ext_proc) | Request headers, buffered body |
| Gateway → Upstream MCP Server | All client headers, MCP JSON-RPC body |
| Gateway → Broker | MCP JSON-RPC body, gateway session JWT, `x-authorized-tools` signed header |
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

## Authentication and Authorization

Authentication and authorization are enforced via [Kuadrant AuthPolicy](https://docs.kuadrant.io/1.2.x/kuadrant-operator/doc/reference/authpolicy/) backed by Authorino, applied at the Gateway HTTPRoute level. See [auth-phase-1.md](auth-phase-1.md) and [auth-phase-2.md](auth-phase-2.md) for detailed design.

### Client authentication

Clients authenticate via OAuth2/OIDC. The gateway serves MCP-spec resource metadata at `/.well-known/oauth-protected-resource/mcp`, pointing clients to the authorization server. JWT tokens are validated against the OIDC provider.

### Token exchange

The client's Authorization header is always forwarded to upstream servers, allowing them to authenticate and authorize access as needed. As a best practice, Authorino can be configured to perform OAuth2 token exchange (RFC 8693) as part of the AuthPolicy pipeline, replacing the client's token with one of reduced scope and specific audience via a confidential client. This limits the blast radius if a backend server is compromised. The gateway components themselves contain no token exchange logic — it is entirely handled by Authorino.

### Tool-level RBAC

AuthPolicy can enforce per-tool access control using the `x-mcp-toolname` and `x-mcp-servername` headers set by the router. JWT claims (e.g., `resource_access`) are matched against the requested tool to produce an `x-authorized-tools` JWT signed header via Authorino. The broker verifies this signature and filters tool lists accordingly.  

## Session Isolation

The MCP protocol is stateful. The gateway manages three session types to prevent cross-user data leakage. See [session-mgmt.md](session-mgmt.md) for the full design.

- **Gateway session**: a signed JWT issued by the broker on `initialize`, unique per client. Used as the key for backend mcp server session lookups.
- **Client-backend session**: a separate MCP session negotiated per client per upstream server. Cached by gateway session ID + server name. One client cannot access another client's backend sessions.
- **Broker session**: a long-lived service account session between the broker and each upstream server, used only for tool discovery and notifications — not used during client interactions or exposed to the client in any way.

Gateway sessions expire (default 24 hours), and all associated backend sessions are closed on expiry. Session IDs from backends are never exposed to clients — the router rewrites them to the gateway session ID.

## Prompt Injection and Context Pollution

### Gateway position

The router parses MCP JSON-RPC bodies to extract the method and tool name for routing, but tool arguments, tool responses, and any embedded prompts pass through without semantic inspection. The gateway cannot distinguish between legitimate tool arguments and injected prompts. The Gateway doesn't directly integrate with agents/models. It is the users responsibility to configure a prompt guard capability that works with the Gateway to intercept and semantically evaluate the payload.


### Context pollution

Context pollution — where untrusted data from one tool call influences subsequent LLM reasoning — is similarly outside the gateway's scope. The gateway ensures session isolation (one user's sessions are not shared with another), but it does not control how an LLM client aggregates tool responses into its context window.

### Recommendations

- Deploy a prompt guard (e.g., input/output scanning) as an additional plugin to Envoy, to inspect MCP payloads before they reach tool servers or return to clients
- Upstream MCP servers should also validate and sanitize their own inputs

## Known Risks and Mitigations

| Risk | Severity | Current status | Mitigation |
|------|----------|---------------|------------|
| No payload-level prompt injection detection | Medium | By design — gateway for tool calls is just a routing layer | Use prompt guard tooling alongside the gateway |
| Tool responses streamed as-is from backends | Medium | By design | Upstream servers must validate outputs; prompt guards can inspect, reject, modify responses |
| Client custom headers forwarded to backends | Low | `mcp-session-id` replaced with specific target mcp backend session | Review header forwarding policy if backends are untrusted |
| No response size limits on SSE streams | Low | Not implemented | Envoy buffer limits and timeouts provide partial mitigation |
| JWT_SESSION_SIGNING_KEY defaults to "default-not-secure" if not set | High | Not implemented | Set via flag/envvar or [#714](https://github.com/Kuadrant/mcp-gateway/issues/714) to have the controller generate and manage the key |
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
