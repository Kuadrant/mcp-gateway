## Guardrails Integration E2E Test Cases

---
test_suite: guardrails_test.go
tags: Happy,Guardrails
---

> **Note:** These tests require either a mock guardrails server (deployed as a test server in `tests/servers/`) or a real NeMo Guardrails instance in the CI environment. A mock server that implements the `v1/guardrail/checks` endpoint with configurable pass/block responses is the recommended approach — it avoids a NeMo dependency in CI and gives deterministic test control.

### [Happy,Guardrails] Global guardrails blocks a dangerous tool call

- When an MCPGatewayExtension is configured with `guardrailsRef` pointing to a guardrails Secret, and a client makes a `tools/call` with arguments that trigger a block (e.g. a destructive SQL command), the gateway should return a JSON-RPC error with a 403 status. The request should never reach the backend MCP server.

### [Happy,Guardrails] Global guardrails allows a safe tool call

- When an MCPGatewayExtension is configured with `guardrailsRef` and a client makes a `tools/call` with arguments that pass the guardrails check, the request should be routed to the backend MCP server and the tool result returned to the client.

### [Happy,Guardrails] Per-server guardrails applies to a specific server

- When an MCPServerRegistration is configured with `guardrailsRef` (and no global guardrails), tool calls to that server should be checked against the guardrails server. Tool calls to other servers without `guardrailsRef` should pass through without a guardrails check.

### [Guardrails] Global and per-server guardrails are additive (same URL)

- When both the MCPGatewayExtension and an MCPServerRegistration reference guardrails Secrets pointing to the same guardrails server URL, the router should merge the `configIDs` from both into a single request. A tool call to that server should be checked against all merged config IDs. If any policy blocks, the call is rejected.

### [Guardrails] Global and per-server guardrails with different URLs

- When the MCPGatewayExtension references guardrails server A and an MCPServerRegistration references guardrails server B, the router should make two separate requests — one to each server. Both must return success for the tool call to proceed. If either blocks, the call is rejected.

### [Guardrails] Guardrails server unreachable — failMode deny

- When the guardrails server is unreachable and the Secret configures `failMode: deny` (or omits it, using the default), tool calls should be rejected with a 503 JSON-RPC error. No request should reach the backend MCP server.

### [Guardrails] Guardrails server unreachable — failMode allow

- When the guardrails server is unreachable and the Secret configures `failMode: allow`, tool calls should proceed to the backend MCP server as if guardrails were not configured.

### [Guardrails] Invalid guardrails Secret — MCPGatewayExtension reports error

- When `guardrailsRef` references a Secret that does not exist, or a Secret without the required label `mcp.kuadrant.io/secret=true`, or a Secret with the wrong type (not `guardrails/external/nemo`), the MCPGatewayExtension should report a status condition with an appropriate error message.

### [Guardrails] Guardrails Secret update triggers config reload

- When the guardrails Secret is updated (e.g. changing `configIDs` or `url`), the controller should detect the change, re-validate, and write the updated config. After propagation, tool calls should be checked against the new guardrails configuration.

### [Guardrails] Server without guardrails unaffected by global config

- When an MCPGatewayExtension has `guardrailsRef` set, servers whose tool calls pass the guardrails check should continue to work identically to the non-guardrails case. Latency aside, the tool result and headers should be unchanged.

### [Guardrails] Elicitation accept response checked by guardrails

- When guardrails are configured and a client sends an elicitation `accept` response carrying token data, the router should check the content against the guardrails server before forwarding to the backend. A blocked response should return a JSON-RPC error.

### [Guardrails] Elicitation decline/cancel bypass guardrails

- When guardrails are configured and a client sends an elicitation `decline` or `cancel` response, the router should forward it to the backend without making a guardrails check.

### [Guardrails] Bearer token sent to guardrails server

- When the guardrails Secret contains a `bearer-token` key, the router should send it as `Authorization: Bearer <token>` on every request to the guardrails server. Requests without a valid token should be rejected by the guardrails server (401).

### [Guardrails] Multiple configIDs evaluated in single request

- When the guardrails Secret specifies multiple `configIDs`, the router should send all of them in a single `config_ids` array to the guardrails server. The guardrails server evaluates all policies — if any policy blocks, the tool call is rejected.
