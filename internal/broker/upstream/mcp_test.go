package upstream

import (
	"testing"

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
