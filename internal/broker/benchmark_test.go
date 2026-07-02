package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func makeBenchTools(n int) []*mcp.Tool {
	tools := make([]*mcp.Tool, n)
	for i := range tools {
		tools[i] = &mcp.Tool{
			Name:        fmt.Sprintf("server1_tool-%d", i),
			Description: "a tool for benchmarking",
			Meta: mcp.Meta{
				"kuadrant/id":   "server1",
				"broker-source": "test",
				"custom-key":    "custom-value",
			},
		}
	}
	return tools
}

func newBenchBroker() *mcpBrokerImpl {
	return &mcpBrokerImpl{
		logger:    slog.Default(),
		discovery: discoveryConfig{enabled: false},
	}
}

func BenchmarkRemoveGatewayMeta_10(b *testing.B) {
	broker := newBenchBroker()
	b.ReportAllocs()
	for b.Loop() {
		// recreate each iteration since removeGatewayMeta rewrites slice elements
		tools := makeBenchTools(10)
		broker.removeGatewayMeta(tools)
	}
}

func BenchmarkRemoveGatewayMeta_100(b *testing.B) {
	broker := newBenchBroker()
	b.ReportAllocs()
	for b.Loop() {
		tools := makeBenchTools(100)
		broker.removeGatewayMeta(tools)
	}
}

func BenchmarkApplyVirtualServerFilter_10(b *testing.B) {
	broker := newBenchBroker()
	broker.virtualServers = map[string]*config.VirtualServer{
		"test-ns/test-vs": {
			Name:  "test-ns/test-vs",
			Tools: []string{"server1_tool-0", "server1_tool-2", "server1_tool-4"},
		},
	}

	tools := makeBenchTools(10)
	headers := http.Header{
		virtualMCPHeader: []string{"test-ns/test-vs"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		broker.applyVirtualServerFilter(headers, tools)
	}
}

func BenchmarkFilterTools_NoFilters(b *testing.B) {
	broker := newBenchBroker()
	headers := http.Header{}

	b.ReportAllocs()
	for b.Loop() {
		tools := makeBenchTools(50)
		result := &mcp.ListToolsResult{Tools: tools}
		broker.FilterTools(context.Background(), headers, "session-1", result)
	}
}
