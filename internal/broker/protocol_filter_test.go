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

type namedItem interface {
	getName() string
}

type namedTool struct{ *mcp.Tool }

func (n namedTool) getName() string { return n.Name }

type namedPrompt struct{ *mcp.Prompt }

func (n namedPrompt) getName() string { return n.Name }

func testProtocolFilter[T namedItem](t *testing.T, label string, lookup func(http.Header) []T, stateful, stateless []string) {
	t.Helper()
	tests := []struct {
		name      string
		header    string
		wantNames []string
	}{
		{"2026 header returns stateless", "2026-07-28", stateless},
		{"no header returns stateful", "", stateful},
		{"2025 header returns stateful", "2025-11-25", stateful},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set("Mcp-Protocol-Version", tt.header)
			}
			got := lookup(headers)
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
		headers := http.Header{}
		headers.Set("Mcp-Protocol-Version", "2026-07-28")
		r1 := lookup(headers)
		n := len(r1)
		_ = append(r1, r1[0])
		r2 := lookup(headers)
		if len(r2) != n {
			t.Errorf("cache was mutated: got %d %s, want %d", len(r2), label, n)
		}
	})
}

func TestToolsForProtocol(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	sf := []*mcp.Tool{{Name: "tool1"}, {Name: "tool2"}}
	sl := []*mcp.Tool{{Name: "tool3"}}
	b.statefulTools.Store(&sf)
	b.statelessTools.Store(&sl)

	testProtocolFilter(t, "tools",
		func(h http.Header) []namedTool {
			got := b.toolsForProtocol(h)
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
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	sf := []*mcp.Prompt{{Name: "prompt1"}, {Name: "prompt2"}}
	sl := []*mcp.Prompt{{Name: "prompt3"}}
	b.statefulPrompts.Store(&sf)
	b.statelessPrompts.Store(&sl)

	testProtocolFilter(t, "prompts",
		func(h http.Header) []namedPrompt {
			got := b.promptsForProtocol(h)
			out := make([]namedPrompt, len(got))
			for i, p := range got {
				out[i] = namedPrompt{p}
			}
			return out
		},
		[]string{"prompt1", "prompt2"}, []string{"prompt3"},
	)
}
