package broker

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func createMockActiveMCPServer(t *testing.T, cfg config.MCPServer, toolsCache, promptsCache upstream.CacheMetadata) upstream.ActiveMCPServer {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&cfg, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	assert.NoError(t, err)
	manager.SetCacheMetadataForTesting(toolsCache, promptsCache)
	return upstream.NewActiveForTesting(manager)
}

func TestCollectToolsCacheMetadata(t *testing.T) {
	t.Run("tools from two different upstreams", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1", Prefix: "s1_"},
				upstream.CacheMetadata{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
				upstream.CacheMetadata{},
			),
			"server2": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server2", Prefix: "s2_"},
				upstream.CacheMetadata{TTLMs: 2000, CacheScope: upstream.CacheScopePrivate},
				upstream.CacheMetadata{},
			),
		}
		tools := []*mcp.Tool{
			{Name: "tool1", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "tool2", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server2"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
			{TTLMs: 2000, CacheScope: upstream.CacheScopePrivate},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListToolsResult{Tools: tools}
		got := broker.collectToolsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		for i, want := range expected {
			assert.Equal(t, want.TTLMs, got[i].TTLMs)
			assert.Equal(t, want.CacheScope, got[i].CacheScope)
		}
	})

	t.Run("tools from same upstream deduplicates", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1"},
				upstream.CacheMetadata{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
				upstream.CacheMetadata{},
			),
		}
		tools := []*mcp.Tool{
			{Name: "tool1", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "tool2", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "tool3", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListToolsResult{Tools: tools}
		got := broker.collectToolsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		assert.Equal(t, expected[0].TTLMs, got[0].TTLMs)
		assert.Equal(t, expected[0].CacheScope, got[0].CacheScope)
	})

	t.Run("tool with no kuadrant/id skipped", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1"},
				upstream.CacheMetadata{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
				upstream.CacheMetadata{},
			),
		}
		tools := []*mcp.Tool{
			{Name: "tool1", InputSchema: objectSchema, Meta: mcp.Meta{"other": "value"}},
			{Name: "tool2", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListToolsResult{Tools: tools}
		got := broker.collectToolsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		assert.Equal(t, expected[0].TTLMs, got[0].TTLMs)
		assert.Equal(t, expected[0].CacheScope, got[0].CacheScope)
	})

	t.Run("no tools returns empty", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = map[config.UpstreamMCPID]upstream.ActiveMCPServer{}
		result := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
		got := broker.collectToolsCacheMetadata(result)

		assert.Nil(t, got)
	})
}

func TestCollectPromptsCacheMetadata(t *testing.T) {
	t.Run("prompts from two different upstreams", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1", Prefix: "s1_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 500, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
			),
			"server2": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server2", Prefix: "s2_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 1500, CacheScope: upstream.CacheScopePrivate, UserSpecificList: false},
			),
		}
		prompts := []*mcp.Prompt{
			{Name: "prompt1", Meta: map[string]any{"kuadrant/id": "server1"}},
			{Name: "prompt2", Meta: map[string]any{"kuadrant/id": "server2"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 500, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
			{TTLMs: 1500, CacheScope: upstream.CacheScopePrivate, UserSpecificList: false},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListPromptsResult{Prompts: prompts}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		for i, want := range expected {
			assert.Equal(t, want.TTLMs, got[i].TTLMs)
			assert.Equal(t, want.CacheScope, got[i].CacheScope)
			assert.Equal(t, want.UserSpecificList, got[i].UserSpecificList)
		}
	})

	t.Run("prompts from same upstream deduplicates", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 3000, CacheScope: upstream.CacheScopePublic},
			),
		}
		prompts := []*mcp.Prompt{
			{Name: "prompt1", Meta: map[string]any{"kuadrant/id": "server1"}},
			{Name: "prompt2", Meta: map[string]any{"kuadrant/id": "server1"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 3000, CacheScope: upstream.CacheScopePublic},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListPromptsResult{Prompts: prompts}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		assert.Equal(t, expected[0].TTLMs, got[0].TTLMs)
		assert.Equal(t, expected[0].CacheScope, got[0].CacheScope)
	})

	t.Run("prompt with no kuadrant/id skipped", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
			),
		}
		prompts := []*mcp.Prompt{
			{Name: "prompt1", Meta: map[string]any{"other": "value"}},
			{Name: "prompt2", Meta: map[string]any{"kuadrant/id": "server1"}},
		}
		expected := []upstream.CacheMetadata{
			{TTLMs: 1000, CacheScope: upstream.CacheScopePublic},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListPromptsResult{Prompts: prompts}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Equal(t, len(expected), len(got))
		assert.Equal(t, expected[0].TTLMs, got[0].TTLMs)
		assert.Equal(t, expected[0].CacheScope, got[0].CacheScope)
	})

	t.Run("no prompts returns empty", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = map[config.UpstreamMCPID]upstream.ActiveMCPServer{}
		result := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Nil(t, got)
	})
}
