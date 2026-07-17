package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestHandleListTags_empty(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	result, err := b.handleListTags(&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	var tags []string
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &tags))
	require.Empty(t, tags)
}

func TestHandleListTags_deduplicates(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{{Name: "tool1"}}, []string{"prod", "finance"})
	b.mcpServers["s2"] = createTestManagerWithTags(t, "s2", "s2_", []mcp.Tool{{Name: "tool2"}}, []string{"prod", "hr"})

	result, err := b.handleListTags(&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tags []string
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &tags))
	require.Equal(t, []string{"finance", "hr", "prod"}, tags)
}

func TestHandleFilterToolsByTags_match(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{{Name: "tool1"}}, []string{"prod", "finance"})
	b.mcpServers["s2"] = createTestManagerWithTags(t, "s2", "s2_", []mcp.Tool{{Name: "tool2"}}, []string{"prod", "hr"})

	args, _ := json.Marshal(map[string]any{"tags": []any{"prod", "finance"}})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tools []mcp.Tool
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &tools))
	require.Len(t, tools, 1)
	require.Equal(t, "s1_tool1", tools[0].Name)
}

func TestHandleFilterToolsByTags_no_match(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{{Name: "tool1"}}, []string{"dev"})

	args, _ := json.Marshal(map[string]any{"tags": []any{"prod"}})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tools []mcp.Tool
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &tools))
	require.Empty(t, tools)
}

func TestHandleFilterToolsByTags_missing_param(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	args, _ := json.Marshal(map[string]any{})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestHandleFilterToolsByTags_empty_tags(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{{Name: "tool1"}}, []string{"prod"})

	args, _ := json.Marshal(map[string]any{"tags": []any{}})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestHandleFilterToolsByTags_empty_string_tag(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	args, _ := json.Marshal(map[string]any{"tags": []any{"prod", ""}})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: args}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestHasAllTags(t *testing.T) {
	require.True(t, hasAllTags([]string{"a", "b", "c"}, []string{"a", "b"}))
	require.False(t, hasAllTags([]string{"a", "b"}, []string{"a", "c"}))
	require.False(t, hasAllTags(nil, []string{"a"}))
}

func TestTagsTools_not_registered_at_startup(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	tools := b.gatewayServer.ListTools()
	_, hasListTags := tools[listTagsName]
	_, hasFilterTools := tools[filterToolsByTagsName]
	require.False(t, hasListTags, "list_tags should not be registered without tags")
	require.False(t, hasFilterTools, "filter_tools_by_tags should not be registered without tags")
}

func TestTagsTools_registered_when_tags_exist(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	servers := []*config.MCPServer{{Name: "s1", Tags: []string{"prod"}}}
	b.syncTagsTools(context.Background(), servers)
	require.True(t, b.tagsToolsRegistered.Load())
	tools := b.gatewayServer.ListTools()
	_, hasListTags := tools[listTagsName]
	_, hasFilterTools := tools[filterToolsByTagsName]
	require.True(t, hasListTags, "list_tags should be registered when tags exist")
	require.True(t, hasFilterTools, "filter_tools_by_tags should be registered when tags exist")
}

func TestTagsTools_deregistered_when_tags_removed(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	servers := []*config.MCPServer{{Name: "s1", Tags: []string{"prod"}}}
	b.syncTagsTools(context.Background(), servers)
	require.True(t, b.tagsToolsRegistered.Load())

	servers = []*config.MCPServer{{Name: "s1"}}
	b.syncTagsTools(context.Background(), servers)
	require.False(t, b.tagsToolsRegistered.Load())
	tools := b.gatewayServer.ListTools()
	_, hasListTags := tools[listTagsName]
	_, hasFilterTools := tools[filterToolsByTagsName]
	require.False(t, hasListTags, "list_tags should be deregistered when no tags")
	require.False(t, hasFilterTools, "filter_tools_by_tags should be deregistered when no tags")
}

func createTestManagerWithTags(t *testing.T, serverName, prefix string, tools []mcp.Tool, tags []string) upstream.ActiveMCPServer {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:   serverName,
		Prefix: prefix,
		URL:    "http://test.local/mcp",
		Tags:   tags,
	}, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	manager.SetToolsForTesting(tools)
	return upstream.NewActiveForTesting(manager)
}
