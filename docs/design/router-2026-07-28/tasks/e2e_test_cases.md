## E2E Test Cases — Router 2026-07-28

### [Happy,Protocol2026] Test tool call via stateless gateway

Set `protocolMode: Stateless` on MCPGatewayExtension, register an MCP server with a prefix, and verify tools/call routes correctly. Client sends `mcp-method: tools/call` and `mcp-name: <prefix>_<tool>` headers. Upstream receives the tool call with the prefix stripped. Verify the response returns successfully.

### [Happy,Protocol2026] Test tool call with prefix body rewrite

Set `protocolMode: Stateless`, register an MCP server with prefix `s_`. Client sends tools/call with `mcp-name: s_mytool` and body containing `params.name: s_mytool`. Verify upstream receives body with `params.name: mytool` (prefix stripped). Verify `x-mcp-toolname` header is `mytool`.

### [Protocol2026] Test header-body mismatch rejection

Set `protocolMode: Stateless`. Client sends tools/call with `mcp-name: wrong_tool` header but body `params.name: actual_tool`. Verify the gateway returns a JSON-RPC error with code `-32602` and message containing `HeaderMismatch`.

### [Protocol2026] Test unknown tool error

Set `protocolMode: Stateless`. Client sends tools/call with `mcp-name: nonexistent_tool`. Verify the gateway returns a JSON-RPC error with code `-32602` and message `Tool not found`.

### [Happy,Protocol2026] Test prompt get routing

Set `protocolMode: Stateless`, register an MCP server that exposes prompts. Client sends `mcp-method: prompts/get` with `mcp-name: <prefix>_<prompt>`. Verify upstream receives the prompt request with prefix stripped and returns the prompt successfully.

### [Happy] Test default protocolMode preserves existing behavior

Deploy MCPGatewayExtension without specifying `protocolMode` (defaults to `Stateful`). All existing e2e tests pass unchanged — session management, hairpin initialization, and elicitation continue working as before.

### [Protocol2026] Test protocolMode switch triggers deployment update

Update MCPGatewayExtension from `protocolMode: Stateful` to `protocolMode: Stateless`. Verify the mcp-gateway deployment restarts and the container command includes `--protocol-mode=stateless`. Revert to `Stateful` and verify the flag is removed.

### [Happy,Protocol2026] Test non-tool methods route to broker

Set `protocolMode: Stateless`. Client sends `mcp-method: tools/list`. Verify the request routes to the broker and returns the aggregated tool list. Repeat for `mcp-method: server/discover`.

### [Happy,Protocol2026] Test tool annotations header

Set `protocolMode: Stateless`, register an MCP server whose tools have annotation hints. Client sends tools/call for a tool with annotations. Verify the `x-mcp-annotation-hints` header is set on the upstream request with the correct format.
