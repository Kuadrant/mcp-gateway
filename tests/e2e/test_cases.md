## Test Cases

> **Note:** Test cases reference default namespace names (`mcp-system`, `gateway-system`, `mcp-test`). These are configurable via environment variables (`MCP_GATEWAY_NAMESPACE`, `GATEWAY_NAMESPACE`, `TEST_SERVER_NAMESPACE`). See `README.md` for configuration details.


### [Happy] Test registering multiple MCP servers with the gateway

- When a developer creates multiple MCPServerRegistration resources with their corresponding HTTPRoutes, the gateway should register all servers and make their tools available. Each server's tools should be prefixed with the server's prefix to avoid naming conflicts. A tools/list request should return tools from all registered servers.


### [Happy] Test unregistering MCP servers from the gateway

- When an MCPServerRegistration resource is deleted from the cluster, the gateway should remove the server and its tools should no longer be available. A subsequent tools/list request should not include any tools with that server's prefix.


### [Happy] Test invoking tools through the gateway

- When a client calls a tool through the gateway using the prefixed tool name, the gateway should forward the request to the appropriate backend MCP server and return the result. The tool should execute successfully and return the expected response.


### [Happy] Test MCP server registration with credentials

- When an MCPServerRegistration resource references a credential secret, the gateway should use those credentials to authenticate with the backend MCP server. If the credentials are invalid, the server should not be registered and its tools should not be available. When the credentials are updated to valid values, the server should become registered and its tools should appear in the tools/list.


### [Happy] Test backend MCP session reuse

- When a client makes multiple tool calls to the same backend MCP server, the gateway should reuse the same backend session for efficiency. The backend session ID should remain consistent across multiple calls from the same client. When a client disconnects and reconnects, a new backend session should be created.


### [Happy] Concurrent tool calls on a fresh session create only one backend session

- When multiple concurrent tool calls arrive for the same gateway session before any backend session has been established, the gateway should initialize exactly one backend session and all concurrent calls should complete successfully using that same backend session ID. No backend connections should be leaked regardless of how many concurrent callers race to trigger initialization.


### [Happy] Test MCPVirtualServer behaves as expected when defined

- When a developer defines an MCPVirtualServer resource and specifies the value of the `X-Mcp-Virtualserver` header as the name in the format `namespace/name`, where the namespace and name come from the created MCPVirtualServer resource, they should only get the tools specified in the MCPVirtualServer resource when they do a tools/list request to the MCP Gateway host.


### [Happy] Test tools are filtered down based on x-mcp-authorized header

- When the value of the `x-mcp-authorized` header is set as a JWT signed by a trusted key to a set of tools, the MCP Gateway should respond with only tools in that list.


### [Happy] Test notifications are received when a notifications/tools/list_changed notification is sent

- When an MCPServerRegistration is registered with the MCP Gateway, a `list_changed` notification should be sent to any clients connected to the MCP Gateway. One spec registers a server exposing both tools and prompts (server1) and covers the registration-driven notification for both lists. This notification should work for a single connected client as well as multiple connected clients. They should all receive the same notification at least once. The clients should receive these notifications within one minute of the MCPServerRegistration having reached a ready state.

- When a registered backend MCP Server, emits a `notifications/tools/list_changed` a notification should be received by the connected clients. When the clients receive this notification they should get a changed tools/list. 

### [Happy] Test no two mcp-session-ids are the same

- When a client initializes with the gateway, the session id it receives should be unique. So if two clients connect at basically the same time, each of those clients should get a unique session id.

- If a client is closed and disconnects, if it connects to the gateway and initializes it should receive a new mcp-session-id

### [Happy] Test Hostname backendRef registers MCPServerRegistration

- When an HTTPRoute uses a Hostname backendRef (`kind: Hostname, group: networking.istio.io`) with a URLRewrite filter, and an MCPServerRegistration references that HTTPRoute, the controller should correctly handle the external endpoint configuration and the MCPServerRegistration should become ready. Tool discovery is not tested as it requires actual HTTPS connectivity to external services.

### [Full] Redis session cache persists backend sessions across pod restarts

