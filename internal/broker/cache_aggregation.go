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
	seen := make(map[config.UpstreamMCPID]bool)
	var contributing []upstream.CacheMetadata

	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, tool := range result.Tools {
		id := extractServerID(tool.Meta)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		mgr, ok := m.mcpServers[id]
		if !ok {
			continue
		}
		contributing = append(contributing, mgr.ToolsCacheMetadata())
	}
	return contributing
}

// collectPromptsCacheMetadata extracts cache metadata from the upstreams that
// contributed prompts to the given result.
func (m *mcpBrokerImpl) collectPromptsCacheMetadata(result *mcp.ListPromptsResult) []upstream.CacheMetadata {
	seen := make(map[config.UpstreamMCPID]bool)
	var contributing []upstream.CacheMetadata

	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, prompt := range result.Prompts {
		id := extractServerID(prompt.Meta)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		mgr, ok := m.mcpServers[id]
		if !ok {
			continue
		}
		contributing = append(contributing, mgr.PromptsCacheMetadata())
	}
	return contributing
}

func extractServerID(meta mcp.Meta) config.UpstreamMCPID {
	val, ok := meta["kuadrant/id"]
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return ""
	}
	return config.UpstreamMCPID(s)
}
