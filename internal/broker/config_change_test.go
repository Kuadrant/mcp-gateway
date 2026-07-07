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
	return fakeUpstreamWithStopHook(t, toolName, sessionID, func() { time.Sleep(deleteDelay) })
}

// fakeUpstreamWithStopHook is fakeUpstream with an arbitrary hook run on
// session termination (DELETE), so tests can gate manager Stop on channels.
func fakeUpstreamWithStopHook(t *testing.T, toolName, sessionID string, onStop func()) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodDelete:
			onStop()
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

// regression: config.Notify runs each observer in its own goroutine and
// secret-mount updates emit several fsnotify events back to back, so
// OnConfigChange can be invoked concurrently. with Stop outside mcpLock,
// unserialised reloads let a stale reload's Stop delete tools a newer
// reload's replacement manager had registered. reloads must serialise
// end to end so the final state always matches the last-applied config.
func TestOnConfigChange_ConcurrentReloadsSerialise(t *testing.T) {
	stopEntered := make(chan struct{}, 1)
	stopGate := make(chan struct{})
	upstreamA := fakeUpstreamWithStopHook(t, "reload_tool", "sess-a", func() {
		select {
		case stopEntered <- struct{}{}:
		default:
		}
		<-stopGate
	})
	defer upstreamA.Close()
	upstreamB := fakeUpstream(t, "reload_tool", "sess-b", 0)
	defer upstreamB.Close()
	upstreamC := fakeUpstream(t, "reload_tool", "sess-c", 0)
	defer upstreamC.Close()

	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)

	toolRegistered := func() bool {
		_, ok := b.gatewayServer.ListTools()["s1_reload_tool"]
		return ok
	}
	serverConf := func(url string) *config.MCPServersConfig {
		return &config.MCPServersConfig{Servers: []*config.MCPServer{
			{Name: "server-one", URL: url, Prefix: "s1_"},
		}}
	}
	serverID := (&config.MCPServer{Name: "server-one", Prefix: "s1_"}).ID()

	b.OnConfigChange(context.Background(), serverConf(upstreamA.URL))
	require.Eventually(t, toolRegistered, 5*time.Second, 20*time.Millisecond, "initial tool registration")

	// reload 1 replaces A with B; A's manager Stop blocks on the gate
	r1done := make(chan struct{})
	go func() {
		b.OnConfigChange(context.Background(), serverConf(upstreamB.URL))
		close(r1done)
	}()
	<-stopEntered

	// reload 2 arrives while reload 1 is still stopping the old manager
	r2done := make(chan struct{})
	go func() {
		b.OnConfigChange(context.Background(), serverConf(upstreamC.URL))
		close(r2done)
	}()

	// serialised reloads cannot complete reload 2 while reload 1 holds the
	// gate. if it completes anyway (the regression), wait for its manager to
	// register its tools so the stale Stop's removeAllTools provably lands
	// after them. the old manager's same-named tool is still registered at
	// gateway level, so observe the replacement manager's own served tools.
	managerServesTool := func() bool {
		man, ok := b.RegisteredMCPServers()[serverID]
		return ok && man.Config().URL == upstreamC.URL && man.GetServedManagedTool("s1_reload_tool") != nil
	}
	select {
	case <-r2done:
		require.Eventually(t, managerServesTool, 5*time.Second, 20*time.Millisecond, "overlapping reload tool registration")
	case <-time.After(500 * time.Millisecond):
	}
	close(stopGate)

	for name, done := range map[string]chan struct{}{"reload 1": r1done, "reload 2": r2done} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not complete", name)
		}
	}

	lastApplied := func() bool {
		man, ok := b.RegisteredMCPServers()[serverID]
		return ok && man.Config().URL == upstreamC.URL && toolRegistered()
	}
	require.Eventually(t, lastApplied, 5*time.Second, 20*time.Millisecond,
		"registered state must match the last-applied config")
	require.Never(t, func() bool { return !lastApplied() }, 1*time.Second, 20*time.Millisecond,
		"a stale reload must not clobber the newer one")

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
