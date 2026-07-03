package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// pinnedVersionServer returns an httptest server that speaks just enough raw
// JSON-RPC to complete an MCP initialize handshake, always replying with the
// given protocol version regardless of what the client proposed.
func pinnedVersionServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("mcp-session-id", "test-session")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":%q,"capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"pinned","version":"1.0.0"}}}`, req.ID, version)
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
		}
	}))
}

func connectTo(t *testing.T, url string) (*MCPServer, error) {
	t.Helper()
	up := NewUpstreamMCP(&config.MCPServer{
		Name:     "pinned",
		URL:      url,
		State:    string(mcpv1alpha1.ServerStateEnabled),
		Hostname: "pinned",
	}, "")
	return up, up.Connect(t.Context(), func() {})
}

// TestConnect_NegotiatesValidProtocolVersions asserts the broker accepts an
// upstream pinned to any version in mcp.ValidProtocolVersions and records the
// negotiated version. The matrix is driven by the library's own list, so it
// stays current as new MCP versions are added upstream.
func TestConnect_NegotiatesValidProtocolVersions(t *testing.T) {
	for _, version := range mcp.ValidProtocolVersions {
		t.Run(version, func(t *testing.T) {
			srv := pinnedVersionServer(t, version)
			t.Cleanup(srv.Close)

			up, err := connectTo(t, srv.URL+"/mcp")
			require.NoError(t, err, "broker should accept valid protocol version %s", version)
			// disconnect before the server closes (cleanups run LIFO) to avoid noisy logs
			t.Cleanup(func() { _ = up.Disconnect() })

			info := up.ProtocolInfo()
			require.NotNil(t, info)
			require.Equal(t, version, info.ProtocolVersion)
		})
	}
}

// TestConnect_RejectsUnsupportedProtocolVersion asserts an upstream replying
// with a version outside mcp.ValidProtocolVersions is rejected at connect time,
// mirroring the broken-server e2e negative case at the unit level.
func TestConnect_RejectsUnsupportedProtocolVersion(t *testing.T) {
	srv := pinnedVersionServer(t, "2021-11-05")
	t.Cleanup(srv.Close)

	up, err := connectTo(t, srv.URL+"/mcp")
	if up != nil {
		t.Cleanup(func() { _ = up.Disconnect() })
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported protocol version")
}
