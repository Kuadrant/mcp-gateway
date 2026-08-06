package broker

import (
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// collectToolsCacheMetadata extracts cache metadata from the upstreams that
// contributed tools to the given result. Each tool's kuadrant/id meta links
// it to an upstream; duplicates are deduplicated by server ID.
func (m *mcpBrokerImpl) collectToolsCacheMetadata(result *mcp.ListToolsResult) []upstream.CacheMetadata {
	metas := make([]mcp.Meta, len(result.Tools))
	for i, t := range result.Tools {
		metas[i] = t.Meta
	}
	return m.collectCacheMetadata(metas, func(mgr upstream.ActiveMCPServer) upstream.CacheMetadata {
		return mgr.ToolsCacheMetadata()
	})
}

// collectPromptsCacheMetadata extracts cache metadata from the upstreams that
// contributed prompts to the given result.
func (m *mcpBrokerImpl) collectPromptsCacheMetadata(result *mcp.ListPromptsResult) []upstream.CacheMetadata {
	metas := make([]mcp.Meta, len(result.Prompts))
	for i, p := range result.Prompts {
		metas[i] = p.Meta
	}
	return m.collectCacheMetadata(metas, func(mgr upstream.ActiveMCPServer) upstream.CacheMetadata {
		return mgr.PromptsCacheMetadata()
	})
}

// collectCacheMetadata deduplicates server IDs from the given meta slices
// and returns one CacheMetadata per distinct contributing upstream.
func (m *mcpBrokerImpl) collectCacheMetadata(metas []mcp.Meta, metaFn func(upstream.ActiveMCPServer) upstream.CacheMetadata) []upstream.CacheMetadata {
	seen := make(map[config.UpstreamMCPID]bool)
	var contributing []upstream.CacheMetadata

	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, meta := range metas {
		id := extractServerID(meta)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		mgr, ok := m.mcpServers[id]
		if !ok {
			continue
		}
		contributing = append(contributing, metaFn(mgr))
	}
	return contributing
}

func extractServerID(meta mcp.Meta) config.UpstreamMCPID {
	val, ok := meta[upstream.GatewayServerID]
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return config.UpstreamMCPID(s)
}
