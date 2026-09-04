package routing

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── Tool-level errors ───────────────────────────────────────────────────
// These build a JSON-RPC *result* with isError:true. The HTTP request was
// valid and the gateway understood it, but the tool execution could not be
// fulfilled (e.g. tool not found, token resolution failed).

// BuildSSEToolExecutionError constructs an SSE JSON-RPC result indicating
// tool execution failure for the 2025-11-05 (SSE/streamable-HTTP) protocol.
func BuildSSEToolExecutionError(requestID any, message string) string {
	return SseJSONRPC(requestID, func(b *strings.Builder) {
		b.WriteString(",\"result\":{\"content\":[{\"type\":\"text\",\"text\":")
		b.WriteString(jsonQuote(message))
		b.WriteString("}],\"isError\":true}}")
	})
}

// BuildJSONToolExecutionError constructs a plain JSON-RPC result indicating
// tool execution failure for the 2026-07-28 (HTTP-only) protocol.
func BuildJSONToolExecutionError(requestID any, message string) string {
	var b strings.Builder
	b.WriteString("{\"jsonrpc\":\"2.0\",\"id\":")
	idBytes, err := json.Marshal(requestID)
	if err != nil {
		b.WriteString("null")
	} else {
		b.Write(idBytes)
	}
	b.WriteString(",\"result\":{\"content\":[{\"type\":\"text\",\"text\":")
	b.WriteString(jsonQuote(message))
	b.WriteString("}],\"isError\":true}}")
	return b.String()
}

// ── Protocol-level rejections ───────────────────────────────────────────
// These build a JSON-RPC *error* object. The request is rejected before it
// reaches any upstream server (e.g. invalid method, unknown prompt).

// BuildSSEProtocolRejection constructs an SSE JSON-RPC error response for
// the 2025-11-05 (SSE/streamable-HTTP) protocol. code is the JSON-RPC
// error code (e.g. -32602 for "Invalid params").
func BuildSSEProtocolRejection(requestID any, code int, message string) string {
	return SseJSONRPC(requestID, func(b *strings.Builder) {
		fmt.Fprintf(b, ",\"error\":{\"code\":%d,\"message\":%s}}", code, jsonQuote(message))
	})
}

// BuildJSONProtocolRejection constructs a plain JSON-RPC error response
// for the 2026-07-28 (HTTP-only) protocol. code is the JSON-RPC error
// code (e.g. -32602 for "Invalid params").
func BuildJSONProtocolRejection(requestID any, code int, message string) string {
	var b strings.Builder
	b.WriteString("{\"jsonrpc\":\"2.0\",\"id\":")
	idBytes, err := json.Marshal(requestID)
	if err != nil {
		b.WriteString("null")
	} else {
		b.Write(idBytes)
	}
	fmt.Fprintf(&b, ",\"error\":{\"code\":%d,\"message\":%s}}", code, jsonQuote(message))
	return b.String()
}

// ── Helpers ─────────────────────────────────────────────────────────────

// SseJSONRPC constructs sse json-rpc event with custom body
func SseJSONRPC(requestID any, writeBody func(b *strings.Builder)) string {
	var b strings.Builder
	b.WriteString("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":")
	idBytes, err := json.Marshal(requestID)
	if err != nil {
		b.WriteString("null")
	} else {
		b.Write(idBytes)
	}
	writeBody(&b)
	b.WriteString("\n\n")
	return b.String()
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