- When the MCP Gateway is configured with a Redis session cache, backend MCP sessions should survive pod restarts. Redis is already deployed in the system namespace (default: mcp-system, configurable via `MCP_GATEWAY_NAMESPACE` env var) as part of CI setup. With a single gateway replica, create a secret in the system namespace with a `CACHE_CONNECTION_STRING` key containing the Redis connection string and label `mcp.kuadrant.io/secret: "true"`. Register an MCPServerRegistration and wait for tools to be available. Then enable Redis by patching the MCPGatewayExtension to set `sessionStore.secretName` referencing the Redis secret and waiting for the deployment rollout to complete. Using a raw HTTP client (not the MCP client library), send an `initialize` request to obtain a session ID, then send `notifications/initialized`, then call the `headers` tool to establish a backend session and capture the backend `Mcp-Session-Id` from the response content. Then trigger a rollout restart of the mcp-gateway deployment and wait for the new rollout to complete. After the new pod is running, call the `headers` tool again using the same raw HTTP session ID and verify the same backend `Mcp-Session-Id` is returned, proving the session was retrieved from Redis. Cleanup should remove the `sessionStore` from the MCPGatewayExtension spec and delete the Redis secret. Do not deploy or delete Redis in the test itself.

### [Full] Tools and prompts re-populate after gateway restart

- When the mcp-gateway deployment is restarted, all previously registered MCPServerRegistration tools and prompts should re-populate and become available to clients. After registering a server and verifying tools and prompts are present, trigger a rollout restart of the gateway deployment. Once the new pod is ready, reconnect an MCP client and verify that tools and prompts with the server's prefix are listed again and that tool invocation works correctly.

### [Full] Gracefully handle an MCP Server becoming unavailable

- When a backend MCP Server becomes unavailable, the gateway should no longer show its tools in the tools/list response and a notification should be sent to the client within one minute. When the MCP Server becomes available again, the tools/list should be updated to include the tools again. While unavailable any tools/call should result in a 503 response

### [Full] MCP Server status

- When a backend MCPServerRegistration is added but the backend MCP is invalid because it doesn't meet the protocol version the status of the MCPServerRegistration resource should report the reason for the MCPServer being invalid

- When a backend MCPServerRegistration is added but the backend MCP is invalid because the broker cannot connect to the the backend MCP server, the MCPServerRegistration resource should report the reason for the MCPServer being invalid

### [Full] Multiple MCP Servers without prefix

- When two servers with no prefix are used, the gateway sees and forwards both tools correctly.

### [Happy] Prefix conflict resolved by adding a prefix

- When two servers with no prefix conflict and one is then modified to have a specified prefix via the MCPServer resource, both tools should become available via the gateway and capable of being invoked

### [multi-gateway] Multiple Isolated MCP Gateways deployed to the same cluster

- As a platform admin having deployed multiple instances of the MCP Gateway using the MCPGatewayExtension resource, I should see that they become ready once I have created a valid referencegrant. Once the MCPGatewayExtension is valid, there should be a unique deployment of the mcp gateway in the same namespace as the MCPGatewayExtension resources

- As a client, when multiple isolated gateways are ready and available at different hostnames, I should be able to see a unique list of tools for each gateway based on the MCPServerRegistrations created by each team using the MCPGatewayExtension. Example I should see tools prefixed with team_a on one gateway and team_b on the second gateway

### [Happy] MCPGatewayExtension with sectionName targets specific listener

- When an MCPGatewayExtension is created with a valid `targetRef.sectionName` that matches a listener name on the Gateway, the extension should become Ready. The controller should read the listener port and hostname from the Gateway configuration. The EnvoyFilter should be created targeting the correct listener port, and the broker-router deployment should have the `--mcp-gateway-public-host` flag set based on the listener hostname. The Gateway's listener status should be updated with an MCPGatewayExtension condition. The condition should have type "MCPGatewayExtension", status "True", reason "Programmed", and a message indicating which extension and EnvoyFilter are using the listener.

- When an MCPGatewayExtension is deleted, the MCPGatewayExtension condition should be removed from the Gateway's listener status. The Gateway should no longer show the MCPGatewayExtension condition for that listener.

### [Full] MCPGatewayExtension with invalid sectionName is rejected

- When an MCPGatewayExtension is created with a `targetRef.sectionName` that does not match any listener on the Gateway, the extension should be marked as Invalid with a status message containing "listener not found". No EnvoyFilter or broker-router deployment should be created.

### [Happy] Wildcard listener hostname converted to mcp subdomain

