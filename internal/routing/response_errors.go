package routing

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuildSSEToolExecutionError builds an SSE tool result with isError:true.
func BuildSSEToolExecutionError(requestID any, message string) string {
	return SseJSONRPC(requestID, func(b *strings.Builder) {
		b.WriteString(",\"result\":{\"content\":[{\"type\":\"text\",\"text\":")
		b.WriteString(jsonQuote(message))
		b.WriteString("}],\"isError\":true}}")
	})
}

// BuildJSONToolExecutionError builds a plain JSON tool result with isError:true.
// Both protocol versions can return application/json responses.
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

// BuildSSEProtocolRejection builds an SSE JSON-RPC error with the given code.
func BuildSSEProtocolRejection(requestID any, code int, message string) string {
	return SseJSONRPC(requestID, func(b *strings.Builder) {
		fmt.Fprintf(b, ",\"error\":{\"code\":%d,\"message\":%s}}", code, jsonQuote(message))
	})
}

// BuildJSONProtocolRejection builds a plain JSON-RPC error with the given code.
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

// SseJSONRPC builds an SSE message event; writeBody must close the JSON object.
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
