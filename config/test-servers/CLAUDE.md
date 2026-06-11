# Test Servers

Test servers in `config/test-servers/`:
- **Server1**: Go SDK (tools: greet, time, slow, headers)
- **Server2**: Go SDK (tools: hello_world, time, headers, auth1234, slow, set_time, pour_chocolate_into_mold)
- **Server3**: Python FastMCP (tools: time, add, dozen, pi, get_weather, slow)
- **API Key Server**: Validates Bearer token authentication (tool: hello_world)
- **Broken Server**: Intentionally broken server for testing error handling
- **Custom Path Server**: Go SDK at `/v1/special/mcp` (tools: echo_custom, path_info, timestamp)
- **OIDC Server**: Validates OpenID Connect (OIDC) Bearer tokens
- **Everything Server**: TypeScript SDK (prompts, tools, resources, sampling)
- **Conformance Server**: TypeScript SDK conformance test server
- **Custom Response Server**: Tests custom response handling
- **TLS Server**: Go SDK with native TLS support (tools: echo_tls, tls_info). Requires cert-manager; deployed via `make deploy-tls-test-server`
- **User-Specific Server**: Go SDK, returns different tools per user based on Authorization header (userSpecificList feature testing)
- **A2A Server**: Hand-rolled A2A v0.3.0 agent (skills: forecast, alerts). Serves an Agent Card at `/.well-known/agent-card.json` (plus legacy `agent.json`), handles `message/send`, `message/stream`, `tasks/get`, `tasks/cancel` with SSE streaming. Message text drives behaviour: "slow" → working then completed, "fail" → failed. Completed tasks echo the message and received headers in artifacts for e2e assertions. Configurable via `AGENT_NAME`, `SKILLS`, `SKILL_PREFIX`, `AGENT_URL`, `TASK_DURATION_MS`, `STREAM_DELAY_MS` env vars
