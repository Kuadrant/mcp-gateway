package broker

import (
	"maps"
	"net/http"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// computeGatewaySupportedVersions returns the union of protocol versions
// supported by all registered upstream servers. Used to populate the
// server/discover response so clients negotiate a version the gateway
// can actually serve.
func (m *mcpBrokerImpl) computeGatewaySupportedVersions() []string {
	seen := make(map[string]struct{})
	m.serverVersions.Range(func(_, val any) bool {
		if versions, ok := val.([]string); ok {
			for _, v := range versions {
				seen[v] = struct{}{}
			}
		}
		return true
	})
	if len(seen) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(seen))
}

// rebuildProtocolCaches partitions the current gateway server tools and
// prompts into stateful (2025) and stateless (2026) sets based on each
// upstream server's supportedVersions. Broker meta-tools (those without
// kuadrant/id) are included only in the stateful set.
func (m *mcpBrokerImpl) rebuildProtocolCaches() {
	// partition tools
	allTools := m.gatewayServer.ListTools()
	var statefulT, statelessT []*mcp.Tool
	for _, gt := range allTools {
		tool := &gt.Tool

		if _, isBrokerTool := tool.Meta[brokerToolMetaKey]; isBrokerTool {
			statefulT = append(statefulT, tool)
			continue
		}

		serverIDVal, hasServerID := tool.Meta["kuadrant/id"]
		if !hasServerID {
			m.logger.Warn("tool missing kuadrant/id, excluded from protocol sets", "toolName", tool.Name)
			continue
		}

		serverIDStr, ok := serverIDVal.(string)
		if !ok {
			m.logger.Warn("tool has non-string kuadrant/id", "toolName", tool.Name, "id", serverIDVal)
			continue
		}
		serverID := config.UpstreamMCPID(serverIDStr)

		if m.ServerSupportsVersion(serverID, protocol.Version2025) {
			statefulT = append(statefulT, tool)
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			statelessT = append(statelessT, tool)
		}
	}
	m.statefulTools.Store(&statefulT)
	m.statelessTools.Store(&statelessT)

	// partition prompts
	allPrompts := m.gatewayServer.ListPrompts()
	var statefulP, statelessP []*mcp.Prompt
	for _, gp := range allPrompts {
		prompt := &gp.Prompt

		serverIDVal, hasServerID := prompt.Meta["kuadrant/id"]
		if !hasServerID {
			m.logger.Warn("prompt missing kuadrant/id, excluded from protocol sets", "promptName", prompt.Name)
			continue
		}

		serverIDStr, ok := serverIDVal.(string)
		if !ok {
			m.logger.Warn("prompt has non-string kuadrant/id", "promptName", prompt.Name, "id", serverIDVal)
			continue
		}
		serverID := config.UpstreamMCPID(serverIDStr)

		if m.ServerSupportsVersion(serverID, protocol.Version2025) {
			statefulP = append(statefulP, prompt)
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			statelessP = append(statelessP, prompt)
		}
	}
	m.statefulPrompts.Store(&statefulP)
	m.statelessPrompts.Store(&statelessP)

	m.logger.Debug("rebuilt protocol caches",
		"statefulTools", len(statefulT), "statelessTools", len(statelessT),
		"statefulPrompts", len(statefulP), "statelessPrompts", len(statelessP))
}

// promptsForProtocol returns the pre-cached prompt set for the client's protocol version.
// Returns a shallow copy to avoid mutation by downstream filters.
func (m *mcpBrokerImpl) promptsForProtocol(headers http.Header) []*mcp.Prompt {
	version := headers.Get(protocolVersionHeader)
	if version == protocol.Version2026 {
		if cached := m.statelessPrompts.Load(); cached != nil {
			prompts := make([]*mcp.Prompt, len(*cached))
			copy(prompts, *cached)
			return prompts
		}
	}

	if cached := m.statefulPrompts.Load(); cached != nil {
		prompts := make([]*mcp.Prompt, len(*cached))
		copy(prompts, *cached)
		return prompts
	}
	return nil
}

// toolsForProtocol returns the pre-cached tool set for the client's protocol version.
// Returns a shallow copy to avoid mutation by downstream filters.
func (m *mcpBrokerImpl) toolsForProtocol(headers http.Header) []*mcp.Tool {
	version := headers.Get(protocolVersionHeader)
	if version == protocol.Version2026 {
		if cached := m.statelessTools.Load(); cached != nil {
			tools := make([]*mcp.Tool, len(*cached))
			copy(tools, *cached)
			return tools
		}
	}

	// default to stateful for no header or any other version
	if cached := m.statefulTools.Load(); cached != nil {
		tools := make([]*mcp.Tool, len(*cached))
		copy(tools, *cached)
		return tools
	}
	return nil
}
