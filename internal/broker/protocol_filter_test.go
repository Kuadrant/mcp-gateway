package broker

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var objectSchema = map[string]any{"type": "object"}

func TestComputeGatewaySupportedVersions(t *testing.T) {
	tests := []struct {
		name     string
		versions map[config.UpstreamMCPID][]string
		want     []string
	}{
		{
			name:     "empty serverVersions returns nil",
			versions: map[config.UpstreamMCPID][]string{},
			want:     nil,
		},
		{
			name: "single server with 2025",
			versions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25"},
			},
			want: []string{"2025-11-25"},
		},
		{
			name: "single server with 2026",
			versions: map[config.UpstreamMCPID][]string{
				"server1": {"2026-07-28"},
			},
			want: []string{"2026-07-28"},
		},
		{
			name: "two servers different versions",
			versions: map[config.UpstreamMCPID][]string{
				"server1": {"2026-07-28"},
				"server2": {"2025-11-25"},
			},
			want: []string{"2025-11-25", "2026-07-28"},
		},
		{
			name: "two servers overlapping versions",
			versions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25", "2026-07-28"},
				"server2": {"2025-11-25"},
			},
			want: []string{"2025-11-25", "2026-07-28"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
			for id, versions := range tt.versions {
				broker.serverVersions.Store(id, versions)
			}

			got := broker.computeGatewaySupportedVersions()
			if len(got) != len(tt.want) {
				t.Fatalf("got %d versions, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRebuildProtocolToolCache(t *testing.T) {
	tests := []struct {
		name               string
		tools              []upstream.GatewayTool
		serverVersions     map[config.UpstreamMCPID][]string
		wantStatefulCount  int
		wantStatelessCount int
	}{
		{
			name: "all 2025 server tools",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
				{Tool: mcp.Tool{Name: "tool2", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25"},
			},
			wantStatefulCount:  2,
			wantStatelessCount: 0,
		},
		{
			name: "all 2026 server tools",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
				{Tool: mcp.Tool{Name: "tool2", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2026-07-28"},
			},
			wantStatefulCount:  0,
			wantStatelessCount: 2,
		},
		{
			name: "mixed servers",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
				{Tool: mcp.Tool{Name: "tool2", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server2"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25"},
				"server2": {"2026-07-28"},
			},
			wantStatefulCount:  1,
			wantStatelessCount: 1,
		},
		{
			name: "broker meta-tool",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "discover_tools", InputSchema: objectSchema, Meta: map[string]any{brokerToolMetaKey: true}}, Handler: upstream.NoopToolHandler},
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2026-07-28"},
			},
			wantStatefulCount:  1,
			wantStatelessCount: 1,
		},
		{
			name: "tool with non-string kuadrant/id",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": 123}}, Handler: upstream.NoopToolHandler},
				{Tool: mcp.Tool{Name: "tool2", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25"},
			},
			wantStatefulCount:  1,
			wantStatelessCount: 0,
		},
		{
			name: "server supports both versions",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"kuadrant/id": "server1"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions: map[config.UpstreamMCPID][]string{
				"server1": {"2025-11-25", "2026-07-28"},
			},
			wantStatefulCount:  1,
			wantStatelessCount: 1,
		},
		{
			name: "tool without kuadrant/id",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"other": "value"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions:     map[config.UpstreamMCPID][]string{},
			wantStatefulCount:  1,
			wantStatelessCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
			for id, versions := range tt.serverVersions {
				broker.serverVersions.Store(id, versions)
			}

			broker.gatewayServer.AddTools(tt.tools...)
			broker.rebuildProtocolToolCache()

			stateful := broker.statefulTools.Load()
			if stateful == nil {
				t.Fatal("statefulTools is nil")
			}
			if len(*stateful) != tt.wantStatefulCount {
				t.Errorf("stateful count: got %d, want %d", len(*stateful), tt.wantStatefulCount)
			}

			stateless := broker.statelessTools.Load()
			if stateless == nil && tt.wantStatelessCount > 0 {
				t.Fatal("statelessTools is nil but expected tools")
			}
			if stateless != nil && len(*stateless) != tt.wantStatelessCount {
				t.Errorf("stateless count: got %d, want %d", len(*stateless), tt.wantStatelessCount)
			}
		})
	}
}

func TestToolsForProtocol(t *testing.T) {
	broker := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)

	statefulTools := []*mcp.Tool{
		{Name: "tool1"},
		{Name: "tool2"},
	}
	statelessTools := []*mcp.Tool{
		{Name: "tool3"},
	}

	broker.statefulTools.Store(&statefulTools)
	broker.statelessTools.Store(&statelessTools)

	tests := []struct {
		name      string
		header    string
		wantCount int
		wantNames []string
	}{
		{
			name:      "2026 header returns stateless",
			header:    "2026-07-28",
			wantCount: 1,
			wantNames: []string{"tool3"},
		},
		{
			name:      "no header returns stateful",
			header:    "",
			wantCount: 2,
			wantNames: []string{"tool1", "tool2"},
		},
		{
			name:      "2025 header returns stateful",
			header:    "2025-11-25",
			wantCount: 2,
			wantNames: []string{"tool1", "tool2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set("Mcp-Protocol-Version", tt.header)
			}

			got := broker.toolsForProtocol(headers)
			if len(got) != tt.wantCount {
				t.Fatalf("got %d tools, want %d", len(got), tt.wantCount)
			}

			for i, name := range tt.wantNames {
				if got[i].Name != name {
					t.Errorf("tool %d: got name %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}

	t.Run("returns shallow copy", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Mcp-Protocol-Version", "2026-07-28")

		result1 := broker.toolsForProtocol(headers)
		originalLen := len(result1)

		_ = append(result1, &mcp.Tool{Name: "appended"})

		result2 := broker.toolsForProtocol(headers)
		if len(result2) != originalLen {
			t.Errorf("cache was mutated: got %d tools, want %d", len(result2), originalLen)
		}
	})
}
