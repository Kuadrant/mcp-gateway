## URL Elicitation E2E Test Cases

---
test_suite: elicitation_test.go
tags: Happy,URLElicitation,Security
---

### [Happy,URLElicitation] URL elicitation triggers on missing token for elicitation-capable client

- When an MCPServerRegistration is configured with `credentialURLElicitation: {}` and `--enable-elicitation` is true, and a client that declares `elicitation.url` capability during initialize calls a tool on that server without an Authorization header, the gateway should return a URLElicitationRequiredError (-32042) as an SSE response. The error should contain an `elicitations` array with a single entry containing `mode: "url"`, an `elicitationId`, a `url` pointing to the broker's `/credentials` page with the correct server name and elicitation ID as query parameters, and a `message` describing what token is needed.


### [Happy,URLElicitation] Token provided via broker page is used for upstream auth

- When a client receives a -32042 error and the user provides a token via the broker's `/credentials` page (POST with the correct elicitation ID and server name), the token should be stored in the session cache. When the client retries the same tool call, the gateway should read the token from cache, set the Authorization header with the token value, and forward the request to the upstream MCP server. The upstream server should receive the token and return a successful tool result.


### [Happy,URLElicitation] Full elicitation round-trip with token validation

- When an MCPServerRegistration targets a backend MCP server that validates Bearer tokens (e.g. api-key-server), and the server is configured with `credentialURLElicitation: {}`, a client that declares `elicitation.url` capability should be able to complete the full flow: call a tool → receive -32042 → provide the correct token via the broker page → retry the tool call → receive a successful result from the upstream server. The upstream server should see the correct Authorization header.


### [URLElicitation] Cached token reused across multiple tool calls

- When a client has already provided a token via elicitation for a given server, subsequent tool calls to the same server should reuse the cached token without triggering another -32042 error. The Authorization header should be set from the cached token on each call, and the upstream server should return successful results.


### [URLElicitation] Elicitation with external URL returns configured URL

- When an MCPServerRegistration is configured with `credentialURLElicitation: { url: "https://vault.example.com/ui" }`, the -32042 error returned to the client should contain the external URL verbatim instead of the broker's `/credentials` page URL. The gateway should not generate a broker page URL when an external URL is configured.


### [Happy,URLElicitation] 401 from upstream invalidates cached token and re-triggers elicitation

- When the upstream MCP server returns a 401 Unauthorized response (e.g. because the cached token has expired or been revoked), the gateway should delete the cached token for that server and session, and return a -32042 URLElicitationRequiredError to the client. The client should be able to provide a new token via the broker page and retry successfully.


### [URLElicitation] Non-elicitation-capable client gets standard error on missing token

- When a client does not declare `elicitation.url` capability during initialize, and calls a tool on a server configured with `credentialURLElicitation: {}` without an Authorization header, the gateway should return a standard error (not -32042). The client should not be directed to a credential page.


### [Happy,URLElicitation] Non-elicitation-capable client with Authorization header succeeds

- When a client does not declare `elicitation.url` capability but sends an Authorization header with its request, the gateway should use the Authorization header as-is for upstream routing, regardless of the server's `credentialURLElicitation` configuration. The tool call should succeed if the token is valid.


### [URLElicitation] Elicitation-capable client with Authorization header bypasses cache

- When a client declares `elicitation.url` capability and sends an Authorization header with its tool call request, the gateway should use the provided Authorization header as-is without checking the token cache. No -32042 error should be returned.


### [URLElicitation] Elicitation disabled via feature flag has no effect

- When `--enable-elicitation` is false, even if an MCPServerRegistration has `credentialURLElicitation` configured, the gateway should not return -32042 errors and should not check the token cache. Tool calls should behave as if `credentialURLElicitation` is not set. The Authorization header from the client request should be used as-is.


### [URLElicitation] Broker credential page rejects invalid elicitation ID

- When a user navigates to the broker's `/credentials` page with an invalid or missing `elicitation_id` parameter, the page should return an error and not store any token. A missing `server` parameter should also result in an error.


### [URLElicitation,Security] Broker credential page rejects session mismatch

- When a user submits a token to the broker's `/credentials` page but the session JWT on the request does not match the session ID embedded in the elicitation ID, the broker should reject the request and not store the token. This prevents an attacker from submitting a token into another user's session using a stolen elicitation URL.


### [URLElicitation,Security] Credential page rejects mismatched session (phishing prevention)

- When client Alice triggers elicitation and receives a -32042 with an elicitation URL containing her session ID, a direct HTTP request to the broker's `/credentials` POST endpoint with a different `mcp-session-id` header (emulating a different user) should be rejected. The broker should not store the token. This emulates the MCP spec's phishing scenario where an attacker forwards an elicitation URL to a victim — in practice browsers cannot set the custom header on navigation, but the e2e test verifies the broker's server-side session mismatch check.


### [URLElicitation] Expired JWT token treated as cache miss

- When a cached token is a JWT with an expired `exp` claim, the gateway should treat it as a cache miss on the next tool call. The expired token should be deleted from the cache, and a -32042 error should be returned to an elicitation-capable client, prompting the user to provide a new token.


### [URLElicitation] Opaque token not subject to expiry check

- When a cached token is not a JWT (e.g. a GitHub PAT like `ghp_abc123`), the gateway should return it from cache without any expiry check. The token should be forwarded to the upstream server as-is.


### [Happy,URLElicitation] Server without credentialURLElicitation is unaffected

- When an MCPServerRegistration does not have `credentialURLElicitation` configured, tool calls should behave exactly as before — no token cache lookup, no -32042 errors, no changes to the Authorization header handling. This verifies no regression in existing behavior.
