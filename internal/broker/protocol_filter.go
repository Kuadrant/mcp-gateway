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

// rebuildProtocolToolCache partitions the current gateway server tool list
// into stateful (2025) and stateless (2026) sets based on each upstream
// server's supportedVersions. Broker meta-tools (those without kuadrant/id)
// are included only in the stateful set.
func (m *mcpBrokerImpl) rebuildProtocolToolCache() {
	allTools := m.gatewayServer.ListTools()

	var stateful, stateless []*mcp.Tool
	for _, gt := range allTools {
		tool := &gt.Tool

		// broker meta-tools (discover_tools, select_tools, etc) are
		// session-scoped and only usable by stateful clients
		if _, isBrokerTool := tool.Meta[brokerToolMetaKey]; isBrokerTool {
			stateful = append(stateful, tool)
			continue
		}

		serverIDVal, hasServerID := tool.Meta["kuadrant/id"]
		if !hasServerID {
			stateful = append(stateful, tool)
			continue
		}

		serverIDStr, ok := serverIDVal.(string)
		if !ok {
			m.logger.Warn("tool has non-string kuadrant/id", "toolName", tool.Name, "id", serverIDVal)
			continue
		}
		serverID := config.UpstreamMCPID(serverIDStr)

		if m.ServerSupportsVersion(serverID, protocol.Version2025) {
			stateful = append(stateful, tool)
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			stateless = append(stateless, tool)
		}
	}

	m.statefulTools.Store(&stateful)
	m.statelessTools.Store(&stateless)
	m.logger.Debug("rebuilt protocol tool cache",
		"statefulCount", len(stateful),
		"statelessCount", len(stateless))
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
