package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// fakeUpstream returns a minimal MCP HTTP server exposing a single tool.
// deleteDelay throttles session termination (DELETE), simulating a slow
// upstream teardown during manager Stop.
func fakeUpstream(t *testing.T, toolName, sessionID string, deleteDelay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodDelete:
			time.Sleep(deleteDelay)
			w.WriteHeader(http.StatusOK)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		id := req["id"]
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", sessionID)
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"serverInfo":      map[string]any{"name": "fake-upstream", "version": "1.0"},
					"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": toolName, "description": "d", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		}
	}))
}

// regression: on a config change the replaced manager's deferred Stop ran
// after the replacement started; its removeAllTools deleted the same-named
// tools the new manager had just registered, leaving them missing until the
// next upstream change. old managers must be fully stopped before
// replacements start.
func TestOnConfigChange_ReplacedManagerDoesNotDeleteReplacementTools(t *testing.T) {
	// old upstream is slow to terminate, so with the buggy ordering its
	// removeAllTools lands well after the replacement registered its tools
	oldUpstream := fakeUpstream(t, "shared_tool", "old-sess", 400*time.Millisecond)
	defer oldUpstream.Close()
	newUpstream := fakeUpstream(t, "shared_tool", "new-sess", 0)
	defer newUpstream.Close()

	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)

	toolRegistered := func() bool {
		_, ok := b.gatewayServer.ListTools()["s1_shared_tool"]
		return ok
	}

	conf := &config.MCPServersConfig{Servers: []*config.MCPServer{
		{Name: "server-one", URL: oldUpstream.URL, Prefix: "s1_"},
	}}
	b.OnConfigChange(context.Background(), conf)
	require.Eventually(t, toolRegistered, 5*time.Second, 20*time.Millisecond, "initial tool registration")

	// same server, changed URL: manager is replaced
	conf = &config.MCPServersConfig{Servers: []*config.MCPServer{
		{Name: "server-one", URL: newUpstream.URL, Prefix: "s1_"},
	}}
	b.OnConfigChange(context.Background(), conf)

	require.Eventually(t, toolRegistered, 5*time.Second, 20*time.Millisecond, "replacement tool registration")
	require.Never(t, func() bool { return !toolRegistered() }, 1*time.Second, 20*time.Millisecond,
		"replacement's tools must not be deleted by the old manager's teardown")

	require.NoError(t, b.Shutdown(context.Background()))
}

// the scope-change sentinel must never surface in tools/list responses.
func TestFilterTools_DropsScopeChangeSentinel(t *testing.T) {
	b := &mcpBrokerImpl{logger: slog.Default()}
	res := &mcp.ListToolsResult{Tools: []*mcp.Tool{
		{Name: "real_tool", Meta: mcp.Meta{"kuadrant/id": "ns/s"}},
		{Name: scopeChangeSentinelName},
	}}

	b.FilterTools(context.Background(), http.Header{}, "", res)

	require.Len(t, res.Tools, 1)
	require.Equal(t, "real_tool", res.Tools[0].Name)
}
