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
	t.Run("multiple tools per server deduplicates to one metadata entry per server", func(t *testing.T) {
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
			{Name: "s1_a", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "s1_b", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "s1_c", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server1"}},
			{Name: "s2_a", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": "server2"}},
			{Name: "no_id", InputSchema: objectSchema, Meta: mcp.Meta{"other": "value"}},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListToolsResult{Tools: tools}
		got := broker.collectToolsCacheMetadata(result)

		assert.Len(t, got, 2, "should return one entry per server; tools without kuadrant/id ignored")
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
	t.Run("multiple prompts per server deduplicates to one metadata entry per server", func(t *testing.T) {
		servers := map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"server1": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server1", Prefix: "s1_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 500, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
			),
			"server2": createMockActiveMCPServer(t,
				config.MCPServer{Name: "server2", Prefix: "s2_"},
				upstream.CacheMetadata{},
				upstream.CacheMetadata{TTLMs: 1500, CacheScope: upstream.CacheScopePrivate},
			),
		}
		prompts := []*mcp.Prompt{
			{Name: "s1_a", Meta: map[string]any{"kuadrant/id": "server1"}},
			{Name: "s1_b", Meta: map[string]any{"kuadrant/id": "server1"}},
			{Name: "s2_a", Meta: map[string]any{"kuadrant/id": "server2"}},
			{Name: "no_id", Meta: map[string]any{"other": "value"}},
		}

		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = servers
		result := &mcp.ListPromptsResult{Prompts: prompts}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Len(t, got, 2, "should return one entry per server; prompts without kuadrant/id ignored")
	})

	t.Run("no prompts returns empty", func(t *testing.T) {
		broker := NewBroker(slog.Default()).(*mcpBrokerImpl)
		broker.mcpServers = map[config.UpstreamMCPID]upstream.ActiveMCPServer{}
		result := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
		got := broker.collectPromptsCacheMetadata(result)

		assert.Nil(t, got)
	})
}