- When a Gateway listener has a wildcard hostname like `*.example.com`, and an MCPGatewayExtension targets that listener without a public host annotation override, the broker-router deployment should have `--mcp-gateway-public-host=mcp.example.com`. The wildcard prefix should be replaced with "mcp".

### [Happy] MCPGatewayExtension rejected when listener allowedRoutes does not permit namespace

- When an MCPGatewayExtension targets a listener that has `allowedRoutes.namespaces.from: Same` and the MCPGatewayExtension is in a different namespace than the Gateway, the extension should be marked as Invalid with a status message indicating the namespace is not allowed.

### [Full] Second MCPGatewayExtension in same namespace is rejected

- When a namespace already has one MCPGatewayExtension that is Ready, and a second MCPGatewayExtension is created in the same namespace, the second extension should be marked as Invalid with a status message indicating a conflict. Only one MCPGatewayExtension is allowed per namespace, and the oldest by creation timestamp wins.

### [Full] MCPGatewayExtension targeting non-existent Gateway is rejected

- When an MCPGatewayExtension is created with a `targetRef` pointing to a Gateway that does not exist, the extension should be marked as Invalid with a status message indicating the invalid configuration.

### [multi-gateway] Shared Gateway with team isolation via sectionName

- Given a Gateway with two listeners configured for different teams:
  - Listener "team-a-external" on port 8080 with `allowedRoutes.namespaces.from: Selector` matching namespace "team-a"
  - Listener "team-b-external" on port 8081 with `allowedRoutes.namespaces.from: Selector` matching namespace "team-b"

- When team-a creates an MCPGatewayExtension in namespace "team-a" with `targetRef.sectionName: team-a-external`, and team-b creates an MCPGatewayExtension in namespace "team-b" with `targetRef.sectionName: team-b-external`:
  - Both MCPGatewayExtensions should become Ready
  - Each team should have their own broker-router deployment in their namespace
  - The Gateway should show MCPGatewayExtension conditions on both listeners

- When team-a registers MCPServerRegistrations with prefix "team_a_" and team-b registers MCPServerRegistrations with prefix "team_b_":
  - Clients connecting to the team-a gateway endpoint should only see tools with "team_a_" prefix
  - Clients connecting to the team-b gateway endpoint should only see tools with "team_b_" prefix
  - Tool invocations should route to the correct backend for each team

- When team-b attempts to create an MCPGatewayExtension targeting "team-a-external":
  - The extension should be marked Invalid because the listener's allowedRoutes does not permit routes from namespace "team-b"

### [Happy] MCPGatewayExtension creates HTTPRoute for gateway access

- When an MCPGatewayExtension is created targeting a Gateway listener with a hostname, the controller should automatically create an HTTPRoute named "mcp-gateway-route" in the same namespace as the MCPGatewayExtension. The HTTPRoute should have a parentRef pointing to the target Gateway with the correct sectionName, a hostname matching the listener hostname (or "mcp.<domain>" for wildcard listeners), PathPrefix matches for /mcp and /.well-known/oauth-protected-resource, and a backendRef to the mcp-gateway service on port 8080. The HTTPRoute should have an owner reference to the MCPGatewayExtension so it is cleaned up automatically on deletion. MCP clients should be able to reach the gateway at the derived hostname without any manual HTTPRoute creation.

### [Happy] MCPGatewayExtension with httpRouteManagement Disabled skips HTTPRoute creation

- When an MCPGatewayExtension has `spec.httpRouteManagement: Disabled`, the controller should NOT create an HTTPRoute. This allows users to manage their own HTTPRoute with custom configuration (e.g. CORS response headers or custom filters). The MCPGatewayExtension should still become Ready and all other resources (Deployment, Service, EnvoyFilter) should be created normally.

### [multi-gateway] Each MCPGatewayExtension gets its own HTTPRoute

- When multiple MCPGatewayExtensions target different listeners on a shared Gateway, each should get its own HTTPRoute with the correct hostname derived from its listener. For example, team-a's HTTPRoute should have hostname "team-a.127-0-0-1.sslip.io" and team-b's should have "team-b.127-0-0-1.sslip.io". Each HTTPRoute's parentRef should reference the correct sectionName. Clients should be able to reach each team's gateway independently via the correct hostname.

