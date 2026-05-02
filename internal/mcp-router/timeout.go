package mcprouter

import (
	"encoding/json"
	"fmt"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// JSON-RPC application-level error code returned to clients when the gateway aborts
// a tool call after the configured timeout elapses. The MCP spec reserves the
// -32000..-32099 range for implementation-defined server errors.
const jsonRPCToolTimeoutCode = -32001

// timeoutSourceFor reports where the effective timeout for a tool came from so it
// can be attached to spans and logs without re-doing the resolution.
//   - "perTool": a perTool override matched the tool
//   - "server":  the server-wide ToolCall default applied
//   - "":        no timeout was configured (caller should not record this)
func timeoutSourceFor(t *config.ServerTimeouts, upstreamToolName string) string {
	if t == nil {
		return ""
	}
	if d, ok := t.PerTool[upstreamToolName]; ok && d > 0 {
		return "perTool"
	}
	if t.ToolCall > 0 {
		return "server"
	}
	return ""
}

// buildToolTimeoutSSEEvent returns an SSE-formatted JSON-RPC error event that the
// router substitutes for an upstream 504 Gateway Timeout response. The format
// matches existing tool-call SSE responses produced by HandleToolCall.
//
// id matches the request ID so MCP clients can correlate the error with the
// original tools/call invocation. timeoutMS is included in the structured `data`
// field so operators can observe the enforced bound from client telemetry.
func buildToolTimeoutSSEEvent(id any, toolName string, timeoutMS int64) []byte {
	data := map[string]any{
		"timeoutMs": timeoutMS,
		"tool":      toolName,
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    jsonRPCToolTimeoutCode,
			"message": fmt.Sprintf("Tool call %q timed out after %dms (gateway timeout policy)", toolName, timeoutMS),
			"data":    data,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		// Marshalling a map with primitive values cannot realistically fail. Fall back
		// to a static minimal payload so the client still receives a JSON-RPC error.
		encoded = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"tool call timed out"}}`)
	}
	return []byte("event: message\ndata: " + string(encoded) + "\n\n")
}
