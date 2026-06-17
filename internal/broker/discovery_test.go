package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func createTestManagerWithMeta(t *testing.T, serverName, prefix string, tools []mcp.Tool, category []string, hint string) upstream.ActiveMCPServer {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:     serverName,
		Prefix:   prefix,
		URL:      "http://test.local/mcp",
		Category: category,
		Hint:     hint,
	})
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	manager.SetToolsForTesting(tools)
	return upstream.NewActiveForTesting(manager)
}

func TestDiscoverTools_BasicResponse(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"weather-service", "weather_",
		[]mcp.Tool{{Name: "forecast"}, {Name: "current"}},
		[]string{"Weather", "External"}, "weather data from OpenWeather",
	)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}
	req.Header = http.Header{}

	result, err := b.handleDiscoverTools(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var resp discoverToolsResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &resp))
	require.Len(t, resp.Servers, 1)
	require.Equal(t, "weather-service", resp.Servers[0].Name)
	require.Equal(t, []string{"Weather", "External"}, resp.Servers[0].Categories)
	require.Equal(t, "weather data from OpenWeather", resp.Servers[0].Hint)
	require.ElementsMatch(t, []string{"weather_forecast", "weather_current"}, resp.Servers[0].Tools)
}

func TestDiscoverTools_CategoryFilter(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "tool1"}},
		[]string{"Dining", "Reservations"}, "",
	)
	b.mcpServers["s2"] = createTestManagerWithMeta(t,
		"svc2", "s2_",
		[]mcp.Tool{{Name: "tool2"}},
		[]string{"Weather"}, "",
	)

	tests := []struct {
		name     string
		filter   string
		expected int
	}{
		{"case-insensitive match", "dining", 1},
		{"exact case match", "Dining", 1},
		{"no match", "finance", 0},
		{"no filter returns all", "", 2},
		{"matches second element", "reservations", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.filter != "" {
				args["category"] = tc.filter
			}
			req := mcp.CallToolRequest{}
			req.Params.Arguments = args
			req.Header = http.Header{}

			result, err := b.handleDiscoverTools(context.Background(), req)
			require.NoError(t, err)

			var resp discoverToolsResponse
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &resp))
			require.Len(t, resp.Servers, tc.expected)
		})
	}
}

func TestDiscoverTools_EmptyCategoryMatchReturnsEmptyServers(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "tool1"}},
		[]string{"Weather"}, "",
	)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"category": "nonexistent"}
	req.Header = http.Header{}

	result, err := b.handleDiscoverTools(context.Background(), req)
	require.NoError(t, err)

	var resp discoverToolsResponse
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &resp))
	require.Len(t, resp.Servers, 0)
}

func TestMatchesCategory(t *testing.T) {
	require.True(t, matchesCategory([]string{"Dining", "Reservations"}, "dining"))
	require.True(t, matchesCategory([]string{"Dining", "Reservations"}, "RESERVATIONS"))
	require.False(t, matchesCategory([]string{"Dining"}, "weather"))
	require.False(t, matchesCategory(nil, "anything"))
	require.False(t, matchesCategory([]string{}, "anything"))
}

func TestIsBrokerTool(t *testing.T) {
	require.False(t, IsBrokerTool(mcp.Tool{Name: "user_tool"}))
	require.True(t, IsBrokerTool(mcp.Tool{Name: "discover_tools"}))
	require.True(t, IsBrokerTool(mcp.Tool{Name: "select_tools"}))

	// tags tools are detected via meta annotation, not name
	require.False(t, IsBrokerTool(mcp.Tool{Name: "list_tags"}))
	tagsTool := mcp.Tool{Name: "list_tags"}
	tagsTool.Meta = mcp.NewMetaFromMap(map[string]any{brokerToolMetaKey: true})
	require.True(t, IsBrokerTool(tagsTool))
}

func TestIsBrokerToolName(t *testing.T) {
	b := &mcpBrokerImpl{discovery: discoveryConfig{enabled: true}}
	require.True(t, b.IsBrokerToolName("discover_tools"))
	require.True(t, b.IsBrokerToolName("select_tools"))
	require.False(t, b.IsBrokerToolName("user_tool"))
	require.False(t, b.IsBrokerToolName(""))

	disabled := &mcpBrokerImpl{discovery: discoveryConfig{enabled: false}}
	require.False(t, disabled.IsBrokerToolName("discover_tools"))

	// tags tools are gated on tagsToolsRegistered
	require.False(t, b.IsBrokerToolName("list_tags"))
	require.False(t, b.IsBrokerToolName("filter_tools_by_tags"))
	b.tagsToolsRegistered.Store(true)
	require.True(t, b.IsBrokerToolName("list_tags"))
	require.True(t, b.IsBrokerToolName("filter_tools_by_tags"))
}

