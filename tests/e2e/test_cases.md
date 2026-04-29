## Test Cases


### [Happy] Test registering multiple MCP servers with the gateway

- When a developer creates multiple MCPServerRegistration resources with their corresponding HTTPRoutes, the gateway should register all servers and make their tools available. Each server's tools should be prefixed with the server's toolPrefix to avoid naming conflicts. A tools/list request should return tools from all registered servers.


### [Happy] Test unregistering MCP servers from the gateway

- When an MCPServerRegistration resource is deleted from the cluster, the gateway should remove the server and its tools should no longer be available. A subsequent tools/list request should not include any tools with that server's prefix.


### [Happy] Test invoking tools through the gateway

- When a client calls a tool through the gateway using the prefixed tool name, the gateway should forward the request to the appropriate backend MCP server and return the result. The tool should execute successfully and return the expected response.


### [Happy] Test MCP server registration with credentials

- When an MCPServerRegistration resource references a credential secret, the gateway should use those credentials to authenticate with the backend MCP server. If the credentials are invalid, the server should not be registered and its tools should not be available. When the credentials are updated to valid values, the server should become registered and its tools should appear in the tools/list.


### [Happy] Test backend MCP session reuse

- When a client makes multiple tool calls to the same backend MCP server, the gateway should reuse the same backend session for efficiency. The backend session ID should remain consistent across multiple calls from the same client. When a client disconnects and reconnects, a new backend session should be created.


### [Happy] Test MCPVirtualServer behaves as expected when defined

- When a developer defines an MCPVirtualServer resource and specifies the value of the `X-Mcp-Virtualserver` header as the name in the format `namespace/name`, where the namespace and name come from the created MCPVirtualServer resource, they should only get the tools specified in the MCPVirtualServer resource when they do a tools/list request to the MCP Gateway host.


### [Happy] Test tools are filtered down based on x-authorized-tools header

- When the value of the `x-authorized-tools` header is set as a JWT signed by a trusted key to a set of tools, the MCP Gateway should respond with only tools in that list.


### [Happy] Test notifications are received when a notifications/tools/list_changed notification is sent

- When an MCPServerRegistration is registered with the MCP Gateway, a `notifications/tools/list_changed` should be sent to any clients connected to the MCP Gateway. This notification should work for a single connected client as well as multiple connected clients. They should all receive the same notification at least once. The clients should receive these notifications within one minute of the MCPServerRegistration having reached a ready state.

- When a registered backend MCP Server, emits a `notifications/tools/list_changed` a notification should be received by the connected clients. When the clients receive this notification they should get a changed tools/list. 

### [Happy] Test no two mcp-session-ids are the same

- When a client initializes with the gateway, the session id it receives should be unique. So if two clients connect at basically the same time, each of those clients should get a unique session id.

- If a client is closed and disconnects, if it connects to the gateway and initializes it should receive a new mcp-session-id

### [Happy] Test Hostname backendRef registers MCPServerRegistration

- When an HTTPRoute uses a Hostname backendRef (`kind: Hostname, group: networking.istio.io`) with a URLRewrite filter, and an MCPServerRegistration references that HTTPRoute, the controller should correctly handle the external endpoint configuration and the MCPServerRegistration should become ready. Tool discovery is not tested as it requires actual HTTPS connectivity to external services.

### [Full] Redis session cache persists backend sessions across pod restarts

- When the MCP Gateway is configured with a Redis session cache, backend MCP sessions should survive pod restarts. Redis is already deployed in the mcp-system namespace as part of CI setup. With a single gateway replica, create a secret in the system namespace with a `CACHE_CONNECTION_STRING` key containing the Redis connection string and label `mcp.kuadrant.io/secret: "true"`. Register an MCPServerRegistration and wait for tools to be available. Then enable Redis by patching the MCPGatewayExtension to set `sessionStore.secretName` referencing the Redis secret and waiting for the deployment rollout to complete. Using a raw HTTP client (not the MCP client library), send an `initialize` request to obtain a session ID, then send `notifications/initialized`, then call the `headers` tool to establish a backend session and capture the backend `Mcp-Session-Id` from the response content. Then trigger a rollout restart of the mcp-gateway deployment and wait for the new rollout to complete. After the new pod is running, call the `headers` tool again using the same raw HTTP session ID and verify the same backend `Mcp-Session-Id` is returned, proving the session was retrieved from Redis. Cleanup should remove the `sessionStore` from the MCPGatewayExtension spec and delete the Redis secret. Do not deploy or delete Redis in the test itself.

### [Full] Gracefully handle an MCP Server becoming unavailable

- When a backend MCP Server becomes unavailable, the gateway should no longer show its tools in the tools/list response and a notification should be sent to the client within one minute. When the MCP Server becomes available again, the tools/list should be updated to include the tools again. While unavailable any tools/call should result in a 503 response

### [Happy] MCP Server status

- When a backend MCPServerRegistration is added but the backend MCP is invalid because it doesn't meet the protocol version the status of the MCPServerRegistration resource should report the reason for the MCPSever being invalid

- When a backend MCPServerRegistration is added but the backend MCP is invalid because it has conflicting tools due to tool name overlap with another server that has been added, the status of the MCPServerRegistration resource should report the reason for the MCPSever being invalid

- When a backend MCPServerRegistration is added but the backend MCP is invalid because the broker cannot connect to the the backend MCP server, the MCPServerRegistration resource should report the reason for the MCPSever being invalid

