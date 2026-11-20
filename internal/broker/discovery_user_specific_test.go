package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// This upstream deliberately keeps its per-user catalog out of the manager's
// shared cache, as real userSpecificList servers do.
func newUserSpecificDiscoveryHarness(t *testing.T) *brokerHarness {
	t.Helper()
	backend := mcp.NewServer(&mcp.Implementation{Name: "per-user", Version: "1"}, nil)
	for _, name := range []string{"server_info", "only_a", "only_b"} {
		backend.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	backend.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err == nil && method == "tools/list" {
				auth := ""
				if extra := req.GetExtra(); extra != nil {
					auth = extra.Header.Get("Authorization")
				}
				result.(*mcp.ListToolsResult).Tools = slices.DeleteFunc(result.(*mcp.ListToolsResult).Tools, func(tool *mcp.Tool) bool {
					return tool.Name != "server_info" && !(tool.Name == "only_a" && auth == "Bearer user-a-token") && !(tool.Name == "only_b" && auth == "Bearer user-b-token")
				})
			}
			return result, err
		}
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, nil))
	t.Cleanup(ts.Close)
	h := newDiscoveryHarness(t)
	cfg := config.MCPServer{Name: "user-server", URL: ts.URL, Prefix: "uspec_", UserSpecificList: true, Category: []string{"User"}, Hint: "caller-specific tools"}
	h.b.mcpServers[cfg.ID()] = newMockActiveMCPServer(cfg)
	h.b.userSpecificServers = []userSpecificServer{toUserSpecificServer(cfg)}
	h.b.mcpServers["static"] = createTestManagerWithMeta(t, "static-server", "static_", []mcp.Tool{{Name: "sum"}}, []string{"General"}, "")
	populateTestVersions(h.b)
	t.Cleanup(h.b.drainUserSessionPool)
	return h
}

func discoveryUserRequest(t *testing.T, h *brokerHarness, sid, auth, method string, params any, extraHeaders ...map[string]string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": h.rpcID.Add(1), "method": method, "params": params})
	require.NoError(t, err)
	headers := map[string]string{"Authorization": auth}
	for _, extra := range extraHeaders {
		for key, value := range extra {
			headers[key] = value
		}
	}
	response := h.do(t, http.MethodPost, sid, string(body), headers)
	require.Equal(t, http.StatusOK, response.status, response.body)
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.body), &envelope), response.body)
	require.Empty(t, envelope.Error, response.body)
	return envelope.Result
}

func TestDiscoveryUserSpecificTools(t *testing.T) {
	h := newUserSpecificDiscoveryHarness(t)
	for _, caller := range []struct{ name, auth, allowed, denied string }{
		{"anonymous", "", "uspec_server_info", "uspec_only_a"},
		{"user A", "Bearer user-a-token", "uspec_only_a", "uspec_only_b"},
		{"user B", "Bearer user-b-token", "uspec_only_b", "uspec_only_a"},
	} {
		t.Run(caller.name, func(t *testing.T) {
			sid := h.initialize(t)
			// Selecting is the first tool call: no preceding list or discover can prime it.
			var selected mcp.CallToolResult
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, caller.auth, "tools/call", map[string]any{"name": "select_tools", "arguments": map[string]any{"tools": []string{caller.allowed}}}), &selected))
			require.False(t, selected.IsError)
			var listing mcp.ListToolsResult
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, caller.auth, "tools/list", map[string]any{}), &listing))
			var names []string
			for _, tool := range listing.Tools {
				names = append(names, tool.Name)
			}
			require.Contains(t, names, caller.allowed)
			require.NotContains(t, names, "static_sum")
			require.NotContains(t, names, caller.denied)

			var discovered mcp.CallToolResult
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, caller.auth, "tools/call", map[string]any{"name": "discover_tools", "arguments": map[string]any{"category": "user"}}), &discovered))
			require.False(t, discovered.IsError)
			var catalog discoverToolsResponse
			require.NoError(t, json.Unmarshal([]byte(discovered.Content[0].(*mcp.TextContent).Text), &catalog))
			require.Len(t, catalog.Servers, 1)
			require.Equal(t, "user-server", catalog.Servers[0].Name)
			require.Equal(t, "caller-specific tools", catalog.Servers[0].Hint)
			require.Contains(t, catalog.Servers[0].Tools, caller.allowed)
			require.Contains(t, catalog.Servers[0].Tools, "uspec_server_info")
			require.NotContains(t, catalog.Servers[0].Tools, caller.denied)

			// Selection checks the caller's full inventory, not the previous scope.
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, caller.auth, "tools/call", map[string]any{"name": "select_tools", "arguments": map[string]any{"tools": []string{"static_sum"}}}), &selected))
			require.False(t, selected.IsError)
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, caller.auth, "tools/call", map[string]any{"name": "select_tools", "arguments": map[string]any{"tools": []string{caller.denied}}}), &selected))
			require.True(t, selected.IsError)
			_, scope := h.b.scopeStore.getScope(sid)
			require.Contains(t, scope, "static_sum", "failed selection must preserve the existing scope")
		})
	}
}