### [multi-gateway] MCPGatewayExtension rejected when targeting a listener port already in use

- When a Gateway has two listeners on the same port (e.g. both on port 8080) and an MCPGatewayExtension already targets one of those listeners:
  - A second MCPGatewayExtension targeting the other listener on the same port should be marked Invalid with a status message indicating a port conflict
  - The message should identify the conflicting extension and listener
  - Only one ext_proc can handle a given port, so the oldest MCPGatewayExtension (by creation timestamp) wins
  - If the winning extension is deleted, the previously rejected extension should reconcile and become Ready

### [Happy] Tool schema validation filters invalid tools

- When a backend MCP server presents tools with invalid JSON schemas (e.g. `inputSchema.properties` with `"type": "int"` instead of `"integer"`), the gateway should filter out the invalid tools and not serve them to clients. The default `FilterOut` policy means the server is still registered and any valid tools are served, but invalid tools are excluded from `tools/list` responses. The `/status` endpoint should report the invalid tool count and details for the affected server. The custom-response-server is used for this test as it has a tool with `"type": "int"` in its input schema.

### [Happy] Prompt listing, invocation, and unregistration

- When an MCPServerRegistration is registered with a backend MCP server that has prompts (e.g., server1 with a "greet" prompt), the gateway should discover and serve those prompts with the server's prefix applied. A prompts/list request should return the prompt with the prefixed name. A prompts/get request using the prefixed prompt name should return the prompt's messages. When the MCPServerRegistration is deleted, the prompt should no longer appear in prompts/list.

### [Happy] Multi-server prompt aggregation

- When multiple MCPServerRegistration resources are created pointing to servers with prompts and different prefixes, a prompts/list request should return prompts from all registered servers, each prefixed with the corresponding server's prefix. For example, two registrations of server1 with prefixes "s1a_" and "s1b_" should produce "s1a_greet" and "s1b_greet" in the prompts list.

### [Happy] MCPVirtualServer filters prompts

- When an MCPVirtualServer resource specifies a subset of prompt names in its `prompts` field, a client using the `X-Mcp-Virtualserver` header should only see the specified prompts in a prompts/list response. A client without the header should still see all prompts.

### [Happy] VirtualServer with no prompts field exposes all prompts

- When an MCPVirtualServer resource omits the `prompts` field, all federated prompts should be returned in a prompts/list response. Tools should still be filtered by the `tools` field. This matches the behavior where omitting a field means "no filtering" rather than "deny all".

### [Happy] Test prompts are filtered down based on x-mcp-authorized header

- When the value of the `x-mcp-authorized` header is set as a JWT signed by a trusted key with a `prompts` capabilities payload, the MCP Gateway should respond to prompts/list with only the prompts in that list. Prompts from servers not named in the JWT should be excluded, and a client without the header should still see all prompts.

### [Happy] prompts/get for nonexistent prompt returns error

- When a client sends a prompts/get request with a prompt name that does not match any registered server, the gateway should return a JSON-RPC error with code -32602 (Invalid params).

### [Happy] Tool and prompt conflicts with same prefix

- When two MCPServerRegistrations with the same prefix point to a backend that has both tools and prompts (e.g. server1, where both registrations produce "conflict_time" and "conflict_greet"), at least one MCPServerRegistration should report a conflict in its status. One two-server cycle covers both the tool and the prompt name overlap behaviour.

### [Auth] JWT-filtered prompts/list with Keycloak

- When a client authenticates via Keycloak and sends a prompts/list request, only prompts the user has `prompt:*` roles for should be returned. The `mcp` user in the `accounting` group should see `test1_greet` but not prompts from servers where they have no prompt roles.

### [Auth] prompts/get with auth as first request (hairpin test)

- When a client sends a prompts/get request as the first request to a server (no prior tools/call), the hairpin initialize should pass through the AuthPolicy correctly and return prompt messages.

### [Auth] Combined JWT + VirtualServer prompt filtering

- When a client sends a prompts/list request with both a valid auth token and an `X-Mcp-Virtualserver` header, the result should be the intersection of both filters. The everything-server is registered with prefix `everything_` and its prompts are federated. If the JWT allows `test1_greet` but the VirtualServer only allows `everything_simple_prompt` (which the user has no JWT role for), the result should be empty.

### [Happy] Elicitation accept flow

