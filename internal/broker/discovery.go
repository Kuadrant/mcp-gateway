package broker

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const (
	discoverToolsName = "discover_tools"
	selectToolsName   = "select_tools"

	// brokerToolMetaKey marks a tool as a broker-internal meta-tool
	brokerToolMetaKey = "kuadrant/broker-tool"

	// default scope store settings
	defaultScopeTTL     = 24 * time.Hour
	defaultScopeMaxSize = 10000

	gatewayInstructions = `This is an MCP Gateway that aggregates tools from multiple backend MCP servers into a single endpoint. The full tool set may be large.

To avoid loading all tool schemas upfront, use the discovery tools:
1. Call discover_tools to browse available servers, categories, and tool names (lightweight, no full schemas).
2. Call select_tools with the tool names relevant to your task. This sends a notifications/tools/list_changed notification. The filtered tool set is available on the next tools/list call, not in the current turn.
3. To change scope, call select_tools again with a new set. Pass an empty list to reset to the full tool set.`
)

// isBrokerToolName returns true if the name is a statically-registered broker meta-tool.
func isBrokerToolName(name string) bool {
	return name == discoverToolsName || name == selectToolsName
}

// IsBrokerTool returns true if the given tool is a broker-internal meta-tool,
// either by name (static tools) or by meta annotation (dynamic tools like tags).
func IsBrokerTool(tool mcp.Tool) bool {
	if isBrokerToolName(tool.Name) {
		return true
	}
	if tool.Meta != nil {
		if v, ok := tool.Meta.AdditionalFields[brokerToolMetaKey]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	return false
}

// discoveryConfig holds discovery feature configuration
type discoveryConfig struct {
	enabled   bool
	threshold int
}

// discoverToolsResponse is the response from discover_tools
type discoverToolsResponse struct {
	Servers []serverInfo `json:"servers"`
}

type serverInfo struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Hint       string   `json:"hint,omitempty"`
	Tools      []string `json:"tools"`
}

// registerDiscoveryTools adds the discover_tools and select_tools meta-tools to the broker's MCP server
func (m *mcpBrokerImpl) registerDiscoveryTools() {
	discoverTool := mcp.Tool{
		Name:        discoverToolsName,
		Description: "Browse available servers and tools. Returns server names, categories, hints, and tool names without full schemas. Use the optional category parameter to filter by category.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"category": map[string]any{
					"type":        "string",
					"description": "Filter servers by category (case-insensitive match against any element in the server's category list)",
				},
			},
		},
	}
	discoverTool.Meta = mcp.NewMetaFromMap(map[string]any{
		brokerToolMetaKey: true,
	})

	selectTool := mcp.Tool{
		Name:        selectToolsName,
		Description: "Scope your session to a specific set of tools. After calling this, subsequent tools/list calls will return only the selected tools with full schemas. Pass an empty list to reset to the full tool set.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"tools": map[string]any{
					"type":        "array",
					"description": "List of tool names to include in your session scope. Pass an empty array to reset to the full tool set.",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
			Required: []string{"tools"},
		},
	}
	selectTool.Meta = mcp.NewMetaFromMap(map[string]any{
		brokerToolMetaKey: true,
	})

	m.listeningMCPServer.AddTools(
		server.ServerTool{Tool: discoverTool, Handler: m.handleDiscoverTools},
		server.ServerTool{Tool: selectTool, Handler: m.handleSelectTools},
	)
}

// handleDiscoverTools implements the discover_tools tool handler
func (m *mcpBrokerImpl) handleDiscoverTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_, span := otel.Tracer("broker").Start(ctx, "discover_tools")
	defer span.End()

	categoryFilter := req.GetString("category", "")
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("discovery.tool", discoverToolsName),
			attribute.String("discovery.category_filter", categoryFilter),
		)
	}

	m.mcpLock.RLock()
	resp := m.buildDiscoverResponse(req.Header, categoryFilter)
	m.mcpLock.RUnlock()

	slices.SortFunc(resp.Servers, func(a, b serverInfo) int {
		return cmp.Compare(a.Name, b.Name)
	})

	if span.IsRecording() {
		span.SetAttributes(attribute.Int("discovery.servers_returned", len(resp.Servers)))
	}

	return m.marshalToolResult(resp), nil
}

