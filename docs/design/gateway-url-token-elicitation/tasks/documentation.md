# URL Elicitation Documentation Plan

Documentation for URL elicitation, organized by user goals. Each section maps to a guide or doc update.

## User-Facing Guide (`docs/guides/url-elicitation.md`)

### When I want to securely collect per-user tokens for an upstream MCP server

When a platform operator has an upstream MCP server that requires each user to authenticate with their own token, they want to enable URL elicitation so that the gateway collects tokens at runtime without exposing them to the MCP client or LLM context.

**Cover:**
- Adding `credentialURLElicitation: {}` to an MCPServerRegistration
- Enabling the feature with `--enable-elicitation`
- What the user experience looks like (tool call → prompt → browser → retry)
- Prerequisites (HTTPS, client capability)

### When I want to use my own credential UI instead of the built-in page

When a platform operator already has credential infrastructure (e.g., Vault web UI), they want to direct users there instead of the broker's built-in page so that tokens are managed in their existing system.

**Cover:**
- Setting `credentialURLElicitation.url` to an external URL
- How AuthPolicy on the upstream route handles token injection
- Differences from the default flow (no cache write, no completion notification)

### When I want to protect the token page from unauthorized access

When a platform operator deploys URL elicitation, they want to ensure only the authenticated user who triggered the elicitation can submit a token, so that attackers cannot inject credentials into other users' sessions.

**Cover:**
- How session JWT binding prevents cross-session token injection (broker verifies session JWT matches elicitation ID)
- Why `mcp-session-id` as a custom header prevents browser-based forgery (CORS blocks cross-origin header setting)
- AuthPolicy as an additional layer restricting access to authenticated and authorized users
- Link to the MCP spec's phishing warning

### When I have automated agents that can't use a browser

When a platform operator has both interactive users and automated agents (CI/CD, agent-to-agent) calling the same MCP servers, they want agents to work without being blocked by elicitation prompts.

**Cover:**
- No configuration needed — behavior is automatic based on client capabilities
- How agents should pass tokens via the Authorization header
- What happens when the upstream returns 401 for an agent (standard error, not -32042)

### When a user's token expires and they need to provide a new one

When a user's cached token is rejected by the upstream server, they want to be prompted to provide a new one without restarting their session.

**Cover:**
- How 401 invalidation works (automatic cache clear + re-elicitation)
- JWT expiry detection (pre-emptive cache miss before hitting upstream)
- What the user sees (same flow as initial elicitation)

## Security Architecture Update (`docs/design/security-architecture.md`)

### When I need to understand how user tokens are isolated and protected

When a security reviewer or contributor needs to assess the token handling in URL elicitation, they want to understand data boundaries and protection mechanisms.

**Cover:**
- Token data flow: broker writes → cache stores (encrypted) → router reads → upstream receives
- Encryption at rest: AES-GCM with HKDF-derived key, only for external cache backends
- Session scoping: tokens bound to gateway session, lost on session expiry
- Identity verification: session JWT binding (broker verifies match), custom header prevents browser forgery, AuthPolicy as additional hardening
- Known risks: no completion callback for external URL pattern, cache eviction loses tokens

## API Reference Update (`docs/reference/mcpserverregistration.md`)

### When I need to know the exact field names and types for URL elicitation

When a platform operator is writing MCPServerRegistration YAML, they want to know the exact API surface for `credentialURLElicitation`.

**Cover:**
- `credentialURLElicitation` object (optional)
- `credentialURLElicitation.url` field (optional string, overrides default broker page)
- Relationship to `credentialRef` (they serve different purposes)
- Examples: minimal config, external URL config