- When a client connects to the gateway with an elicitation handler that accepts requests and provides user information, and calls a tool that triggers an elicitation request, the gateway should broker the elicitation between the upstream server and the client. The tool response should indicate that the user provided the requested information.

### [Happy] Elicitation decline flow

- When a client connects to the gateway with an elicitation handler that declines requests, and calls a tool that triggers an elicitation request, the gateway should broker the elicitation between the upstream server and the client. The tool response should indicate that the user declined.

### [Full] Elicitation without handler errors

- When a client connects to the gateway without an elicitation handler and calls a tool that triggers an elicitation request, the call should result in an error. The error may be a transport error or an error indicated in the tool result.

### [Happy,URLElicitation] URL elicitation triggers on missing token; server without tokenURLElicitation is unaffected

- When an elicitation-capable client calls a tool on an MCPServerRegistration that has `tokenURLElicitation` configured but the client has no cached token, the gateway should return a -32042 URLElicitationRequired error containing a URL pointing to the token page. The response should be an SSE JSON-RPC error with code -32042 and a `data.url` field. The same spec registers a second server without `tokenURLElicitation` or a credentialRef and verifies a tool call to it from the same session proceeds without any token resolution or -32042 error.

### [Happy,URLElicitation] Full round-trip: token page submit then retry succeeds

- When an elicitation-capable client receives a -32042 error, it should be able to GET the token page URL, POST the token via the form with the elicitation_id, then retry the tool call. On retry the cached token should be injected by the router as an Authorization header and the upstream server should receive it and return a successful tool response.

### [Full][URLElicitation] Cached token reused across multiple tool calls

- After a token has been submitted via the token page, subsequent tool calls to the same server from the same session should reuse the cached token without triggering a new -32042 error. The upstream server should receive the token on each call.

### [URLElicitation] Non-elicitation-capable client gets standard error on missing token

- When a client that did NOT declare `capabilities.elicitation` in its initialize request calls a tool on a server with `tokenURLElicitation` configured and no cached token, the gateway should return a tool result with `isError: true` and a message about elicitation (not a -32042 JSON-RPC error). The client should not receive a URL for token submission.

### [Happy,URLElicitation] 401 from upstream invalidates cached token and re-triggers elicitation

- When a client has a valid cached token and an established backend session, and the upstream server rejects a subsequent tool call with 401 (simulated via `X-Force-Auth-Reject` header on the api-key-server), the gateway should delete the cached token and pass the 401 through. The next tool call should trigger a fresh -32042 elicitation error. The client can then re-submit the correct token and retry successfully.

### [Happy] discover_tools returns correct metadata for registered servers

- When an MCPServerRegistration is created with `category` and `hint` fields, calling `discover_tools` should return a server entry containing the correct categories, hint, and prefixed tool names.

### [Happy] discover_tools category filtering: case-insensitive match, multi-category match, no match

- One spec registers a multi-category server (e.g. `["Dining", "reservations"]`) and a single-category messaging server once, then exercises three filters: a lowercase `dining` filter should return the multi-category server (case-insensitive match) and exclude the messaging server; a `reservations` filter should also return the multi-category server (matched by either category value); a category value no registered server has should match neither server.

### [Full] discover_tools respects auth filtering

- When a client sends requests with an `X-Mcp-Authorized` JWT that restricts visible tools, `discover_tools` should only return tools that the JWT authorises. Servers with no authorised tools should be excluded entirely.

### [Happy] discover_tools respects MCPVirtualServer scoping

- When a client sends requests with an `X-Mcp-Virtualserver` header, `discover_tools` should only return tools that the MCPVirtualServer allows. Servers with no allowed tools should be excluded.

### [Happy] select_tools scopes, re-scopes, and resets within one session

- One spec drives a single session through the full scope lifecycle: calling `select_tools` with a list of tool names should make subsequent `tools/list` requests return only those tools (plus the discover_tools and select_tools meta-tools); calling `select_tools` again should completely replace the first selection; calling it with an empty tools array should reset the session scope so `tools/list` returns the full tool set again.

### [Happy] select_tools returns error for invalid tool name

- When `select_tools` is called with a tool name that does not exist or is not visible, the response should contain a "not available" error.

### [Happy] select_tools all-or-nothing with partial valid list

- When `select_tools` is called with a list containing both valid and invalid tool names, the entire selection should fail. No partial scope should be applied.