// buildDiscoverResponse collects server info for visible tools, optionally filtered by category.
// caller must hold mcpLock.
func (m *mcpBrokerImpl) buildDiscoverResponse(headers map[string][]string, categoryFilter string) discoverToolsResponse {
	visible := m.getVisibleToolNames(headers)
	resp := discoverToolsResponse{Servers: []serverInfo{}}

	for _, manager := range m.mcpServers {
		cfg := manager.Config()
		if categoryFilter != "" && !matchesCategory(cfg.Category, categoryFilter) {
			continue
		}

		toolNames := m.visibleToolNames(cfg.Prefix, manager, visible)
		if len(toolNames) == 0 {
			continue
		}

		categories := cfg.Category
		if categories == nil {
			categories = []string{"uncategorised"}
		}

		resp.Servers = append(resp.Servers, serverInfo{
			Name:       cfg.Name,
			Categories: categories,
			Hint:       cfg.Hint,
			Tools:      toolNames,
		})
	}
	return resp
}

// visibleToolNames returns the prefixed names of tools on a server that are in the visible set.
func (m *mcpBrokerImpl) visibleToolNames(prefix string, manager upstream.ActiveMCPServer, visible map[string]struct{}) []string {
	var names []string
	for _, tool := range manager.GetManagedTools() {
		prefixed := prefix + tool.Name
		if _, ok := visible[prefixed]; ok {
			names = append(names, prefixed)
		}
	}
	return names
}

// handleSelectTools implements the select_tools tool handler
func (m *mcpBrokerImpl) handleSelectTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_, span := otel.Tracer("broker").Start(ctx, "select_tools")
	defer span.End()

	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return mcp.NewToolResultError("no active session"), nil
	}
	sessionID := session.SessionID()

	const maxSelectTools = 250

	rawTools := req.GetArguments()["tools"]
	toolNames, err := parseToolNames(rawTools)
	if err != nil {
		return mcp.NewToolResultError("invalid tools parameter"), nil
	}

	if len(toolNames) > maxSelectTools {
		return mcp.NewToolResultError(fmt.Sprintf("too many tools requested (max %d)", maxSelectTools)), nil
	}

	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("discovery.tool", selectToolsName),
			attribute.Int("discovery.tools_requested", len(toolNames)),
			attribute.String("discovery.session_id", sessionID),
		)
	}

	// empty list resets to full tool set
	if len(toolNames) == 0 {
		m.scopeStore.resetScope(sessionID)
		warning := m.sendToolsListChanged(sessionID)
		return m.marshalToolResult(m.selectResponse("scope reset to all tools", nil, warning)), nil
	}

	if err := m.validateToolSelection(toolNames, req.Header, sessionID); err != nil {
		return mcp.NewToolResultError("tool not available"), nil
	}

	m.scopeStore.setScope(sessionID, toolNames)
	warning := m.sendToolsListChanged(sessionID)

	if span.IsRecording() {
		span.SetAttributes(attribute.Int("discovery.tools_scoped", len(toolNames)))
	}

	status := fmt.Sprintf("scope set to %d tools", len(toolNames))
	return m.marshalToolResult(m.selectResponse(status, toolNames, warning)), nil
}

// validateToolSelection checks that every requested tool is visible and not a broker meta-tool.
func (m *mcpBrokerImpl) validateToolSelection(toolNames []string, headers map[string][]string, sessionID string) error {
	m.mcpLock.RLock()
	visible := m.getVisibleToolNames(headers)
	m.mcpLock.RUnlock()

	for _, name := range toolNames {
		if isBrokerToolName(name) {
			m.logger.Debug("select_tools: broker tool requested", "tool", name)
			return fmt.Errorf("broker tool: %s", name)
		}
		if _, ok := visible[name]; !ok {
			m.logger.Debug("select_tools: tool not available", "tool", name, "session", sessionID)
			return fmt.Errorf("not visible: %s", name)
		}
	}
	return nil
}

// selectResponse builds the map payload for a select_tools result.
func (m *mcpBrokerImpl) selectResponse(status string, tools []string, warning string) map[string]any {
	result := map[string]any{"status": status}
	if tools != nil {
		result["tools"] = tools
	}
	if warning != "" {
		result["warning"] = warning
	}
	return result
}