func TestDiscoverUserSpecificToolsBeforeListing(t *testing.T) {
	h := newUserSpecificDiscoveryHarness(t)
	h.b.discovery.threshold = 1
	sid := h.initialize(t)
	var result mcp.CallToolResult
	require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, "", "tools/call", map[string]any{"name": "discover_tools", "arguments": map[string]any{}}), &result))
	var catalog discoverToolsResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &catalog))
	require.Len(t, catalog.Servers, 2, "discovery must bypass the listing threshold")
	var names []string
	for _, srv := range catalog.Servers {
		names = append(names, srv.Tools...)
	}
	require.Contains(t, names, "uspec_server_info")
	require.Contains(t, names, "static_sum")
	// Fetching must never publish caller-specific tools to the shared cache.
	for _, tool := range h.b.toolsForProtocol(false) {
		require.NotEqual(t, "uspec_server_info", tool.Name)
	}
	// Protocol-incompatible user-specific servers must remain excluded.
	for _, srv := range h.b.userSpecificServers {
		h.b.serverVersions.Store(srv.id, []string{protocol.Version2026})
	}
	require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, "", "tools/call", map[string]any{"name": "discover_tools", "arguments": map[string]any{}}), &result))
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &catalog))
	require.Len(t, catalog.Servers, 1)
	require.Equal(t, "static-server", catalog.Servers[0].Name)
}

func TestDiscoveryUserSpecificToolsRespectFilters(t *testing.T) {
	for _, filter := range []string{"authorization", "virtual server"} {
		t.Run(filter, func(t *testing.T) {
			h := newUserSpecificDiscoveryHarness(t)
			sid := h.initialize(t)
			headers := map[string]string{}
			if filter == "authorization" {
				h.b.trustedHeadersPublicKey = testPublicKey
				headers[authorizedCapabilitiesHeader] = createTestJWT(t, map[string][]string{"user-server": {"server_info"}})
			} else {
				h.b.virtualServers = map[string]*config.VirtualServer{"ns/vs": {Name: "ns/vs", Tools: []string{"uspec_server_info"}}}
				headers[virtualMCPHeader] = "ns/vs"
			}
			var discovered mcp.CallToolResult
			require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, "Bearer user-a-token", "tools/call", map[string]any{"name": "discover_tools", "arguments": map[string]any{}}, headers), &discovered))
			var catalog discoverToolsResponse
			require.NoError(t, json.Unmarshal([]byte(discovered.Content[0].(*mcp.TextContent).Text), &catalog))
			require.Len(t, catalog.Servers, 1)
			require.Equal(t, []string{"uspec_server_info"}, catalog.Servers[0].Tools)
			for _, tc := range []struct {
				name   string
				denied bool
			}{{"uspec_server_info", false}, {"uspec_only_a", true}} {
				var selected mcp.CallToolResult
				require.NoError(t, json.Unmarshal(discoveryUserRequest(t, h, sid, "Bearer user-a-token", "tools/call", map[string]any{"name": "select_tools", "arguments": map[string]any{"tools": []string{tc.name}}}, headers), &selected))
				require.Equal(t, tc.denied, selected.IsError)
			}
		})
	}
}
