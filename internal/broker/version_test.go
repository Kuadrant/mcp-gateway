package broker

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBroker_ServerSupportsVersion(t *testing.T) {
	broker := NewBroker(slog.Default()).(*mcpBrokerImpl)

	// create a mock manager with protocol version set
	mgr := &mockActiveServer{
		supportedVersions: []string{"2025-11-25", "2026-07-28"},
	}

	serverID := config.UpstreamMCPID("test-server")
	broker.mcpServers[serverID] = mgr

	// test that the broker can query versions
	if !broker.ServerSupportsVersion(serverID, "2025-11-25") {
		t.Error("ServerSupportsVersion(2025-11-25) = false, want true")
	}

	if !broker.ServerSupportsVersion(serverID, "2026-07-28") {
		t.Error("ServerSupportsVersion(2026-07-28) = false, want true")
	}

	if broker.ServerSupportsVersion(serverID, "9999-99-99") {
		t.Error("ServerSupportsVersion(9999-99-99) = true, want false")
	}

	// test unknown server
	if broker.ServerSupportsVersion("unknown", "2025-11-25") {
		t.Error("ServerSupportsVersion for unknown server = true, want false")
	}

	// test caching: second call should use cached value
	if !broker.ServerSupportsVersion(serverID, "2025-11-25") {
		t.Error("ServerSupportsVersion(2025-11-25) on second call = false, want true")
	}
}

// mockActiveServer implements upstream.ActiveMCPServer for testing
type mockActiveServer struct {
	supportedVersions []string
}

func (m *mockActiveServer) Stop()           {}
func (m *mockActiveServer) MCPName() string { return "mock" }
func (m *mockActiveServer) GetStatus() upstream.ServerValidationStatus {
	return upstream.ServerValidationStatus{}
}
func (m *mockActiveServer) GetManagedTools() []mcp.Tool           { return nil }
func (m *mockActiveServer) GetServedManagedTool(string) *mcp.Tool { return nil }
func (m *mockActiveServer) GetToolHints(string) (upstream.ToolHints, bool) {
	return upstream.ToolHints{}, false
}
func (m *mockActiveServer) GetManagedPrompts() []mcp.Prompt           { return nil }
func (m *mockActiveServer) GetServedManagedPrompt(string) *mcp.Prompt { return nil }
func (m *mockActiveServer) Config() config.MCPServer                  { return config.MCPServer{} }
func (m *mockActiveServer) SupportedVersions() []string               { return m.supportedVersions }
func (m *mockActiveServer) SupportsVersion(v string) bool {
	for _, ver := range m.supportedVersions {
		if ver == v {
			return true
		}
	}
	return false
}
