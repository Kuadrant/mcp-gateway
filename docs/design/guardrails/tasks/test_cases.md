## Guardrails Integration Test Cases

---
test_suite: guardrails_test.go
tags: Happy,Guardrails
---

> **Note:** E2E tests require a mock guardrails server (deployed in `tests/servers/`) implementing `v1/guardrail/checks` with configurable pass/block responses.

## E2E Tests

### [Happy,Guardrails] Global guardrails blocks a dangerous tool call

- When an MCPGatewayExtension is configured with `guardrailsRef` and a client makes a `tools/call` with arguments that trigger a block, the gateway should return a JSON-RPC error with a 403 status. The request should never reach the backend MCP server.

### [Happy,Guardrails] Global guardrails allows a safe tool call

- When an MCPGatewayExtension is configured with `guardrailsRef` and a client makes a `tools/call` with arguments that pass the guardrails check, the request should be routed to the backend and the tool result returned.

### [Happy,Guardrails] Per-server guardrailsConfigIDs merged with global defaults

- When an MCPGatewayExtension has `guardrailsRef` with default config IDs and an MCPServerRegistration specifies additional `guardrailsConfigIDs`, tool calls to that server should be checked with all config IDs merged into a single request. Tool calls to other servers should use only gateway defaults.

### [Guardrails] Per-server guardrailsConfigIDs override when global has no default IDs

- When an MCPGatewayExtension has `guardrailsRef` with empty `configIDs` and an MCPServerRegistration specifies `guardrailsConfigIDs`, tool calls to that server should be checked with only the per-server config IDs. Other servers should not trigger a guardrails check.

### [Guardrails] Guardrails server unreachable — failMode deny

- When the guardrails server is unreachable and `failMode: deny` (default), tool calls should be rejected with a 503 JSON-RPC error.

### [Guardrails] Guardrails server unreachable — failMode allow

- When the guardrails server is unreachable and `failMode: allow`, tool calls should proceed to the backend as if guardrails were not configured.

### [Guardrails] Global guardrails allows tool call that passes check

- When an MCPGatewayExtension has `guardrailsRef` set and a server has no per-server `guardrailsConfigIDs`, a tool call with arguments that pass the global guardrails check should be routed to the backend and the tool result returned.

### [Guardrails] Elicitation accept response checked by guardrails

- When guardrails are configured and a client sends an elicitation `accept` response, the router should check it against the guardrails server. A blocked response should return a JSON-RPC error.

### [Guardrails,Security] Client Authorization header not forwarded to guardrails server

- When guardrails are configured and a client makes an authenticated `tools/call` with an `Authorization` header, the router should not forward the client's `Authorization` header to the guardrails server. The mock guardrails server should receive the check request without any `Authorization` header.

### [Guardrails,Security] Guardrails Secret deletion fails closed

- When an MCPGatewayExtension has `guardrailsRef` and the guardrails Secret is deleted, `GlobalGuardrails` should be cleared from `mcp-gateway-config`, all MCPServerRegistrations should be set to NotReady and removed from the gateway config. Tool calls should fail because no tools are available.

## Controller Integration Tests (envtest)

### guardrailsConfigIDs without global guardrailsRef sets NotReady

- When an MCPServerRegistration specifies `guardrailsConfigIDs` but the MCPGatewayExtension has no `guardrailsRef`, the controller should set the MCPServerRegistration to NotReady with reason `GuardrailsNotConfigured`. When `guardrailsRef` is later added, the server should become Ready.

### Invalid guardrails Secret — MCPGatewayExtension reports error

- When `guardrailsRef` references a Secret that does not exist, lacks the required label, or has the wrong type, the MCPGatewayExtension should report a status condition with an appropriate error.

### Guardrails Secret update triggers config reload

- When the guardrails Secret is updated (e.g. changing `configIDs` or `url`), the controller should detect the change, re-validate, and write the updated config to `mcp-gateway-config`.

### Guardrails Secret deletion clears config and sets all registrations NotReady

- When the guardrails Secret is deleted, the controller should clear `GlobalGuardrails` from `mcp-gateway-config`, set MCPGatewayExtension condition to `GuardrailsSecretNotFound`, and set all MCPServerRegistrations to NotReady. Recreating the Secret should restore normal operation.

## Unit Tests

### Multiple configIDs sent in single request

- When the guardrails config specifies multiple `configIDs`, the transformer should produce a single request body with all IDs in the `config_ids` array.

### Translation failure returns error

- When `params.arguments` is not valid JSON, Transform should return an error. The router should map this to a 400 Decision.Error regardless of `failMode`.

### Elicitation decline/cancel bypass guardrails

- When the router receives an elicitation `decline` or `cancel` response, it should skip the guardrails check and proceed to routing.

### Config ID merging and deduplication

- When gateway defaults and per-server config IDs overlap, the merged slice passed to Transform should be deduplicated.
