package broker

import (
	"log/slog"
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
			if len(stateful.items) != tt.wantStatefulCount {
				t.Errorf("stateful count: got %d, want %d", len(stateful.items), tt.wantStatefulCount)
			}

			stateless := broker.statelessTools.Load()
			if stateless == nil && tt.wantStatelessCount > 0 {
				t.Fatal("statelessTools is nil but expected tools")
			}
			if stateless != nil && len(stateless.items) != tt.wantStatelessCount {
				t.Errorf("stateless count: got %d, want %d", len(stateless.items), tt.wantStatelessCount)
			}
		})
	}
}

type namedItem interface {
	getName() string
}

type namedTool struct{ *mcp.Tool }

func (n namedTool) getName() string { return n.Name }

type namedPrompt struct{ *mcp.Prompt }

func (n namedPrompt) getName() string { return n.Name }

func testProtocolFilter[T namedItem](t *testing.T, label string, lookup func(bool) []T, stateful, stateless []string) {
	t.Helper()
	tests := []struct {
		name        string
		isStateless bool
		wantNames   []string
	}{
		{"stateless returns 2026 set", true, stateless},
		{"stateful returns 2025 set", false, stateful},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lookup(tt.isStateless)
			if len(got) != len(tt.wantNames) {
				t.Fatalf("got %d %s, want %d", len(got), label, len(tt.wantNames))
			}
			for i, name := range tt.wantNames {
				if got[i].getName() != name {
					t.Errorf("%s %d: got name %q, want %q", label, i, got[i].getName(), name)
				}
			}
		})
	}
	t.Run("returns shallow copy", func(t *testing.T) {
		r1 := lookup(true)
		n := len(r1)
		_ = append(r1, r1[0])
		r2 := lookup(true)
		if len(r2) != n {
			t.Errorf("cache was mutated: got %d %s, want %d", len(r2), label, n)
		}
	})
}

func TestToolsForProtocol(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	sf := protocolCacheEntry[*mcp.Tool]{items: []*mcp.Tool{{Name: "tool1"}, {Name: "tool2"}}}
	sl := protocolCacheEntry[*mcp.Tool]{items: []*mcp.Tool{{Name: "tool3"}}}
	b.statefulTools.Store(&sf)
	b.statelessTools.Store(&sl)

	testProtocolFilter(t, "tools",
		func(isStateless bool) []namedTool {
			got := b.toolsForProtocol(isStateless)
			out := make([]namedTool, len(got))
			for i, tool := range got {
				out[i] = namedTool{tool}
			}
			return out
		},
		[]string{"tool1", "tool2"}, []string{"tool3"},
	)
}

func TestRebuildProtocolCaches_Prompts(t *testing.T) {
	t.Run("mixed servers with edge cases", func(t *testing.T) {
		// 2025-only server, 2026-only server, dual-version server,
		// prompt without kuadrant/id (excluded from all sets),
		// prompt with non-string kuadrant/id (excluded from all sets)
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
		if len(stateful.items) != 2 {
			t.Errorf("stateful count: got %d, want 2", len(stateful.items))
		}

		stateless := broker.statelessPrompts.Load()
		if stateless == nil {
			t.Fatal("statelessPrompts is nil")
		}
		// p26 + pdual = 2
		if len(stateless.items) != 2 {
			t.Errorf("stateless count: got %d, want 2", len(stateless.items))
		}
	})
}

func TestPromptsForProtocol(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	sf := protocolCacheEntry[*mcp.Prompt]{items: []*mcp.Prompt{{Name: "prompt1"}, {Name: "prompt2"}}}
	sl := protocolCacheEntry[*mcp.Prompt]{items: []*mcp.Prompt{{Name: "prompt3"}}}
	b.statefulPrompts.Store(&sf)
	b.statelessPrompts.Store(&sl)

	testProtocolFilter(t, "prompts",
		func(isStateless bool) []namedPrompt {
			got := b.promptsForProtocol(isStateless)
			out := make([]namedPrompt, len(got))
			for i, p := range got {
				out[i] = namedPrompt{p}
			}
			return out
		},
		[]string{"prompt1", "prompt2"}, []string{"prompt3"},
	)
}