### [Happy] Multiple MCP Servers without prefix

- When two servers with no prefix are used, the gateway sees and forwards both tools correctly.
- When two servers with no prefix conflict and one is then modified to have a specified prefix via the MCPServer resource, both tools should become available via the gateway and capable of being invoked

### [multi-gateway] Multiple Isolated MCP Gateways deployed to the same cluster

- As a platform admin having deployed multiple instances of the MCP Gateway using the MCPGatewayExtension resource, I should see that they become ready once I have created a valid referencegrant. Once the MCPGatewayExtension is valid, there should be a unique deployment of the mcp gateway in the same namespace as the MCPGatewayExtension resources

- As a client, when multiple isolated gateways are ready and available at different hostnames, I should be able to see a unique list of tools for each gateway based on the MCPServerRegistrations created by each team using the MCPGatewayExtension. Example I should see tools prefixed with team_a on one gateway and team_b on the second gateway

### [Happy] MCPGatewayExtension with sectionName targets specific listener

- When an MCPGatewayExtension is created with a valid `targetRef.sectionName` that matches a listener name on the Gateway, the extension should become Ready. The controller should read the listener port and hostname from the Gateway configuration. The EnvoyFilter should be created targeting the correct listener port, and the broker-router deployment should have the `--mcp-gateway-public-host` flag set based on the listener hostname. The Gateway's listener status should be updated with an MCPGatewayExtension condition. The condition should have type "MCPGatewayExtension", status "True", reason "Programmed", and a message indicating which extension and EnvoyFilter are using the listener.

- When an MCPGatewayExtension is deleted, the MCPGatewayExtension condition should be removed from the Gateway's listener status. The Gateway should no longer show the MCPGatewayExtension condition for that listener.

### [Happy] MCPGatewayExtension with invalid sectionName is rejected

- When an MCPGatewayExtension is created with a `targetRef.sectionName` that does not match any listener on the Gateway, the extension should be marked as Invalid with a status message containing "listener not found". No EnvoyFilter or broker-router deployment should be created.

### [Happy] Wildcard listener hostname converted to mcp subdomain

- When a Gateway listener has a wildcard hostname like `*.example.com`, and an MCPGatewayExtension targets that listener without a public host annotation override, the broker-router deployment should have `--mcp-gateway-public-host=mcp.example.com`. The wildcard prefix should be replaced with "mcp".

### [Happy] MCPGatewayExtension rejected when listener allowedRoutes does not permit namespace

- When an MCPGatewayExtension targets a listener that has `allowedRoutes.namespaces.from: Same` and the MCPGatewayExtension is in a different namespace than the Gateway, the extension should be marked as Invalid with a status message indicating the namespace is not allowed.

### [Happy] Second MCPGatewayExtension in same namespace is rejected

- When a namespace already has one MCPGatewayExtension that is Ready, and a second MCPGatewayExtension is created in the same namespace, the second extension should be marked as Invalid with a status message indicating a conflict. Only one MCPGatewayExtension is allowed per namespace, and the oldest by creation timestamp wins.

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

- When an MCPGatewayExtension is created targeting a Gateway listener with a hostname, the controller should automatically create an HTTPRoute named "mcp-gateway-route" in the same namespace as the MCPGatewayExtension. The HTTPRoute should have a parentRef pointing to the target Gateway with the correct sectionName, a hostname matching the listener hostname (or "mcp.<domain>" for wildcard listeners), a PathPrefix match on /mcp, and a backendRef to the mcp-gateway service on port 8080. The HTTPRoute should have an owner reference to the MCPGatewayExtension so it is cleaned up automatically on deletion. MCP clients should be able to reach the gateway at the derived hostname without any manual HTTPRoute creation.

### [Happy] MCPGatewayExtension with httpRouteManagement Disabled skips HTTPRoute creation

- When an MCPGatewayExtension has `spec.httpRouteManagement: Disabled`, the controller should NOT create an HTTPRoute. This allows users to manage their own HTTPRoute with custom configuration (e.g. CORS response headers, additional path rules like /.well-known/oauth-protected-resource). The MCPGatewayExtension should still become Ready and all other resources (Deployment, Service, EnvoyFilter) should be created normally.

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

### [Happy] Elicitation accept flow

- When a client connects to the gateway with an elicitation handler that accepts requests and provides user information, and calls a tool that triggers an elicitation request, the gateway should broker the elicitation between the upstream server and the client. The tool response should indicate that the user provided the requested information.

### [Happy] Elicitation decline flow

- When a client connects to the gateway with an elicitation handler that declines requests, and calls a tool that triggers an elicitation request, the gateway should broker the elicitation between the upstream server and the client. The tool response should indicate that the user declined.

### [Happy] Elicitation without handler errors

- When a client connects to the gateway without an elicitation handler and calls a tool that triggers an elicitation request, the call should result in an error. The error may be a transport error or an error indicated in the tool result.

### [multi-gateway] MCPVirtualServer tool filtering is scoped to the gateway namespace

- When multiple isolated gateways are deployed via MCPGatewayExtensions in separate namespaces, and MCPVirtualServers are created in each namespace with different tool subsets, each gateway should only apply the VirtualServer filtering from its own namespace. A client connecting to Team A's gateway with Team A's VirtualServer header should see only the tools specified in that VirtualServer. Team B's gateway should be unaffected by Team A's VirtualServer — a client connecting without a VirtualServer header should see all of Team B's tools.
