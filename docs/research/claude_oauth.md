# Claude Code OAuth + Keycloak: Debugging Notes

## How Claude Code authenticates with MCP servers

1. Discovers `/.well-known/oauth-protected-resource` from the MCP gateway
2. Reads `authorization_servers` and `scopes_supported` from the response
3. Fetches `/.well-known/openid-configuration` from the authorization server
4. Uses `registration_endpoint` for **RFC 7591 Dynamic Client Registration**
5. Sends a `scope` field in the registration request based on `scopes_supported` + `offline_access`
6. Uses the dynamically registered client for the authorization code flow

## Key findings

### 1. Keycloak scope assignment depends on the `scope` field in registration

- **Without `scope` field**: client gets all realm `defaultDefaultClientScopes` as default scopes
- **With `scope` field**: client gets `basic` as default, everything else as optional, and only scopes listed in the field are assigned

This means the `scopes_supported` in the protected resource response must include every scope Claude will request, including `offline_access`.

### 2. One invalid scope rejects all scopes

Keycloak validates all requested scopes at the authorization endpoint. If **any single scope** is not assigned to the client (default or optional), the entire request is rejected with `invalid_scope` listing ALL scopes as invalid.

### 3. Claude adds `offline_access` automatically

Claude always requests `offline_access` at the authorization endpoint (to get refresh tokens), even if it's not in `scopes_supported`. If the gateway doesn't advertise it, Claude won't include it in the registration `scope` field, so Keycloak won't assign it to the client. Then the auth request fails.

**Fix**: Always include `offline_access` in `OAUTH_SCOPES_SUPPORTED`.

### 4. Consent Required policy affects dynamic registration

The Keycloak "Consent Required" client registration policy (anonymous subType) sets `consentRequired=true` on dynamically registered clients. This flag is set at registration time and persists even after the policy is removed. For MCP use cases, this policy should be removed or the default should be changed.

### 5. Offline tokens require the `offline_access` realm role

Even after fixing scope issues, the `mcp` user needs the `offline_access` realm role assigned (directly or via `default-roles-mcp` composite). Without it, Keycloak returns "Offline tokens not allowed for the user or client".

## Required Keycloak configuration for Claude Code

1. `offline_access` must be a realm-level client scope
2. `offline_access` must be in `defaultDefaultClientScopes` (so the registration policy allows it)
3. The `mcp` user must have the `offline_access` realm role
4. The gateway must advertise `offline_access` in `scopes_supported`
5. Consider removing the "Consent Required" registration policy for anonymous clients

## Environment variables

- `OAUTH_SCOPES_SUPPORTED`: comma-separated scopes for the protected resource response
  - Recommended: `openid,basic,groups,roles,profile,offline_access`
  - Default (code): `basic`

## Solution summary

Three changes were needed to make Claude Code OAuth work with Keycloak:

1. **Gateway**: set `OAUTH_SCOPES_SUPPORTED=openid,basic,groups,roles,profile,offline_access` — must include `offline_access` because Claude requests it automatically but only includes it in the dynamic registration `scope` field if advertised
2. **Keycloak realm**: all advertised scopes must be in `defaultDefaultClientScopes` so the "Allowed Client Scopes" registration policy permits them for anonymous dynamic registration
3. **Keycloak user**: the authenticating user must have the `offline_access` realm role (typically via `default-roles-mcp` composite)

## What needs to change in the codebase

- Default `OAUTH_SCOPES_SUPPORTED` in `internal/broker/oauth_protected_resource_handler.go` should include `offline_access`
- Keycloak realm import (`config/keycloak/realm-import.yaml`) should include `offline_access` in `defaultDefaultClientScopes` and assign `default-roles-mcp` to the `mcp` user
