package broker

import (
	"maps"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
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

// protocolCacheEntry holds a pre-filtered item set and the unique upstream
// server IDs that contributed to it. serverIDs is computed once during
// rebuildProtocolCaches so that cache aggregation can look up metadata by
// ID directly, without re-extracting kuadrant/id from every item.
// freshFetchServers (tools only, stateless entry) lists 2026 upstreams whose
// cache metadata indicates per-request fetching, precomputed so
// FetchUserSpecificTools avoids iterating all servers on every request.
type protocolCacheEntry[T any] struct {
	items             []T
	serverIDs         []config.UpstreamMCPID
	freshFetchServers []userSpecificServer
}

// isPrivateScopeWithoutPrefix reports whether an upstream's tools are
// unroutable and must be excluded from tools/list.
func isPrivateScopeWithoutPrefix(s upstream.ActiveMCPServer) bool {
	cfg := s.Config()
	if cfg.Prefix != "" {
		return false
	}
	return cfg.UserSpecificList || s.ToolsCacheMetadata().CacheScope == upstream.CacheScopePrivate
}

// privateScopeServersWithoutPrefix returns the set of connected upstreams whose
// tools must be excluded from tools/list, logging one warning per server so
// operators can diagnose the missing tools.
func (m *mcpBrokerImpl) privateScopeServersWithoutPrefix() map[config.UpstreamMCPID]struct{} {
	var excluded map[config.UpstreamMCPID]struct{}
	for id, mgr := range m.mcpServers {
		if !isPrivateScopeWithoutPrefix(mgr) {
			continue
		}
		if excluded == nil {
			excluded = make(map[config.UpstreamMCPID]struct{})
		}
		excluded[id] = struct{}{}
		m.logger.Warn("private-scope server has no prefix configured, tools excluded from listing",
			"server", mgr.MCPName(),
			"reason", "tools would be unroutable without prefix for LookupPrefix fallback")
	}
	return excluded
}

// rebuildProtocolCaches partitions the current gateway server tools and
// prompts into stateful (2025) and stateless (2026) sets based on each
// upstream server's supportedVersions. Broker meta-tools (those without
// kuadrant/id) are included only in the stateful set.
func (m *mcpBrokerImpl) rebuildProtocolCaches() {
	// per-user servers with no prefix serve unroutable tools; exclude them
	excluded := m.privateScopeServersWithoutPrefix()

	// partition tools
	allTools := m.gatewayServer.ListTools()
	var statefulT, statelessT protocolCacheEntry[*mcp.Tool]
	statefulServersSeen := make(map[config.UpstreamMCPID]bool)
	statelessServersSeen := make(map[config.UpstreamMCPID]bool)

	for _, gt := range allTools {
		tool := &gt.Tool

		if _, isBrokerTool := tool.Meta[brokerToolMetaKey]; isBrokerTool {
			statefulT.items = append(statefulT.items, tool)
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

		if _, isExcluded := excluded[serverID]; isExcluded {
			continue
		}

		if m.ServerSupportsVersion(serverID, protocol.Version2025) {
			statefulT.items = append(statefulT.items, tool)
			if !statefulServersSeen[serverID] {
				statefulServersSeen[serverID] = true
				statefulT.serverIDs = append(statefulT.serverIDs, serverID)
			}
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			statelessT.items = append(statelessT.items, tool)
			if !statelessServersSeen[serverID] {
				statelessServersSeen[serverID] = true
				statelessT.serverIDs = append(statelessT.serverIDs, serverID)
			}
		}
	}
	// precompute 2026 servers needing per-request fetching (cacheScope:"private"
	// or ttlMs:0) so FetchUserSpecificTools avoids iterating all servers per request.
	// iterates all connected 2026 upstreams, not just those with cached tools —
	// a fully-private upstream may contribute zero shared tools.
	// CRD-declared userSpecificList servers are excluded (handled in startManagers).
	crdUserSpecific := make(map[config.UpstreamMCPID]bool, len(m.userSpecificServers))
	for _, s := range m.userSpecificServers {
		crdUserSpecific[s.id] = true
	}
	for id, mgr := range m.mcpServers {
		if crdUserSpecific[id] {
			continue
		}
		if _, isExcluded := excluded[id]; isExcluded {
			continue
		}
		if !m.ServerSupportsVersion(id, protocol.Version2026) {
			continue
		}
		meta := mgr.ToolsCacheMetadata()
		cfg := mgr.Config()
		srv := userSpecificServer{
			id:     id,
			name:   cfg.Name,
			url:    cfg.URL,
			prefix: cfg.Prefix,
			caCert: cfg.CACert,
		}
		if m.handler2026.ShouldFetchFresh(srv, &meta) {
			statelessT.freshFetchServers = append(statelessT.freshFetchServers, srv)
		}
	}

	m.statefulTools.Store(&statefulT)
	m.statelessTools.Store(&statelessT)

	// partition prompts
	allPrompts := m.gatewayServer.ListPrompts()
	var statefulP, statelessP protocolCacheEntry[*mcp.Prompt]
	statefulServersSeen = make(map[config.UpstreamMCPID]bool)
	statelessServersSeen = make(map[config.UpstreamMCPID]bool)

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
			statefulP.items = append(statefulP.items, prompt)
			if !statefulServersSeen[serverID] {
				statefulServersSeen[serverID] = true
				statefulP.serverIDs = append(statefulP.serverIDs, serverID)
			}
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			statelessP.items = append(statelessP.items, prompt)
			if !statelessServersSeen[serverID] {
				statelessServersSeen[serverID] = true
				statelessP.serverIDs = append(statelessP.serverIDs, serverID)
			}
		}
	}
	m.statefulPrompts.Store(&statefulP)
	m.statelessPrompts.Store(&statelessP)

	m.logger.Debug("rebuilt protocol caches",
		"statefulTools", len(statefulT.items), "statelessTools", len(statelessT.items),
		"statefulPrompts", len(statefulP.items), "statelessPrompts", len(statelessP.items))
}

// promptsForProtocol returns the pre-cached prompt set for the client's protocol version.
// Returns a shallow copy to avoid mutation by downstream filters.
func (m *mcpBrokerImpl) promptsForProtocol(isStateless bool) []*mcp.Prompt {
	if isStateless {
		if cached := m.statelessPrompts.Load(); cached != nil {
			prompts := make([]*mcp.Prompt, len(cached.items))
			copy(prompts, cached.items)
			return prompts
		}
	}

	if cached := m.statefulPrompts.Load(); cached != nil {
		prompts := make([]*mcp.Prompt, len(cached.items))
		copy(prompts, cached.items)
		return prompts
	}
	return nil
}

// toolsForProtocol returns the pre-cached tool set for the client's protocol version.
// Returns a shallow copy to avoid mutation by downstream filters.
func (m *mcpBrokerImpl) toolsForProtocol(isStateless bool) []*mcp.Tool {
	if isStateless {
		if cached := m.statelessTools.Load(); cached != nil {
			tools := make([]*mcp.Tool, len(cached.items))
			copy(tools, cached.items)
			return tools
		}
	}

	if cached := m.statefulTools.Load(); cached != nil {
		tools := make([]*mcp.Tool, len(cached.items))
		copy(tools, cached.items)
		return tools
	}
	return nil
}
