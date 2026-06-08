## E2E Test Cases

### Test Server: user-specific-server

A test MCP server (`tests/servers/user-specific-server/`) that returns different tool sets based on the requesting user's session. The server inspects the `Authorization` header and returns tools scoped to that identity:

- **User A** (`Bearer user-a-token`): tools `list_repos`, `create_issue` (+ common tools)
- **User B** (`Bearer user-b-token`): tools `run_pipeline` (+ common tools)
- **No auth / unknown**: common tools only (`server_info`, `headers`)

Common tools visible to all users: `server_info` (returns server identity and detected user), `headers` (echoes all received HTTP headers — useful for verifying header forwarding).

Deployed alongside existing test servers with its own Deployment, Service, HTTPRoute, and MCPServerRegistration (with `userSpecificList: Enabled`).

### [Happy,UserSpecificList] User sees their own tools merged with cached tools

When a user authenticates and sends `tools/list`, the response includes both the cached tools from standard servers (e.g. server1, server2) and the user-specific tools from the user-specific-server. User A sees `list_repos` and `create_issue` (prefixed) alongside the standard cached tools. User B sees `run_pipeline` (prefixed) alongside the same standard cached tools.

### [UserSpecificList] Different users get different tool lists

When User A and User B each send `tools/list` in separate sessions, User A's response contains `list_repos`, `create_issue` but NOT `run_pipeline`. User B's response contains `run_pipeline` but NOT `list_repos`, `create_issue`. Both responses contain common tools (`server_info`) and tools from standard (non-userSpecificList) servers.

### [UserSpecificList] User-specific tools are prefixed

When a user sends `tools/list` and the user-specific-server has a prefix configured on its MCPServerRegistration, the tools returned from that server have the prefix applied, matching the behavior of cached tools from standard servers.

### [UserSpecificList] Standard servers unaffected by userSpecificList

When no servers have `userSpecificList: Enabled`, the `tools/list` response is identical to the existing behavior. Adding a user-specific-server does not change the tool list for standard servers that have `userSpecificList: Disabled` (the default).

### [UserSpecificList] User-specific server down does not break tools/list

When the user-specific-server is unreachable (e.g. scaled to 0 replicas), `tools/list` still returns tools from all healthy standard servers. The response does not include an error. The broker logs the failure.

### [UserSpecificList] User-specific server tools not in broker cache at startup

When the broker starts with a user-specific-server configured, the service account's tool list from that server does NOT appear in the cached tool list. A `tools/list` request without user auth headers returns only cached tools from standard servers (no tools from the user-specific-server leak into the shared cache).

### [UserSpecificList] Tool call routing still works for user-specific tools

When a user discovers a tool from a user-specific-server via `tools/list` and then sends `tools/call` for that tool, the call is routed correctly to the upstream server with the user's session context. The tool executes successfully.

### [UserSpecificList] Virtual server filter applies to user-specific tools

When an MCPVirtualServer is configured that includes tools from a user-specific-server, the `tools/list` response only includes the tools allowed by the virtual server filter, including both cached and user-specific tools.

### [Security,UserSpecificList] Internal headers not forwarded and upstream errors sanitized

When a user sends `tools/list` against a gateway with a user-specific-server configured, the broker must not forward internal gateway headers to the upstream. Using the user-specific-server's `headers` tool (which echoes all received HTTP headers), call `tools/call` for the `headers` tool and assert: no `x-mcp-*` headers (e.g. `x-mcp-virtualserver`, `x-mcp-authorized`) are present, the service account credential from `credentialRef` is not forwarded — only the user's own `Authorization` header appears. Additionally, when the user-specific-server is configured to return an error (e.g. scaled to 0 or returning 500), the `tools/list` response to the client contains no upstream error details, stack traces, or server hostnames — the failing server is silently omitted and only tools from healthy servers are returned.
