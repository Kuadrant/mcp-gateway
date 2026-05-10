package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	// ToolDiscoverTools lists lightweight metadata for federated servers (names, categories, hints, tool names).
	ToolDiscoverTools = "discover_tools"
	// ToolSelectTools scopes the session to a subset of visible tools; empty tools resets selection.
	ToolSelectTools        = "select_tools"
	gatewayServerMetaKey   = "kuadrant/id"
	discoveryUncategorised = "uncategorised"
)

type discoveryServerEntry struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Hint       string   `json:"hint"`
	Tools      []string `json:"tools"`
}

type discoveryCatalog struct {
	Servers []discoveryServerEntry `json:"servers"`
}

// IsBrokerTool reports built-in gateway meta-tools used for tool discovery.
func IsBrokerTool(name string) bool {
	return name == ToolDiscoverTools || name == ToolSelectTools
}

func (broker *mcpBrokerImpl) registerDiscoveryTools() {
	if !broker.discoveryToolsEnabled {
		return
	}
	dt := mcp.NewTool(
		ToolDiscoverTools,
		mcp.WithDescription("Browse federated MCP servers with lightweight metadata (categories, hints, tool names). Optional argument: category substring filter."),
	)
	st := mcp.NewTool(
		ToolSelectTools,
		mcp.WithDescription("Scope this session to the listed tool names (full federated names). Pass an empty tools array to reset to the full visible tool set. Triggers tools/list_changed."),
	)
	broker.listeningMCPServer.AddTool(dt, broker.handleDiscoverTools)
	broker.listeningMCPServer.AddTool(st, broker.handleSelectTools)
}

func (broker *mcpBrokerImpl) clearDiscoverySelection(sessionID string) {
	broker.discoveryScope.Delete(sessionID)
}

func (broker *mcpBrokerImpl) gatewayToolSnapshot() []mcp.Tool {
	reg := broker.listeningMCPServer.ListTools()
	if len(reg) == 0 {
		return nil
	}
	out := make([]mcp.Tool, 0, len(reg))
	for name, st := range reg {
		if IsBrokerTool(name) {
			continue
		}
		out = append(out, st.Tool)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (broker *mcpBrokerImpl) visibleUpstreamTools(headers http.Header) []mcp.Tool {
	tools := broker.gatewayToolSnapshot()
	if len(tools) == 0 {
		return nil
	}
	tools = broker.applyAuthorizedCapabilitiesFilter(headers, tools)
	tools = broker.applyVirtualServerFilter(headers, tools)
	return tools
}

func (broker *mcpBrokerImpl) federatedToolNames() map[string]struct{} {
	snap := broker.gatewayToolSnapshot()
	m := make(map[string]struct{}, len(snap))
	for i := range snap {
		m[snap[i].Name] = struct{}{}
	}
	return m
}

func toolUpstreamID(t mcp.Tool) (config.UpstreamMCPID, bool) {
	if t.Meta == nil || t.Meta.AdditionalFields == nil {
		return "", false
	}
	raw, ok := t.Meta.AdditionalFields[gatewayServerMetaKey]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return config.UpstreamMCPID(s), ok
}

func catalogCategories(cfg config.MCPServer) []string {
	if len(cfg.Category) == 0 {
		return []string{discoveryUncategorised}
	}
	out := make([]string, len(cfg.Category))
	copy(out, cfg.Category)
	return out
}

func categoriesMatchFilter(cats []string, filterLower string) bool {
	if filterLower == "" {
		return true
	}
	for _, c := range cats {
		if strings.Contains(strings.ToLower(strings.TrimSpace(c)), filterLower) {
			return true
		}
	}
	return false
}

func (broker *mcpBrokerImpl) handleDiscoverTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !broker.discoveryToolsEnabled {
		return mcp.NewToolResultError("tool not found"), nil
	}
	categoryFilter := ""
	if args := req.GetArguments(); args != nil {
		if c, ok := args["category"].(string); ok {
			categoryFilter = strings.TrimSpace(c)
		}
	}
	filterLower := strings.ToLower(categoryFilter)

	visible := broker.visibleUpstreamTools(req.Header)
	if len(visible) == 0 {
		data, err := json.Marshal(discoveryCatalog{Servers: []discoveryServerEntry{}})
		if err != nil {
			return nil, fmt.Errorf("marshal catalog: %w", err)
		}
		return mcp.NewToolResultText(string(data)), nil
	}

	broker.mcpLock.RLock()
	cfgByID := make(map[config.UpstreamMCPID]config.MCPServer, len(broker.mcpServers))
	for id, man := range broker.mcpServers {
		cfgByID[id] = man.MCP.GetConfig()
	}
	broker.mcpLock.RUnlock()

	byServer := make(map[config.UpstreamMCPID]*discoveryServerEntry)
	for i := range visible {
		id, ok := toolUpstreamID(visible[i])
		if !ok {
			continue
		}
		entry, ok := byServer[id]
		if !ok {
			cfg := cfgByID[id]
			entry = &discoveryServerEntry{
				Name:       cfg.Name,
				Categories: catalogCategories(cfg),
				Hint:       cfg.Hint,
				Tools:      nil,
			}
			byServer[id] = entry
		}
		entry.Tools = append(entry.Tools, visible[i].Name)
	}

	servers := make([]discoveryServerEntry, 0, len(byServer))
	for _, e := range byServer {
		sort.Strings(e.Tools)
		if !categoriesMatchFilter(e.Categories, filterLower) {
			continue
		}
		servers = append(servers, *e)
	}
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

	raw, err := json.Marshal(discoveryCatalog{Servers: servers})
	if err != nil {
		return nil, fmt.Errorf("marshal catalog: %w", err)
	}
	return mcp.NewToolResultText(string(raw)), nil
}

func (broker *mcpBrokerImpl) handleSelectTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !broker.discoveryToolsEnabled {
		return mcp.NewToolResultError("tool not found"), nil
	}
	args := req.GetArguments()
	if args == nil {
		return mcp.NewToolResultError("missing arguments"), nil
	}
	rawTools, ok := args["tools"]
	if !ok {
		return mcp.NewToolResultError("missing tools array"), nil
	}
	list, ok := rawTools.([]any)
	if !ok {
		return mcp.NewToolResultError("tools must be an array of strings"), nil
	}
	names := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return mcp.NewToolResultError("each tool name must be a string"), nil
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if IsBrokerTool(s) {
			continue
		}
		names = append(names, s)
	}
	slices.Sort(names)
	names = slices.Compact(names)

	sess := server.ClientSessionFromContext(ctx)
	if sess == nil {
		return mcp.NewToolResultError("no active MCP session"), nil
	}

	federated := broker.federatedToolNames()
	visible := broker.visibleUpstreamTools(req.Header)
	visibleSet := make(map[string]struct{}, len(visible))
	for i := range visible {
		visibleSet[visible[i].Name] = struct{}{}
	}

	if len(names) == 0 {
		broker.discoveryScope.Set(sess.SessionID(), discoveryScopeAll, nil)
		notifyErr := broker.listeningMCPServer.SendNotificationToClient(ctx, mcp.MethodNotificationToolsListChanged, nil)
		body := map[string]any{"ok": true, "scoped_tools": []string{}}
		if notifyErr != nil {
			body["notification_error"] = notifyErr.Error()
		}
		raw, _ := json.Marshal(body)
		return mcp.NewToolResultText(string(raw)), nil
	}

	var notFound, notAuth []string
	for _, n := range names {
		if _, ok := visibleSet[n]; ok {
			continue
		}
		if _, ok := federated[n]; ok {
			notAuth = append(notAuth, n)
		} else {
			notFound = append(notFound, n)
		}
	}
	if len(notFound) > 0 || len(notAuth) > 0 {
		msg, _ := json.Marshal(map[string]any{
			"ok":             false,
			"not_found":      notFound,
			"not_authorised": notAuth,
		})
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Type: mcp.ContentTypeText, Text: string(msg)}},
			IsError: true,
		}, nil
	}

	selected := make(map[string]struct{}, len(names))
	for _, n := range names {
		selected[n] = struct{}{}
	}
	broker.discoveryScope.Set(sess.SessionID(), discoveryScopeFiltered, selected)

	notifyErr := broker.listeningMCPServer.SendNotificationToClient(ctx, mcp.MethodNotificationToolsListChanged, nil)
	outNames := append([]string(nil), names...)
	sort.Strings(outNames)
	body := map[string]any{"ok": true, "scoped_tools": outNames}
	if notifyErr != nil {
		body["notification_error"] = notifyErr.Error()
	}
	raw, _ := json.Marshal(body)
	return mcp.NewToolResultText(string(raw)), nil
}

