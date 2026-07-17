package broker

import (
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

// buildRoutingTable creates a routing.Table snapshot from the broker's current
// registered servers. Must be called under mcpLock (read or write).
func (m *mcpBrokerImpl) buildRoutingTable() *routing.Table {
	b := routing.NewTableBuilder()

	for id, up := range m.mcpServers {
		cfg := up.Config()
		route := &routing.ServerRoute{
			Name:             cfg.Name,
			Host:             cfg.Hostname,
			Prefix:           cfg.Prefix,
			URL:              cfg.URL,
			UserSpecificList: cfg.UserSpecificList,
		}
		if p, err := cfg.Path(); err == nil {
			route.Path = p
		}
		if cfg.TokenURLElicitation != nil {
			route.TokenURLElicitation = &routing.TokenURLElicitationRoute{
				URL: cfg.TokenURLElicitation.URL,
			}
		}

		// userSpecificList servers return per-user tools not known at
		// registration time — register the prefix for fallback matching
		if cfg.UserSpecificList && cfg.Prefix != "" {
			b.AddPrefix(cfg.Prefix, route)
		}

		for _, tool := range up.GetManagedTools() {
			served := cfg.Prefix + tool.Name
			b.AddTool(served, route)
			if hints, ok := up.GetToolHints(served); ok {
				b.AddAnnotation(string(id), served, &routing.ToolAnnotation{
					ReadOnlyHint:    hints.ReadOnlyHint,
					DestructiveHint: hints.DestructiveHint,
					IdempotentHint:  hints.IdempotentHint,
					OpenWorldHint:   hints.OpenWorldHint,
				})
			}
		}

		for _, prompt := range up.GetManagedPrompts() {
			b.AddPrompt(cfg.Prefix+prompt.Name, route)
		}
	}

	// always register broker meta-tool names so the router forwards
	// calls to the broker regardless of feature state — the broker
	// returns a proper JSON-RPC error when the feature is disabled
	b.AddBrokerTool(discoverToolsName)
	b.AddBrokerTool(selectToolsName)
	b.AddBrokerTool(listTagsName)
	b.AddBrokerTool(filterToolsByTagsName)

	return b.Build()
}

// refreshRoutingTable rebuilds the cached routing table from current state.
// Must be called under mcpLock (read or write).
func (m *mcpBrokerImpl) refreshRoutingTable() {
	m.cachedTable.Store(m.buildRoutingTable())
}

// RoutingTable returns the cached routing table snapshot. Lock-free on the
// read path; the table is rebuilt and swapped on config or tool list changes.
func (m *mcpBrokerImpl) RoutingTable() routing.RoutingTable {
	if t := m.cachedTable.Load(); t != nil {
		return t
	}
	// cold start: no table cached yet
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	m.refreshRoutingTable()
	return m.cachedTable.Load()
}
