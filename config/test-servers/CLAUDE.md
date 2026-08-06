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
- **Stateless Server**: Go SDK with `Stateless: true` for 2026-07-28 protocol testing (tools: hello_world, headers; prompts: greeting)
- **A2A Server**: Hand-rolled A2A **v1.0** agent (skills: forecast, alerts). Serves an Agent Card at `/.well-known/agent-card.json` — a v1.0 card with `supportedInterfaces` (`JSONRPC` binding, `protocolVersion: 1.0`) built from `AGENT_URL` — and handles the v1.0 JSON-RPC methods `SendMessage`, `SendStreamingMessage`, `GetTask`, `CancelTask`, `SubscribeToTask` with SSE streaming. `SendMessage`/streaming responses are the `SendMessageResponse`/`StreamResponse` oneof (`result.task`/`result.statusUpdate`/`result.artifactUpdate`); `GetTask`/`CancelTask` return a bare `Task`. Message text drives behaviour: "slow" → `TASK_STATE_WORKING` then completed, "fail" → `TASK_STATE_FAILED`, "large"/"image" → adds a heavy file artifact (single on `SendMessage`, chunked over SSE on `SendStreamingMessage`). Completed tasks echo the message and received headers in artifacts for e2e assertions; terminal tasks are swept after `TASK_TTL_MS` so the in-memory store stays bounded. `AUTH_MODE` (`apikey`/`bearer`/`none`) makes the card auth-aware and enforces it. Configurable via `AGENT_NAME`, `AGENT_DESCRIPTION`, `SKILLS`, `AGENT_PREFIX`, `AGENT_URL`, `AUTH_MODE`, `API_KEY`, `OAUTH_TOKEN_URL`, `PORT`, `TASK_DURATION_MS`, `STREAM_DELAY_MS`, `ARTIFACT_BYTES`, `TASK_TTL_MS` env vars.
