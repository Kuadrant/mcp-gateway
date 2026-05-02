package config

import (
	"testing"
	"time"
)

func TestServerTimeoutsResolveToolCallTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeouts  *ServerTimeouts
		tool      string
		wantD     time.Duration
		wantFound bool
	}{
		{
			name:      "nil timeouts returns no override",
			timeouts:  nil,
			tool:      "anything",
			wantD:     0,
			wantFound: false,
		},
		{
			name:      "no defaults and no overrides",
			timeouts:  &ServerTimeouts{},
			tool:      "search",
			wantD:     0,
			wantFound: false,
		},
		{
			name: "server-wide default applies when no override",
			timeouts: &ServerTimeouts{
				ToolCall: 5 * time.Second,
			},
			tool:      "search",
			wantD:     5 * time.Second,
			wantFound: true,
		},
		{
			name: "per-tool override wins over default",
			timeouts: &ServerTimeouts{
				ToolCall: 5 * time.Second,
				PerTool: map[string]time.Duration{
					"slow_query": 30 * time.Second,
				},
			},
			tool:      "slow_query",
			wantD:     30 * time.Second,
			wantFound: true,
		},
		{
			name: "non-matching per-tool falls through to default",
			timeouts: &ServerTimeouts{
				ToolCall: 5 * time.Second,
				PerTool: map[string]time.Duration{
					"slow_query": 30 * time.Second,
				},
			},
			tool:      "fast_lookup",
			wantD:     5 * time.Second,
			wantFound: true,
		},
		{
			name: "zero per-tool entry is ignored, falls back to default",
			timeouts: &ServerTimeouts{
				ToolCall: 5 * time.Second,
				PerTool: map[string]time.Duration{
					"weird_zero": 0,
				},
			},
			tool:      "weird_zero",
			wantD:     5 * time.Second,
			wantFound: true,
		},
		{
			name: "per-tool only, matched",
			timeouts: &ServerTimeouts{
				PerTool: map[string]time.Duration{
					"only_tool": 2 * time.Second,
				},
			},
			tool:      "only_tool",
			wantD:     2 * time.Second,
			wantFound: true,
		},
		{
			name: "per-tool only, unmatched",
			timeouts: &ServerTimeouts{
				PerTool: map[string]time.Duration{
					"only_tool": 2 * time.Second,
				},
			},
			tool:      "other",
			wantD:     0,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotD, gotFound := tt.timeouts.ResolveToolCallTimeout(tt.tool)
			if gotD != tt.wantD {
				t.Errorf("duration = %v, want %v", gotD, tt.wantD)
			}
			if gotFound != tt.wantFound {
				t.Errorf("found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}

func TestMCPServerConfigChangedDetectsTimeoutDrift(t *testing.T) {
	base := MCPServer{
		Name:     "ns/example",
		Hostname: "example.mcp.local",
		Timeouts: &ServerTimeouts{
			ToolCall: 10 * time.Second,
			PerTool:  map[string]time.Duration{"slow": 30 * time.Second},
		},
	}

	tests := []struct {
		name string
		next MCPServer
		want bool
	}{
		{
			name: "identical timeouts: no change",
			next: MCPServer{
				Name:     base.Name,
				Hostname: base.Hostname,
				Timeouts: &ServerTimeouts{
					ToolCall: 10 * time.Second,
					PerTool:  map[string]time.Duration{"slow": 30 * time.Second},
				},
			},
			want: false,
		},
		{
			name: "different default duration",
			next: MCPServer{
				Name:     base.Name,
				Hostname: base.Hostname,
				Timeouts: &ServerTimeouts{
					ToolCall: 20 * time.Second,
					PerTool:  map[string]time.Duration{"slow": 30 * time.Second},
				},
			},
			want: true,
		},
		{
			name: "different per-tool override value",
			next: MCPServer{
				Name:     base.Name,
				Hostname: base.Hostname,
				Timeouts: &ServerTimeouts{
					ToolCall: 10 * time.Second,
					PerTool:  map[string]time.Duration{"slow": 60 * time.Second},
				},
			},
			want: true,
		},
		{
			name: "added per-tool override",
			next: MCPServer{
				Name:     base.Name,
				Hostname: base.Hostname,
				Timeouts: &ServerTimeouts{
					ToolCall: 10 * time.Second,
					PerTool: map[string]time.Duration{
						"slow":  30 * time.Second,
						"extra": 5 * time.Second,
					},
				},
			},
			want: true,
		},
		{
			name: "removed timeouts entirely",
			next: MCPServer{
				Name:     base.Name,
				Hostname: base.Hostname,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.next.ConfigChanged(base)
			if got != tt.want {
				t.Errorf("ConfigChanged = %v, want %v", got, tt.want)
			}
		})
	}
}
