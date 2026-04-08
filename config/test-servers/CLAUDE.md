# Test Servers

Test servers in `config/test-servers/`:
- **Server1**: Go SDK (tools: greet, time, slow, headers)
- **Server2**: Go SDK (tools: hello_world, time, headers, auth1234, slow)
- **Server3**: Python FastMCP (tools: time, add, dozen, pi, get_weather, slow)
- **API Key Server**: Validates Bearer token authentication (tool: hello_world)
- **Broken Server**: Intentionally broken server for testing error handling
- **Custom Path Server**: Go SDK at `/v1/special/mcp` (tools: echo_custom, path_info, timestamp)
- **OIDC Server**: Validates OpenID Connect (OIDC) Bearer tokens
- **Everything Server**: TypeScript SDK (prompts, tools, resources, sampling)
- **Conformance Server**: TypeScript SDK conformance test server
- **Custom Response Server**: Tests custom response handling