func TestParseToolNames(t *testing.T) {
	names, err := parseToolNames([]any{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, names)

	_, err = parseToolNames(nil)
	require.Error(t, err)

	_, err = parseToolNames("not-an-array")
	require.Error(t, err)

	_, err = parseToolNames([]any{"a", 123})
	require.Error(t, err)
}

func TestSelectTools_AllOrNothing(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "real_tool"}},
		[]string{"Test"}, "",
	)

	// mock session
	session := &mockSession{id: "test-session-1"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{"s1_real_tool", "s1_nonexistent"},
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.True(t, result.IsError, "should fail because s1_nonexistent doesn't exist")

	// scope must remain unset after a failed selection
	state, _ := b.scopeStore.getScope("test-session-1")
	require.Equal(t, scopeUnset, state)
}

func TestSelectTools_Success(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "tool_a"}, {Name: "tool_b"}},
		[]string{"Test"}, "",
	)

	session := &mockSession{id: "test-session-2"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{"s1_tool_a"},
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	// verify scope was set
	state, tools := b.scopeStore.getScope("test-session-2")
	require.Equal(t, scopeFiltered, state)
	_, ok := tools["s1_tool_a"]
	require.True(t, ok)
}

func TestSelectTools_EmptyResetsScope(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.scopeStore.setScope("s1", []string{"tool_a"})

	session := &mockSession{id: "s1"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{},
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	state, _ := b.scopeStore.getScope("s1")
	require.Equal(t, scopeAll, state)
}

func TestSelectTools_Rescope(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "tool_a"}, {Name: "tool_b"}, {Name: "tool_c"}},
		[]string{"Test"}, "",
	)

	session := &mockSession{id: "rescope-session"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	// first select_tools: scope to tool_a
	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{"s1_tool_a"},
	}
	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	state, tools := b.scopeStore.getScope("rescope-session")
	require.Equal(t, scopeFiltered, state)
	require.Len(t, tools, 1)
	_, ok := tools["s1_tool_a"]
	require.True(t, ok)

	// second select_tools: re-scope to tool_b and tool_c
	req.Params.Arguments = map[string]any{
		"tools": []any{"s1_tool_b", "s1_tool_c"},
	}
	result, err = b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	state, tools = b.scopeStore.getScope("rescope-session")
	require.Equal(t, scopeFiltered, state)
	require.Len(t, tools, 2)
	_, ok = tools["s1_tool_b"]
	require.True(t, ok)
	_, ok = tools["s1_tool_c"]
	require.True(t, ok)
	// tool_a should no longer be in scope
	_, ok = tools["s1_tool_a"]
	require.False(t, ok)
}

