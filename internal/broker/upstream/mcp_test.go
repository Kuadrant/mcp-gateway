package upstream

import (
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewUpstreamMCP(t *testing.T) {
	testServer := config.MCPServer{
		Name:     "test-server",
		URL:      "http://localhost:8088/mcp",
		Prefix:   "",
		State:    string(mcpv1alpha1.ServerStateEnabled),
		Hostname: "dummy",
	}
	up := NewUpstreamMCP(&testServer)
	require.NotNil(t, up)
	require.Equal(t, testServer, up.GetConfig())
}

func TestMCPServer_IsEnabled(t *testing.T) {
	testCases := []struct {
		name     string
		state    string
		expected bool
	}{
		{
			name:     "empty state defaults to enabled",
			state:    "",
			expected: true,
		},
		{
			name:     "Enabled state returns true",
			state:    string(mcpv1alpha1.ServerStateEnabled),
			expected: true,
		},
		{
			name:     "Disabled state returns false",
			state:    string(mcpv1alpha1.ServerStateDisabled),
			expected: false,
		},
		{
			name:     "unknown state returns false",
			state:    "Unknown",
			expected: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := config.MCPServer{
				Name:  "test",
				State: tc.state,
			}
			up := NewUpstreamMCP(&server)
			require.Equal(t, tc.expected, up.IsEnabled())
		})
	}
}
