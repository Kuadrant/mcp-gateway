package mcprouter

import (
	"encoding/json"
	"fmt"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
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
// router substitutes when Envoy returns 504 due to an enforced rq_timeout (UT /
// upstream_rq_timeout). The format matches existing tool-call SSE responses produced by HandleToolCall.
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

// envoyMarkedUpstreamRequestTimeout is true when Envoy attributes the response to an
// upstream request timeout it enforced (per-router rq_timeout), as opposed to an HTTP
// 504 produced by the upstream application (which must not be rewritten as -32001).
//
// See https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/router_filter#x-envoy-response-flags
// (flag UT — Upstream request timeout) and x-envoy-response-code-details.
func envoyMarkedUpstreamRequestTimeout(headers *corev3.HeaderMap) bool {
	if headers == nil {
		return false
	}
	flags := getSingleValueHeader(headers, "x-envoy-response-flags")
	if strings.Contains(flags, "UT") {
		return true
	}
	details := strings.ToLower(getSingleValueHeader(headers, "x-envoy-response-code-details"))
	return strings.Contains(details, "upstream_rq_timeout") ||
		strings.Contains(details, "upstream_request_timeout")
}