func TestSelectTools_BrokerToolsNotSelectable(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	session := &mockSession{id: "s1"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{"discover_tools"},
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestSelectTools_MaxToolsExceeded(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	session := &mockSession{id: "max-tools-session"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	// build a tools list exceeding the 1000 limit
	tools := make([]any, 1001)
	for i := range tools {
		tools[i] = fmt.Sprintf("tool_%d", i)
	}

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": tools,
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.True(t, result.IsError, "should reject when tool count exceeds max")
}

func TestThresholdFilter(t *testing.T) {
	b := NewBroker(logger,
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(2),
	).(*mcpBrokerImpl)

	brokerTool := mcp.Tool{Name: "discover_tools"}
	brokerTool.Meta = mcp.NewMetaFromMap(map[string]any{brokerToolMetaKey: true})

	tools := []mcp.Tool{
		{Name: "tool1"},
		{Name: "tool2"},
		{Name: "tool3"},
		brokerTool,
	}

	// 3 real tools > threshold 2, should hide real tools
	result := b.applyThresholdFilter(tools)
	require.Len(t, result, 1)
	require.Equal(t, "discover_tools", result[0].Name)
}

func TestThresholdFilter_ZeroMeansNeverHide(t *testing.T) {
	b := NewBroker(logger,
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(0),
	).(*mcpBrokerImpl)

	tools := []mcp.Tool{
		{Name: "tool1"},
		{Name: "tool2"},
	}

	result := b.applyThresholdFilter(tools)
	require.Len(t, result, 2)
}

func TestThresholdFilter_UnderThreshold(t *testing.T) {
	b := NewBroker(logger,
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(5),
	).(*mcpBrokerImpl)

	tools := []mcp.Tool{
		{Name: "tool1"},
		{Name: "tool2"},
	}

	result := b.applyThresholdFilter(tools)
	require.Len(t, result, 2)
}

func TestScopeFilterRevalidatesAuth(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	// scope has tool_a, but tool_a is not in the current tool list (e.g. removed upstream)
	b.scopeStore.setScope("s1", []string{"tool_a"})

	session := &mockSession{id: "s1"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	currentTools := []mcp.Tool{
		{Name: "tool_b"},
	}

	result := b.applyScopeFilter(ctx, currentTools)
	// tool_a is in scope but not in current tools, so should be empty (no broker tools in this list)
	require.Len(t, result, 0)
}

func TestConfigChanged_CategoryAndHint(t *testing.T) {
	s1 := config.MCPServer{Name: "a", Category: []string{"X"}, Hint: "old"}
	s2 := config.MCPServer{Name: "a", Category: []string{"X"}, Hint: "new"}
	require.True(t, s2.ConfigChanged(s1), "hint change should be detected")

	s3 := config.MCPServer{Name: "a", Category: []string{"Y"}, Hint: "old"}
	require.True(t, s3.ConfigChanged(s1), "category change should be detected")

	s4 := config.MCPServer{Name: "a", Category: []string{"X"}, Hint: "old"}
	require.False(t, s4.ConfigChanged(s1), "no change")

	s5 := config.MCPServer{Name: "a", Category: []string{"X", "Y"}, Hint: "old"}
	require.True(t, s5.ConfigChanged(s1), "category length change should be detected")
}

func TestDiscoveryToolsRegistration(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true))
	tools := b.MCPServer().ListTools()

	_, hasDiscover := tools[discoverToolsName]
	require.True(t, hasDiscover, "discover_tools should be registered")

	_, hasSelect := tools[selectToolsName]
	require.True(t, hasSelect, "select_tools should be registered")
}

func TestDiscoveryToolsNotRegisteredWhenDisabled(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(false))
	tools := b.MCPServer().ListTools()

	_, hasDiscover := tools[discoverToolsName]
	require.False(t, hasDiscover, "discover_tools should not be registered when disabled")
}

func TestSelectTools_NotificationFailureWarning(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)

	b.mcpServers["s1"] = createTestManagerWithMeta(t,
		"svc1", "s1_",
		[]mcp.Tool{{Name: "tool_a"}},
		[]string{"Test"}, "",
	)

	// use a session that is NOT registered with the MCP server,
	// so SendNotificationToSpecificClient will fail
	session := &mockSession{id: "unregistered-session"}
	ctx := b.listeningMCPServer.WithContext(context.Background(), session)

	req := mcp.CallToolRequest{}
	req.Header = http.Header{}
	req.Params.Arguments = map[string]any{
		"tools": []any{"s1_tool_a"},
	}

	result, err := b.handleSelectTools(ctx, req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &resp))
	_, hasWarning := resp["warning"]
	require.True(t, hasWarning, "should include warning when notification fails")
}

func TestScopeStore_CleanupOnDisconnect(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)
	b.scopeStore.setScope("session-1", []string{"tool_a"})
	require.Equal(t, 1, b.scopeStore.size())

	// simulate the hook that fires on unregister
	b.scopeStore.deleteScope("session-1")
	require.Equal(t, 0, b.scopeStore.size())
}

func TestScopeStore_SizeOnStatus(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)
	b.scopeStore.setScope("s1", []string{"a"})
	b.scopeStore.setScope("s2", []string{"b"})

	status := b.ValidateAllServers()
	require.Equal(t, 2, status.ScopedSessions)
}

func TestDiscoveryEnabled_ScopeStoreAllocated(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(true)).(*mcpBrokerImpl)
	require.NotNil(t, b.scopeStore, "scope store should be allocated when discovery enabled")
	require.True(t, b.discovery.enabled)

	tools := b.MCPServer().ListTools()
	_, hasDiscover := tools[discoverToolsName]
	require.True(t, hasDiscover, "discover_tools should be registered when enabled")
}

func TestDiscoveryDisabled_NoScopeStore(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	require.Nil(t, b.scopeStore, "scope store should be nil when discovery disabled")
	require.False(t, b.discovery.enabled)

	tools := b.MCPServer().ListTools()
	_, hasDiscover := tools[discoverToolsName]
	require.False(t, hasDiscover, "discover_tools should not be registered when disabled")
}

// mockSession implements server.ClientSession for testing
type mockSession struct {
	id   string
	ch   chan mcp.JSONRPCNotification
	init bool
}

func (m *mockSession) SessionID() string { return m.id }
func (m *mockSession) Initialize()       { m.init = true }
func (m *mockSession) Initialized() bool { return m.init }
func (m *mockSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if m.ch == nil {
		m.ch = make(chan mcp.JSONRPCNotification, 10)
	}
	return m.ch
}

var _ server.ClientSession = &mockSession{}
