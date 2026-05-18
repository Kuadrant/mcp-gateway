## E2E Test Cases

### [Happy,Auditing] Full correlation context

When a client sends a tool call with `traceparent`, `baggage` (containing `user.id=test-user,agent.id=test-agent`), and `mcp-session-id`, the access log entry should contain all three correlation IDs plus tool name and server name. This proves the full chain is joinable across agent workflow, MCP session, and individual request levels.

### [Auditing] Multiple tool calls in one session

When a client makes 3 tool calls in the same MCP session, all 3 access log entries should share the same `mcp_session_id` but have distinct `request_id` values. This proves session-level grouping works for audit queries.

### [Auditing] No correlation headers

When a client sends a bare tool call with no baggage and no traceparent, the audit log should still capture tool name, server name, method, and Envoy-generated request ID. This proves the audit trail is useful without client cooperation.

### [Auditing] Parameter logging opt-in

When `spec.audit.parameterLogging` is `Enabled` on the MCPGatewayExtension, tool call arguments should appear in the `mcp_tool_params` field of the access log. When `parameterLogging` is `Disabled` or `spec.audit` is not set, the field should be `-`.

### [Auditing] Auth-layer identity fallback

When a client sends an auth-layer header (e.g., `x-forwarded-email: test@example.com`) but no baggage, `mcp_user_id` in the access log should be populated from the fallback header. The fallback order is controlled by `spec.audit.identityHeaders`.

### [Auditing] Baggage with special characters

When a client sends a baggage header with URL-encoded special characters including control characters (e.g., baggage containing `user.id` with URL-encoded CR/LF characters), the decoded values in the audit log should have control characters stripped. This proves baggage sanitization prevents header injection.

### [Auditing] Parameter truncation at 1KB

When `spec.audit.parameterLogging` is `Enabled` and a tool call includes arguments exceeding 1KB, the `mcp_tool_params` field in the access log should be truncated to 1KB. This proves the truncation boundary is enforced.
