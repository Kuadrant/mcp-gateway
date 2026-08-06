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

func TestRebuildProtocolCaches(t *testing.T) {
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
			name: "tool without kuadrant/id excluded from all sets",
			tools: []upstream.GatewayTool{
				{Tool: mcp.Tool{Name: "tool1", InputSchema: objectSchema, Meta: map[string]any{"other": "value"}}, Handler: upstream.NoopToolHandler},
			},
			serverVersions:     map[config.UpstreamMCPID][]string{},
			wantStatefulCount:  0,
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
			broker.rebuildProtocolCaches()

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

func TestRebuildProtocolCaches_Prompts(t *testing.T) {
	t.Run("mixed servers with edge cases", func(t *testing.T) {
		// 2025-only server, 2026-only server, dual-version server,
		// prompt without kuadrant/id (defaults to stateful),
		// prompt with non-string kuadrant/id (dropped from both sets)
		prompts := []upstream.GatewayPrompt{
			{Prompt: mcp.Prompt{Name: "p25", Meta: map[string]any{"kuadrant/id": "srv25"}}, Handler: upstream.NoopPromptHandler},
			{Prompt: mcp.Prompt{Name: "p26", Meta: map[string]any{"kuadrant/id": "srv26"}}, Handler: upstream.NoopPromptHandler},
			{Prompt: mcp.Prompt{Name: "pdual", Meta: map[string]any{"kuadrant/id": "srvdual"}}, Handler: upstream.NoopPromptHandler},
			{Prompt: mcp.Prompt{Name: "no_id", Meta: map[string]any{"other": "value"}}, Handler: upstream.NoopPromptHandler},
			{Prompt: mcp.Prompt{Name: "bad_id", Meta: map[string]any{"kuadrant/id": 123}}, Handler: upstream.NoopPromptHandler},
		}
		versions := map[config.UpstreamMCPID][]string{
			"srv25":   {"2025-11-25"},
			"srv26":   {"2026-07-28"},
			"srvdual": {"2025-11-25", "2026-07-28"},
		}

		broker := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
		for id, v := range versions {
			broker.serverVersions.Store(id, v)
		}
		broker.gatewayServer.AddPrompts(prompts...)
		broker.rebuildProtocolCaches()

		stateful := broker.statefulPrompts.Load()
		if stateful == nil {
			t.Fatal("statefulPrompts is nil")
		}
		// p25 + pdual = 2 (no_id and bad_id both excluded)
		if len(*stateful) != 2 {
			t.Errorf("stateful count: got %d, want 2", len(*stateful))
		}

		stateless := broker.statelessPrompts.Load()
		if stateless == nil {
			t.Fatal("statelessPrompts is nil")
		}
		// p26 + pdual = 2
		if len(*stateless) != 2 {
			t.Errorf("stateless count: got %d, want 2", len(*stateless))
		}
	})
}

func TestPromptsForProtocol(t *testing.T) {
	broker := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)

	statefulPrompts := []*mcp.Prompt{
		{Name: "prompt1"},
		{Name: "prompt2"},
	}
	statelessPrompts := []*mcp.Prompt{
		{Name: "prompt3"},
	}

	broker.statefulPrompts.Store(&statefulPrompts)
	broker.statelessPrompts.Store(&statelessPrompts)

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
			wantNames: []string{"prompt3"},
		},
		{
			name:      "no header returns stateful",
			header:    "",
			wantCount: 2,
			wantNames: []string{"prompt1", "prompt2"},
		},
		{
			name:      "2025 header returns stateful",
			header:    "2025-11-25",
			wantCount: 2,
			wantNames: []string{"prompt1", "prompt2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set("Mcp-Protocol-Version", tt.header)
			}

			got := broker.promptsForProtocol(headers)
			if len(got) != tt.wantCount {
				t.Fatalf("got %d prompts, want %d", len(got), tt.wantCount)
			}

			for i, name := range tt.wantNames {
				if got[i].Name != name {
					t.Errorf("prompt %d: got name %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}

	t.Run("returns shallow copy", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Mcp-Protocol-Version", "2026-07-28")

		result1 := broker.promptsForProtocol(headers)
		originalLen := len(result1)

		_ = append(result1, &mcp.Prompt{Name: "appended"})

		result2 := broker.promptsForProtocol(headers)
		if len(result2) != originalLen {
			t.Errorf("cache was mutated: got %d prompts, want %d", len(result2), originalLen)
		}
	})
}
