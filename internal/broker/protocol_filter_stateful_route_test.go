package broker

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// TestBroker_serverServesOnStatefulRoute verifies that an upstream advertising
// any 2025-series protocol revision is routed onto the stateful set, so that
// upstreams pinned to an older MCP SDK (which downgrade to 2025-03-26 /
// 2025-06-18 rather than the requested 2025-11-25) are no longer silently
// dropped from every served set.
func TestBroker_serverServesOnStatefulRoute(t *testing.T) {
	cases := []struct {
		name     string
		versions []string
		want     bool
	}{
		{"exact 2025-11-25 (broker-requested)", []string{"2025-11-25"}, true},
		{"older 2025-03-26 (e.g. SDK 1.11 upstream)", []string{"2025-03-26"}, true},
		{"older 2025-06-18", []string{"2025-06-18", "2025-03-26"}, true},
		{"2026 only is stateless route, not stateful", []string{"2026-07-28"}, false},
		{"2024 only is pre-stateful family", []string{"2024-11-05"}, false},
		{"mixed 2024 + 2025 still qualifies", []string{"2024-11-05", "2025-03-26"}, true},
		{"no versions reported yet", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
			id := config.UpstreamMCPID("srv")
			broker.mcpServers[id] = &mockActiveServer{supportedVersions: tc.versions}

			if got := broker.serverServesOnStatefulRoute(id); got != tc.want {
				t.Errorf("serverServesOnStatefulRoute(%v) = %v, want %v", tc.versions, got, tc.want)
			}
		})
	}

	// An unknown server must not qualify.
	broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
	if broker.serverServesOnStatefulRoute("unknown") {
		t.Error("serverServesOnStatefulRoute(unknown) = true, want false")
	}
}