// sendToolsListChanged sends a notifications/tools/list_changed to the specific session.
// returns a warning string if the notification fails.
func (m *mcpBrokerImpl) sendToolsListChanged(sessionID string) string {
	err := m.listeningMCPServer.SendNotificationToSpecificClient(
		sessionID,
		"notifications/tools/list_changed",
		nil,
	)
	if err != nil {
		m.logger.Debug("failed to send tools/list_changed notification", "session", sessionID, "error", err)
		return "notification delivery failed; scope is applied but client may not refresh tool list automatically"
	}
	return ""
}

// getVisibleToolNames returns a set of tool names visible to the current request,
// after applying auth and virtual server filtering. caller must hold mcpLock.
func (m *mcpBrokerImpl) getVisibleToolNames(headers map[string][]string) map[string]struct{} {
	allTools := m.collectAllPrefixedTools()

	filtered := m.applyAuthorizedCapabilitiesFilter(headers, allTools)
	filtered = m.applyVirtualServerFilter(headers, filtered)

	visible := make(map[string]struct{}, len(filtered))
	for i := range filtered {
		visible[filtered[i].Name] = struct{}{}
	}
	return visible
}

// collectAllPrefixedTools builds the full list of prefixed tools across all servers.
// caller must hold mcpLock.
func (m *mcpBrokerImpl) collectAllPrefixedTools() []mcp.Tool {
	var all []mcp.Tool
	for _, manager := range m.mcpServers {
		cfg := manager.Config()
		prefix := cfg.Prefix
		serverID := string(cfg.ID())
		for _, tool := range manager.GetManagedTools() {
			t := mcp.Tool{Name: prefix + tool.Name}
			t.Meta = mcp.NewMetaFromMap(map[string]any{
				"kuadrant/id": serverID,
			})
			all = append(all, t)
		}
	}
	return all
}

// marshalToolResult marshals v to JSON and returns it as a tool result.
func (m *mcpBrokerImpl) marshalToolResult(v any) *mcp.CallToolResult {
	data, err := json.Marshal(v)
	if err != nil {
		m.logger.Error("failed to marshal tool response", "error", err)
		return mcp.NewToolResultError("internal error")
	}
	return mcp.NewToolResultText(string(data))
}

// applyScopeFilter filters tools based on session scope. used in the AfterListTools hook.
func (m *mcpBrokerImpl) applyScopeFilter(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return tools
	}

	state, scopedTools := m.scopeStore.getScope(session.SessionID())
	switch state {
	case scopeUnset, scopeAll:
		return m.applyThresholdFilter(tools)
	case scopeFiltered:
		return filterByScope(tools, scopedTools)
	}
	return tools
}

// filterByScope keeps only tools that are in the scoped set, plus broker meta-tools.
func filterByScope(tools []mcp.Tool, scope map[string]struct{}) []mcp.Tool {
	filtered := make([]mcp.Tool, 0, len(tools))
	for i := range tools {
		if IsBrokerTool(tools[i]) {
			filtered = append(filtered, tools[i])
			continue
		}
		if _, ok := scope[tools[i].Name]; ok {
			filtered = append(filtered, tools[i])
		}
	}
	return filtered
}

// applyThresholdFilter hides real tools when count exceeds the threshold, leaving only meta-tools.
func (m *mcpBrokerImpl) applyThresholdFilter(tools []mcp.Tool) []mcp.Tool {
	if m.discovery.threshold <= 0 {
		return tools
	}

	metaOnly := make([]mcp.Tool, 0, len(tools))
	realCount := 0
	for i := range tools {
		if IsBrokerTool(tools[i]) {
			metaOnly = append(metaOnly, tools[i])
		} else {
			realCount++
		}
	}

	if realCount <= m.discovery.threshold {
		return tools
	}

	m.logger.Debug("threshold hiding activated", "real_tools", realCount, "threshold", m.discovery.threshold)
	return metaOnly
}

// matchesCategory checks if any element in serverCategories matches the filter (case-insensitive)
func matchesCategory(serverCategories []string, filter string) bool {
	lowerFilter := strings.ToLower(filter)
	for _, cat := range serverCategories {
		if strings.ToLower(cat) == lowerFilter {
			return true
		}
	}
	return false
}

// parseToolNames extracts a []string from the raw tools argument
func parseToolNames(raw any) ([]string, error) {
	if raw == nil {
		return nil, fmt.Errorf("tools parameter is required")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array")
	}
	names := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("tool name must be a string")
		}
		names = append(names, s)
	}
	return names, nil
}
