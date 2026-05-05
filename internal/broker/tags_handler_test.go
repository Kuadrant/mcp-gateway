package broker

import (
	"encoding/json"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestHandleListTags_empty(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	result, err := b.handleListTags()
	require.NoError(t, err)
	require.False(t, result.IsError)
	var tags []string
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &tags))
	require.Empty(t, tags)
}

func TestHandleListTags_deduplicates(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{mcp.NewTool("tool1")}, []string{"prod", "finance"})
	b.mcpServers["s2"] = createTestManagerWithTags(t, "s2", "s2_", []mcp.Tool{mcp.NewTool("tool2")}, []string{"prod", "hr"})

	result, err := b.handleListTags()
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tags []string
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &tags))
	require.ElementsMatch(t, []string{"finance", "hr", "prod"}, tags)
}

func TestHandleFilterToolsByTags_match(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{mcp.NewTool("tool1")}, []string{"prod", "finance"})
	b.mcpServers["s2"] = createTestManagerWithTags(t, "s2", "s2_", []mcp.Tool{mcp.NewTool("tool2")}, []string{"prod", "hr"})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"tags": []any{"prod", "finance"}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tools []mcp.Tool
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &tools))
	require.Len(t, tools, 1)
	require.Equal(t, "s1_tool1", tools[0].Name)
}

func TestHandleFilterToolsByTags_no_match(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	b.mcpServers["s1"] = createTestManagerWithTags(t, "s1", "s1_", []mcp.Tool{mcp.NewTool("tool1")}, []string{"dev"})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"tags": []any{"prod"}}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.False(t, result.IsError)

	var tools []mcp.Tool
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &tools))
	require.Empty(t, tools)
}

func TestHandleFilterToolsByTags_missing_param(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := b.handleFilterToolsByTags(req)
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestHasAllTags(t *testing.T) {
	require.True(t, hasAllTags([]string{"a", "b", "c"}, []string{"a", "b"}))
	require.True(t, hasAllTags([]string{"a"}, []string{}))
	require.False(t, hasAllTags([]string{"a", "b"}, []string{"a", "c"}))
	require.False(t, hasAllTags(nil, []string{"a"}))
}

func TestListTagsTool_registered(t *testing.T) {
	b := NewBroker(logger).(*mcpBrokerImpl)
	tools := b.listeningMCPServer.ListTools()
	_, hasListTags := tools["list_tags"]
	_, hasFilterTools := tools["filter_tools_by_tags"]
	require.True(t, hasListTags, "list_tags tool should be registered")
	require.True(t, hasFilterTools, "filter_tools_by_tags tool should be registered")
}

func createTestManagerWithTags(t *testing.T, serverName, toolPrefix string, tools []mcp.Tool, tags []string) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:       serverName,
		ToolPrefix: toolPrefix,
		URL:        "http://test.local/mcp",
		Tags:       tags,
	})
	manager := upstream.NewUpstreamMCPManager(mcpServer, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	manager.SetToolsForTesting(tools)
	return manager
}
