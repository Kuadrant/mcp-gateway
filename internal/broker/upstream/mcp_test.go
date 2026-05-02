package upstream

import (
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewUpstreamMCP(t *testing.T) {
	testServer := config.MCPServer{
		Name:       "test-server",
		URL:        "http://localhost:8088/mcp",
		ToolPrefix: "",
		Enabled:    true,
		Hostname:   "dummy",
	}
	up := NewUpstreamMCP(&testServer)
	require.NotNil(t, up)
	require.Equal(t, testServer, up.GetConfig())
}

// TestGetConfigPreservesTimeouts is a regression test for an easy-to-miss bug:
// GetConfig() builds a fresh config.MCPServer and must include the Timeouts
// pointer or the router will silently lose its tool-call timeout policy.
func TestGetConfigPreservesTimeouts(t *testing.T) {
	timeouts := &config.ServerTimeouts{
		ToolCall: 7 * time.Second,
		PerTool: map[string]time.Duration{
			"slow_query": 45 * time.Second,
		},
	}
	in := config.MCPServer{
		Name:     "with-timeouts",
		URL:      "http://localhost:8088/mcp",
		Hostname: "dummy",
		Timeouts: timeouts,
	}
	out := NewUpstreamMCP(&in).GetConfig()

	require.NotNil(t, out.Timeouts, "GetConfig must propagate Timeouts")
	require.Equal(t, 7*time.Second, out.Timeouts.ToolCall)
	require.Equal(t, 45*time.Second, out.Timeouts.PerTool["slow_query"])
}
