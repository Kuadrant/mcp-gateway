# Auth with the MCP Gateway (Phase 2)

Following on from [Auth Phase 1](./auth-phase-1.md), we will now also show how to provide authorization capabilities to enable identity-based tool filtering and token exchange at the gateway via Kuadrant integration and AuthPolicy API, to ensure tools returned from the gateway are based on permissions and to reduce the scope of the token passed on to backend MCP servers.

> **Note:** while we use Keycloak in our examples, other IDPs could also be used.

> **Note:** while we use Kuadrant to enforce auth requirements, it could be done in other ways.


**Goals**

- Provide the capability of informing the gateway via a trusted mechanism of the subset of tools a client doing a `tools/list` is allowed to see based on their identity, so that the list response can be filtered down to only the permitted tool set.

- Provide a mechanism for doing token exchange at the gateway based on the target MCP for a given `tool/call`, to reduce scope and audience of the token passed to the backend MCP server.

**Constraints**

- We will use Keycloak as the identity provider
- We will use AuthPolicy from the Kuadrant project to protect the gateway

### Identity-based tool filtering

![](./images/tools-list.jpg)

In order to filter down the available tools, either generally or within the context of a virtual MCP, based on the auth integration, a trusted source can set a header `x-mcp-authorized`. This header is expected to be in JWT format signed by a trusted key pair, with the public element being shared with the broker via env var `TRUSTED_HEADER_PUBLIC_KEY`. The list of tools has to be a claim within this JWT with a key `allowed-capabilities` and a value as shown below. The broker will look for this header during a `tools/list` call. If set, it will validate the JWT, extract the allowed tools and ensure the returned tools are matched against this list. If it fails to validate the header, it will return an empty list.

Example JWT payload:

```json
{
  "allowed-capabilities": "{\"tools\":{\"mcp-test/mcp-server1-route\":[\"greet\"],\"mcp-test/mcp-server2-route\":[\"headers\"],\"mcp-test/mcp-server3-route\":[\"add\"]}}",
  "exp": 1760004918,
  "iat": 1760004618,
  "iss": "Authorino",
  "sub": "fbd8ed47a62069bb1d7f328c4c25c47eef49486a3c71cfd179bf17946dc86637"
}
```

The MCP Broker, when receiving a `tools/list` call, will validate the JWT token if present in the request, and load the RBAC data from the JWT.

We have provided an [example AuthPolicy](../../config/samples/oauth-token-exchange/tools-list-auth.yaml) resource showing how to do this using Keycloak to provide the authentication and store the permissions. We then use the [wristband](https://github.com/Kuadrant/authorino/blob/main/docs/features.md#festival-wristband-authentication) feature of the AuthPolicy API (powered by Authorino) to create a signed JWT to securely carry this information to the MCP Broker.

#### Storing tool access control permissions in Keycloak

There are multiple ways to configure a Keycloak realm for storing permissions to control access to the MCP tools. The method employed in the [example provided](../../config/keycloak/realm-import.yaml) consists of:
- An OAuth "resource server" client for each MCP server, identified by the MCP server's internal host name for convenience
- Each tool of an MCP server defined as a role of the resource server client, prefixed with `tool:` (e.g. `tool:greet`)
- Groups representing aggregations of permissions, to which MCP tool client roles are assigned
- Users added as members of the groups whose assigned tools the user can access
- An OpenId Connect client for each MCP client (e.g. agent) that requests access to the MCP system, created by the [OAuth2 Dynamic Client Registration Protocol](https://modelcontextprotocol.io/specification/2025-03-26/basic/authorization#dynamic-client-registration), with **Full scopes allowed** and the `roles` client scope set by default

This setup causes the access tokens issued to the MCP clients to include a `resource_access` claim, listing all tools a user has access to (grouped by MCP server). The [AuthPolicy](../../config/samples/oauth-token-exchange/tools-list-auth.yaml) declares how the permissions should be extracted from this claim and injected into the `tools/list` request for filtering.

> **Note:** If the group aggregation to control access to the tools is not desired/needed, the setup above can be simplified by mapping users directly to the individual MCP tool client roles the user can access.

### Token Exchange

There are two key uses cases here:

1) exchanging the token for an API key stored in a secret store such as Vault.
2) exchanging the token for another OAuth2 access token ([RFC 8693](https://www.rfc-editor.org/rfc/rfc8693.html)) that has specific scopes and audiences.

![](./images/token-exchange.jpg)

#### API Keys

For this use case, the solution is to have the AuthPolicy fetch the token from the secret store, such as Vault, and then add that as the API Key in the request headers before the request is routed on to the backend MCP Server.

#### OAuth2 Token Exchange

In order to provide OAuth2 token exchange ([RFC 8693](https://www.rfc-editor.org/rfc/rfc8693.html)), it is expected that there is a confidential client in the identity provider for the MCP Router to use to perform the token exchange. In our example, we will use `mcp-gateway` as the confidential client that requests the token exchange.

Then, an AuthPolicy is put in place to protect tool calls. See [example token exchange policy](../../config/samples/oauth-token-exchange/tools-call-auth.yaml). In this policy, we use the client ID and secret from the confidential client to call to Keycloak and do the token exchange once the incoming gateway JWT has been validated. The token exchange response is parsed and the new token set as the `Authorization:` header once all other authorization checks in the AuthPolicy have succeeded.

##### A note on scopes and audiences

In our example, the `aud` claim ("audience") of the new token is set to the internal host name of the target MCP server and a default scope of `openid`. This is the default for the entire gateway and all MCP servers. However, using the [defaults and overrides](https://docs.kuadrant.io/latest/kuadrant-operator/doc/overviews/auth/#defaults-and-overrides) feature of AuthPolicy, these values could be overridden at the HTTPRoute level.

For the OAuth2 token exchange to succeed, the original token that is exchanged for a new, scoped one MUST include in the audiences: the same audience that represents the target MCP server (for which the new token is requested) _and_ the client ID of the client that performs the token exchange (`mcp-gateway` in our example.)

##### Configuring Keycloak to enable OAuth2 Token Exchange

Enabling OAuth2 Token Exchange in a Keycloak realm for the MCP gateway example consists of:
- An OpenId Connect confidential client `mcp-gateway` with the **Standard Token Exchange** grant enabled
- A synthetic `impersonator` role defined for the token exchange client
- A `mcp-users` group to which the `impersonator` role is assigned
- All users of the MCP system added to the `mcp-users` group by default

> **Note:** The setup above could be simplified by skipping the group aggregation and mapping users directly to the `impersonator` role.

Having a synthetic role to which users are given access to, in combination with **Full scopes allowed** and `roles` scope defined for the OpenId Connect clients negotiating access tokens, causes the `mcp-gateway` client to be included in the audiences of the access tokens along with the resource server clients for the MCP servers.