func (broker *mcpBrokerImpl) applyProgressiveDiscoveryFilter(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	if !broker.discoveryToolsEnabled {
		return tools
	}
	var meta, rest []mcp.Tool
	for i := range tools {
		if IsBrokerTool(tools[i].Name) {
			meta = append(meta, tools[i])
			continue
		}
		rest = append(rest, tools[i])
	}
	if len(rest) == 0 {
		return tools
	}
	origVisibleCount := len(rest)

	sid := ""
	if sess := server.ClientSessionFromContext(ctx); sess != nil {
		sid = sess.SessionID()
	}
	scopeKind, selected, hasScope := broker.discoveryScope.Get(sid)
	if !hasScope {
		scopeKind = discoveryScopeUnset
	}

	var filteredRest []mcp.Tool
	switch scopeKind {
	case discoveryScopeFiltered:
		if selected == nil {
			selected = map[string]struct{}{}
		}
		for i := range rest {
			if _, ok := selected[rest[i].Name]; ok {
				filteredRest = append(filteredRest, rest[i])
			}
		}
	case discoveryScopeAll, discoveryScopeUnset:
		filteredRest = append(filteredRest, rest...)
	default:
		filteredRest = append(filteredRest, rest...)
	}

	thresholdHide := broker.discoveryToolThreshold > 0 && origVisibleCount > broker.discoveryToolThreshold
	if !thresholdHide {
		out := append(append([]mcp.Tool{}, meta...), filteredRest...)
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}

	if scopeKind == discoveryScopeUnset {
		broker.logger.Debug("discovery threshold hiding active", "visible_tools", origVisibleCount, "threshold", broker.discoveryToolThreshold)
		sort.Slice(meta, func(i, j int) bool { return meta[i].Name < meta[j].Name })
		return meta
	}

	out := append(append([]mcp.Tool{}, meta...), filteredRest...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