### [Happy] notifications/tools/list_changed delivered after select_tools

- When a client with SSE notification support calls `select_tools`, a `notifications/tools/list_changed` notification should be delivered to that client over the SSE channel.

### [Full] discovery-tools-enabled=false hides meta-tools

- When the broker is started with `--discovery-tools-enabled=false`, `tools/list` should not include `discover_tools` or `select_tools`. Calling these tools should return an error.

### [Full] discovery-tool-threshold=0 means never hide

- When the threshold is 0 (default), all real tools should be visible alongside the meta-tools regardless of how many tools are registered.

### [Full] threshold above: only meta-tools shown

- When the `--discovery-tool-threshold` is set to a value lower than the number of registered tools, `tools/list` should return only the meta-tools. After using `select_tools` to scope down, the selected tools should become visible.

### [Happy] session scope does not leak across sessions

- When one session calls `select_tools` to scope down its tools, another session should still see the full tool set. Session scoping is per-session, not global.

### [Happy] concurrent select_tools calls do not corrupt scope state

- When two concurrent `select_tools` calls are made on the same session, the result should be a consistent single scope (one of the two wins), not a corrupted mixed state.

### [Full] controller re-reconciles when category/hint updated

- When the `category` or `hint` fields on a live MCPServerRegistration are updated, the controller should re-reconcile and the broker should reflect the new metadata in subsequent `discover_tools` calls. The old category should no longer match.

### [HTTPS] Default gateway uses private CA TLS (implicit — all tests)

- By default, e2e tests execute against a gateway with a TLS certificate generated by a private CA. The suite setup mounts this CA into the broker-router deployment via `--gateway-ca-cert`, allowing all tests to execute correctly without TLS errors. Any test that performs a `tools/call` exercises the HTTPS hairpin init path as a side effect.

### [HTTPS] [Happy] Test broker connects to TLS upstream with custom CA certificate

- When an MCPServerRegistration has a `caCertSecretRef` pointing to a valid CA certificate Secret and the upstream MCP server terminates TLS with a certificate signed by that CA, the broker should successfully connect via HTTPS, discover tools, and make them available through the gateway. The test deploys an nginx TLS proxy in front of mcp-test-server2 using a cert-manager private CA, creates a labeled Secret containing the CA certificate, and verifies that tools are listed and can be invoked through the gateway.
- **Runs on PR CI** — cert-manager and TLS test server are deployed by `make ci-setup`. Skips if either is absent.

### [HTTPS] [Negative] Test broker rejects TLS upstream with wrong CA certificate

- When an MCPServerRegistration has a `caCertSecretRef` pointing to a CA certificate that did NOT sign the upstream server's certificate, the broker should fail the TLS handshake and the MCPServerRegistration should be in a not-ready state. No tools with the server's prefix should appear in the tools list.
- **Runs on PR CI** — same dependencies as the happy path test above.

### [HTTPS] [Happy] broker connects to TLS upstream and tools/call works via hairpin SNI

- When a plain HTTP backend is registered on an HTTPS listener with a server-specific hostname, `tools/list` should discover tools and `tools/call` should succeed. The hairpin initialize request must use the server's hostname as TLS SNI so the gateway selects the correct filter chain and route table for that server.
- **Runs on PR CI.** Skips if cert-manager or the TLS test server are absent.

### [HTTPS] [HTTPS_EXTERNAL] External GitHub MCP server discovers tools over public TLS

- Registers the GitHub MCP server (`api.githubcopilot.com`) as an external hostname backend with a PAT credential. Verifies the broker connects over HTTPS, discovers at least one tool, and the config contains an `https://` URL.
- **Skips unless** `GITHUB_MCP_PAT` env var is set. Not run on PR CI to avoid depending on an external service we don't own.

### [HTTPS] [HTTPS_EXTERNAL] In-cluster MCP server accessible over public TLS via real certs

