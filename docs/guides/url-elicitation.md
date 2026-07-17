# URL Elicitation: Per-User Token Collection

This guide covers collecting per-user tokens for upstream MCP servers at runtime using URL elicitation. Use this when an upstream server requires individual user credentials (e.g. a GitHub PAT) that are separate from the gateway's own authentication.

## Prerequisites

- MCP Gateway installed and configured
- An MCPServerRegistration with a `credentialRef` (broker still needs credentials for tool discovery)
- HTTPS configured on the gateway (the token page URL uses the gateway's public hostname)
- MCP client that supports elicitation capability (e.g. Claude Code, Claude Desktop)
- An AuthPolicy applied to the gateway route (see [Authentication](./authentication.md))

## How It Works

1. Client calls a tool on a server with `tokenURLElicitation` configured
2. Router checks the session cache for a stored token — on miss, returns a `-32042 URLElicitationRequired` error containing a URL
3. Client opens the URL in the user's browser
4. User enters their token on the gateway-hosted page
5. Client retries the tool call — router injects the cached token as the `Authorization` header
6. Upstream server receives the token and processes the request

The gateway selects this flow only for clients that declared `capabilities.elicitation` during initialization. Non-interactive agents that pass an `Authorization` header directly are unaffected.

## Step 1: Apply an AuthPolicy

URL elicitation requires an AuthPolicy on the gateway route. Without one, there is no identity check on the token page — anyone with the elicitation URL could inject tokens into another user's session.

See [Authentication](./authentication.md) for configuring OIDC on your gateway route. The AuthPolicy ensures that both the MCP client and the browser-based token page are authenticated against the same identity provider, enabling the gateway to verify that the user submitting the token is the same user who triggered the elicitation.

## Step 2: Enable URL Elicitation on the Gateway

URL elicitation is disabled by default. Enable it on the MCPGatewayExtension:

```bash
kubectl patch mcpgatewayextension mcp-gateway -n mcp-gateway \
  --type=merge -p '{"spec":{"urlElicitation":"Enabled"}}'
```

The operator reconciles this field into the deployment args and creates the `/tokens` HTTPRoute automatically.

> **Note:** Enable this only after an AuthPolicy is in place. Without authentication, the token page is open to anyone who obtains or guesses the elicitation URL.

## Step 3: Configure an MCPServerRegistration

Add `tokenURLElicitation: {}` to your MCPServerRegistration spec:

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: my-server
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-server-route
  prefix: myserver_
  credentialRef:
    name: my-server-broker-cred
    key: token
  tokenURLElicitation: {}
```

The `credentialRef` is still required — the broker uses it to connect to the upstream server for tool discovery. The `tokenURLElicitation` field tells the router to collect per-user tokens at tool-call time.

## Step 4: Configure Your MCP Client

Add the gateway as a remote MCP server in your client configuration.

**Claude Code** (`~/.claude.json` or project `.mcp.json`):

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "url",
      "url": "https://mcp.example.com/mcp"
    }
  }
}
```

**Claude Desktop** (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "url",
      "url": "https://mcp.example.com/mcp"
    }
  }
}
```

Replace `https://mcp.example.com/mcp` with your gateway's public URL.

## Step 5: Verify the Flow

1. Start your MCP client — it connects to the gateway and discovers tools
2. Call a tool on the elicitation-enabled server
3. The client prompts you to open a URL in your browser
4. Enter your token on the gateway-hosted page and submit
5. The client retries automatically — the tool call succeeds

## Using an External Credential UI

To direct users to your own credential page (e.g. a Vault web UI) instead of the built-in page, set the `url` field:

```yaml
spec:
  tokenURLElicitation:
    url: "https://vault.example.com/ui/tokens"
```

The gateway appends `?elicitation_id=<id>` to this URL. Your external page is responsible for storing the token — the gateway will not cache tokens submitted to external URLs.

## Non-Interactive Agents

No configuration is needed. The gateway automatically detects whether a client supports elicitation based on its `capabilities` declaration during initialization:

- **Interactive clients** (with `capabilities.elicitation`) receive `-32042` errors with a URL
- **Automated agents** (without the capability) receive a standard error

Agents that need upstream access should pass tokens directly via the `Authorization` header on each request. When the upstream returns 401 to an agent, the error is passed through as-is.

## Token Expiry and Renewal

When a cached token is rejected by the upstream (401 response), the gateway automatically:

1. Deletes the cached token
2. Returns a new `-32042` error with a fresh token page URL
3. The user enters a new token and the flow continues

JWT tokens are also checked for expiry before use — an expired JWT is treated as a cache miss without hitting the upstream.

## Security Considerations

- **AuthPolicy required**: An AuthPolicy on the gateway route is the primary security control. It authenticates both the MCP client and the browser token page against the same identity provider, preventing token injection by unauthorized users. The broker implicitly trusts that the JWT has been verified by the AuthPolicy — it does not repeat signature or expiry validation.
- **Identity verification (`sub` claim)**: The broker compares the `sub` claim from the browser request's JWT with the `sub` stored in the elicitation entry (captured from the MCP client session). This ensures the user submitting the token is the same user whose tool call triggered the elicitation.
- **CSRF protection**: The token page uses a cookie-based CSRF token. The GET response sets a `csrf` cookie and includes a matching hidden form field. The POST handler validates that the cookie and form values match, preventing cross-site form submissions.
- **Single-use entries**: Each elicitation ID can only be claimed once — submitting the form consumes the entry, providing anti-replay protection.

> **Note:** The MCP specification includes a [phishing warning](https://modelcontextprotocol.io/specification/draft/client/elicitation) about URL elicitation. Ensure your users understand they should only enter tokens on URLs they trust.

## Next Steps

- [Authentication](./authentication.md) — configure OAuth for gateway access
- [Authorization](./authorization.md) — restrict tool access per user
- [MCPServerRegistration Reference](../reference/mcpserverregistration.md) — full API reference
