package routing

import (
	"encoding/json"
	"fmt"
	"testing"

	"k8s.io/utils/ptr"
)

// benchmark payloads matching real-world mcp traffic
var (
	toolCallPayload = []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"server1_get-weather","arguments":{"city":"Dublin"}}}`)
	initPayload     = []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{}},"clientInfo":{"name":"test","version":"1.0"}}}`)

	benchHeaders = map[string]string{
		"mcp-session-id": "test-session-id",
		":authority":     "gateway.example.com",
		":path":          "/mcp",
		":method":        "POST",
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
	headers := map[string]string{
		"content-type":   "application/json",
		"mcp-session-id": "abc-123",
		"authorization":  "Bearer token",
		":authority":     "gateway.example.com",
		":path":          "/mcp",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v := headers["mcp-session-id"]
		if v == "" {
			b.Fatal("expected header value")
		}
	}
}

func buildBenchTable(nServers, toolsPerServer int) *Table {
	tb := NewTableBuilder()
	for s := range nServers {
		prefix := fmt.Sprintf("s%d_", s)
		route := &ServerRoute{
			Name:   fmt.Sprintf("server-%d", s),
			Host:   fmt.Sprintf("server-%d.mcp.local", s),
			Prefix: prefix,
			Path:   "/mcp",
		}
		for t := range toolsPerServer {
			name := fmt.Sprintf("%stool_%d", prefix, t)
			tb.AddTool(name, route)
			tb.AddAnnotation(fmt.Sprintf("server-%d:%s:", s, prefix), name, &ToolAnnotation{
				ReadOnlyHint: ptr.To(true),
			})
		}
		tb.AddPrompt(fmt.Sprintf("%sprompt_0", prefix), route)
	}
	tb.AddBrokerTool("discover_tools")
	tb.AddBrokerTool("select_tools")
	return tb.Build()
}

func BenchmarkRoutingTableBuild(b *testing.B) {
	for _, tc := range []struct {
		name    string
		servers int
		tools   int
	}{
		{"5servers_10tools", 5, 10},
		{"20servers_50tools", 20, 50},
		{"100servers_100tools", 100, 100},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buildBenchTable(tc.servers, tc.tools)
			}
		})
	}
}

func BenchmarkRoutingTableLookupTool(b *testing.B) {
	table := buildBenchTable(20, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = table.LookupTool("s10_tool_25")
	}
}

func BenchmarkRoutingTableLookupTool_Miss(b *testing.B) {
	table := buildBenchTable(20, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = table.LookupTool("nonexistent_tool")
	}
}

func BenchmarkRoutingTableLookupPrefix(b *testing.B) {
	tb := NewTableBuilder()
	for s := range 20 {
		prefix := fmt.Sprintf("s%d_", s)
		tb.AddPrefix(prefix, &ServerRoute{
			Name:             fmt.Sprintf("server-%d", s),
			Host:             fmt.Sprintf("server-%d.mcp.local", s),
			Prefix:           prefix,
			Path:             "/mcp",
			UserSpecificList: true,
		})
	}
	table := tb.Build()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = table.LookupPrefix("s15_user_specific_tool")
	}
}

func BenchmarkRoutingTableIsBrokerTool(b *testing.B) {
	table := buildBenchTable(20, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = table.IsBrokerTool("discover_tools")
	}
}

func BenchmarkRoutingTableAnnotations(b *testing.B) {
	table := buildBenchTable(20, 50)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = table.ToolAnnotations("server-10:s10_:", "s10_tool_25")
	}
}

func BenchmarkMCPRequestElicitationCheck(b *testing.B) {
	var req MCPRequest
	_ = json.Unmarshal(initPayload, &req)
	req.Headers = benchHeaders

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = req.ClientSupportsElicitation()
	}
}
