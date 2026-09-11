package broker

import (
	"maps"
	"slices"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// statefulVersionPrefix matches every revision in the stateful protocol family
// (2025-03-26, 2025-06-18, 2025-11-25, ...). The broker requests Version2025
// (2025-11-25) at initialize, but upstreams built on older MCP SDKs downgrade
// to an earlier 2025 revision. Their tools/list and tools/call payloads are
// wire-compatible with what the broker serves downstream, so an upstream that
// advertises any 2025-series revision is served on the stateful route rather
// than silently dropped from every served set.
const statefulVersionPrefix = "2025-"

// supportsStatefulRoute reports whether any of the given advertised protocol
// versions belongs to the stateful (2025) family. Lock-free so it can be
// called from paths that already hold mcpLock (e.g. buildRoutingTable).
func supportsStatefulRoute(versions []string) bool {
	for _, v := range versions {
		if strings.HasPrefix(v, statefulVersionPrefix) {
			return true
		}
	}
	return false
}

// serverServesOnStatefulRoute reports whether the upstream advertises any
// revision in the stateful (2025) protocol family. Used in place of an exact
// Version2025 match so upstreams pinned to an older 2025 revision still
// federate. See rebuildProtocolCaches. Must NOT be called while holding
// mcpLock — serverProtocolVersions may take it; use supportsStatefulRoute
// directly on already-held versions in that case.
func (m *mcpBrokerImpl) serverServesOnStatefulRoute(id config.UpstreamMCPID) bool {
	return supportsStatefulRoute(m.serverProtocolVersions(id))
}

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

	// precompute 2026 servers needing per-request fetching (cacheScope:"private"
	// or ttlMs:0). Their tools remain out of the shared cache and are merged
	// into each request after a fresh upstream fetch.
	userSpecific := make(map[config.UpstreamMCPID]bool, len(m.userSpecificServers))
	for _, s := range m.userSpecificServers {
		userSpecific[s.id] = true
	}
	freshToolServers := make(map[config.UpstreamMCPID]struct{})
	var freshFetchServers []userSpecificServer
	for id, mgr := range m.mcpServers {
		if userSpecific[id] {
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
			freshToolServers[id] = struct{}{}
			freshFetchServers = append(freshFetchServers, srv)
		}
	}

	// partition tools
	allTools := m.gatewayServer.ListTools()
	var statefulT, statelessT protocolCacheEntry[*mcp.Tool]
	statelessT.freshFetchServers = freshFetchServers
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

		if m.serverServesOnStatefulRoute(serverID) {
			statefulT.items = append(statefulT.items, tool)
			if !statefulServersSeen[serverID] {
				statefulServersSeen[serverID] = true
				statefulT.serverIDs = append(statefulT.serverIDs, serverID)
			}
		}
		if m.ServerSupportsVersion(serverID, protocol.Version2026) {
			if _, fresh := freshToolServers[serverID]; !fresh {
				statelessT.items = append(statelessT.items, tool)
			}
			if !statelessServersSeen[serverID] {
				statelessServersSeen[serverID] = true
				statelessT.serverIDs = append(statelessT.serverIDs, serverID)
			}
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

		if m.serverServesOnStatefulRoute(serverID) {
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

// toolsAndFreshFetchServersForProtocol loads one cache entry so the returned
// tools and fresh-fetch servers describe the same routing-table snapshot.
func (m *mcpBrokerImpl) toolsAndFreshFetchServersForProtocol(isStateless bool) ([]*mcp.Tool, []userSpecificServer) {
	var cached *protocolCacheEntry[*mcp.Tool]
	if isStateless {
		cached = m.statelessTools.Load()
	}
	if cached == nil {
		cached = m.statefulTools.Load()
	}
	if cached == nil {
		return nil, nil
	}

	tools := make([]*mcp.Tool, len(cached.items))
	copy(tools, cached.items)
	return tools, cached.freshFetchServers
}

// toolsForProtocol returns the pre-cached tool set for the client's protocol version.
// Returns a shallow copy to avoid mutation by downstream filters.
func (m *mcpBrokerImpl) toolsForProtocol(isStateless bool) []*mcp.Tool {
	tools, _ := m.toolsAndFreshFetchServersForProtocol(isStateless)
	return tools
}