- Registers an internal MCP server and verifies tools/list works end-to-end when the gateway is fronted by a real publicly-trusted TLS certificate. Connects via the HTTPS gateway URL and confirms tools with the expected prefix are discoverable.
- **Cannot run on Kind.** Requires a cluster with a TLS-terminating load balancer and a publicly trusted wildcard certificate (e.g. OpenShift with Let's Encrypt). Previously manually verified on OpenShift 4.20 (see #450).
- **Skips unless** `E2E_HTTPS_REAL_CERTS=true` and `E2E_SCHEME=https`.

### [HTTPS] [Happy] tools/call to internal TLS backend fails without DestinationRule, succeeds with it

- When an MCPServerRegistration with `caCertSecretRef` targets an internal TLS backend, `tools/list` should succeed because the broker connects directly. However, `tools/call` should fail because the gateway forwards plain HTTP to the TLS backend after terminating the client's TLS. After creating a DestinationRule with TLS origination for the same service, `tools/call` should succeed because the gateway re-encrypts traffic to the backend.
- **Runs on PR CI.** Skips if cert-manager or the TLS test server are absent.

Both external tests are tagged `[HTTPS_EXTERNAL]` and skip on PR CI. Run them manually for sanity checks (e.g. before releases):

```bash
# GitHub MCP test (Kind)
GITHUB_MCP_PAT=ghp_your_token make test-e2e-https

# RealCerts test (cluster with real TLS)
E2E_HTTPS_REAL_CERTS=true E2E_SCHEME=https E2E_DOMAIN=your-cluster.example.com make test-e2e-https
```

## Running HTTPS tests

All HTTPS tests are tagged `[HTTPS]` and can be run together:

```bash
make test-e2e-https
```

| Test | PR CI | Manual / release sanity |
|------|-------|-------------------------|
| Private CA + hairpin SNI (happy + negative) | Yes | Yes |
| DestinationRule TLS origination | Yes | Yes |
| GitHub external | Skipped | Yes (needs `GITHUB_MCP_PAT`) |
| Real public certs | Skipped | Yes (needs real TLS cluster) |

### [Happy] OAuth protected resource metadata served from CRD config and reverted after removal

- When an MCPGatewayExtension has `spec.oauthProtectedResource` configured with at least one authorization server, the controller should inject `OAUTH_*` env vars into the broker-router deployment and the `/.well-known/oauth-protected-resource` endpoint should return the configured metadata as JSON. The response should contain `authorization_servers`, `resource`, and `bearer_methods_supported` fields. When `oauthProtectedResource` is then removed from the spec, the controller should remove the OAUTH env vars; after the deployment rolls out, the endpoint should still respond but `authorization_servers` should revert to an empty array (the broker's built-in default when no OAUTH env vars are set). One spec covers the serve and revert halves in a single configure-remove cycle.

### [Happy,UserSpecificList] User sees their own tools merged with cached tools

When a user authenticates and sends `tools/list`, the response includes both the cached tools from standard servers and the user-specific tools from the user-specific-server, with the configured prefix applied.

### [UserSpecificList] Different users get different tool lists

When User A and User B each send `tools/list` in separate sessions, User A's response contains their tools but not User B's, and vice versa. Both responses contain common tools from the user-specific server.

### [UserSpecificList] User-specific tools are prefixed

When a user sends `tools/list` and the user-specific-server has a prefix configured, the tools returned from that server have the prefix applied. Unprefixed versions do not appear.

### [UserSpecificList] Standard servers unaffected by userSpecificList

Adding a user-specific-server does not change the tool list for standard servers. An unauthenticated client does not see user-scoped tools from the user-specific server.

### [UserSpecificList] User-specific server down does not break tools/list

When the user-specific-server is unreachable, `tools/list` still returns tools from all healthy standard servers without error.

### [UserSpecificList] Tool call routing works for user-specific tools

When a user discovers a tool from a user-specific-server via `tools/list` and then sends `tools/call` for that tool, the call is routed correctly to the upstream server and executes successfully.

### [Security,UserSpecificList] Internal headers not forwarded to upstream

When the broker forwards headers to a user-specific upstream, internal gateway headers (`x-mcp-virtualserver`, `x-mcp-authorized`) are stripped. The user's own `Authorization` header is forwarded.

### [UserSpecificList] Virtual server filter applies to user-specific tools

When an MCPVirtualServer is configured that includes a specific user-specific tool, only that tool is returned — user-specific tools are subject to the same virtual server filtering as cached tools.

## Common pitfalls

- MCPServerRegistrations with empty prefix: `strings.HasPrefix(name, "")` matches all tools, including broker meta-tools (discover_tools, select_tools). Always use a non-empty prefix in tests.
