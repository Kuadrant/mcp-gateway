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
