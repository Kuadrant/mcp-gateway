package mcprouter

import (
	"encoding/json"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

// benchmark payloads matching real-world mcp traffic
var (
	toolCallPayload = []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"server1_get-weather","arguments":{"city":"Dublin"}}}`)
	initPayload     = []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{}},"clientInfo":{"name":"test","version":"1.0"}}}`)

	benchHeaders = &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "mcp-session-id", Value: "test-session-id"},
			{Key: ":authority", Value: "gateway.example.com"},
			{Key: ":path", Value: "/mcp"},
			{Key: ":method", Value: "POST"},
		},
	}
)

func BenchmarkMCPRequestParse_ToolCall(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var req MCPRequest
		if err := json.Unmarshal(toolCallPayload, &req); err != nil {
			b.Fatal(err)
		}
		req.Headers = benchHeaders
	}
}

func BenchmarkMCPRequestParse_Initialize(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var req MCPRequest
		if err := json.Unmarshal(initPayload, &req); err != nil {
			b.Fatal(err)
		}
		req.Headers = benchHeaders
	}
}

func BenchmarkMCPRequestValidate(b *testing.B) {
	var req MCPRequest
	_ = json.Unmarshal(toolCallPayload, &req)
	req.Headers = benchHeaders

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := req.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMCPRequestToolName(b *testing.B) {
	var req MCPRequest
	_ = json.Unmarshal(toolCallPayload, &req)
	req.Headers = benchHeaders

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		name := req.ToolName()
		if name == "" {
			b.Fatal("expected tool name")
		}
	}
}

func BenchmarkMCPRequestParseAndRoute(b *testing.B) {
	// full hot path: unmarshal + validate + extract tool name + serialise
	b.ReportAllocs()
	for b.Loop() {
		var req MCPRequest
		if err := json.Unmarshal(toolCallPayload, &req); err != nil {
			b.Fatal(err)
		}
		req.Headers = benchHeaders
		if _, err := req.Validate(); err != nil {
			b.Fatal(err)
		}
		_ = req.ToolName()
		if _, err := req.ToBytes(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetSingleValueHeader(b *testing.B) {
	headers := &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{
			{Key: "content-type", RawValue: []byte("application/json")},
			{Key: "mcp-session-id", RawValue: []byte("abc-123")},
			{Key: "authorization", RawValue: []byte("Bearer token")},
			{Key: ":authority", RawValue: []byte("gateway.example.com")},
			{Key: ":path", RawValue: []byte("/mcp")},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v := getSingleValueHeader(headers, "mcp-session-id")
		if v == "" {
			b.Fatal("expected header value")
		}
	}
}

func BenchmarkMCPRequestElicitationCheck(b *testing.B) {
	var req MCPRequest
	_ = json.Unmarshal(initPayload, &req)
	req.Headers = benchHeaders

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = req.clientSupportsElicitation()
	}
}
