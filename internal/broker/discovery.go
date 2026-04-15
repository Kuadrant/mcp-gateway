package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	discoverToolsName = "discover_tools"
	selectToolsName   = "select_tools"

	gatewayInstructions = `This is an MCP Gateway that aggregates tools from multiple backend MCP servers into a single endpoint. The full tool set may be large.

To avoid loading all tool schemas upfront, use the discovery tools:
1. Call discover_tools to browse available servers, categories, and tool names (lightweight, no full schemas).
2. Call select_tools with the tool names relevant to your task. This scopes your session — subsequent tools/list calls will return only the selected tools with full schemas.
3. To change scope, call select_tools again with a new set. Pass an empty list to reset to the full tool set.`
)

// IsBrokerTool returns true if the tool is handled by the broker directly
func IsBrokerTool(name string) bool {
	return name == discoverToolsName || name == selectToolsName
}

// sessionScopeStore tracks per-session tool scoping
type sessionScopeStore struct {
	mu     sync.RWMutex
	scopes map[string]map[string]bool // sessionID -> set of tool names
}

func newSessionScopeStore() *sessionScopeStore {
	return &sessionScopeStore{
		scopes: make(map[string]map[string]bool),
	}
}

// getScope returns the scope for a session and whether one has been set.
// Three states: (nil, false) = no scope set (default hidden),
// (empty, true) = explicitly reset to all tools, (non-empty, true) = filtered.
func (s *sessionScopeStore) getScope(sessionID string) (map[string]bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope, exists := s.scopes[sessionID]
	return scope, exists
}

func (s *sessionScopeStore) setScope(sessionID string, tools []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(tools) == 0 {
		// empty scope means "show all tools"
		s.scopes[sessionID] = map[string]bool{}
		return
	}
	scope := make(map[string]bool, len(tools))
	for _, t := range tools {
		scope[t] = true
	}
	s.scopes[sessionID] = scope
}

func (s *sessionScopeStore) removeSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scopes, sessionID)
}

type serverInfo struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Hint     string   `json:"hint,omitempty"`
	Tools    []string `json:"tools"`
}

type discoverToolsResponse struct {
	Servers []serverInfo `json:"servers"`
}

func discoveryTools(broker *mcpBrokerImpl) []server.ServerTool {
	return []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        discoverToolsName,
				Description: "Discover available tool categories and servers. Returns lightweight metadata (categories, tool names, hints) to help you identify which tools are relevant to your current task. Use this before select_tools to narrow your working set.",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"category": map[string]any{
							"type":        "string",
							"description": "Optional category filter to narrow results",
						},
					},
				},
			},
			Handler: broker.handleDiscoverTools,
		},
		{
			Tool: mcp.Tool{
				Name:        selectToolsName,
				Description: "Scope your session to a specific set of tools. After calling this, the server will send a notifications/tools/list_changed notification — end your current turn and wait for it. Subsequent tools/list requests will return only the selected tools. Call discover_tools first to identify relevant tools. Call again with a different set to re-scope, or with an empty list to reset to the full tool set.",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"tools": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of tool names to include in your session scope",
						},
					},
					Required: []string{"tools"},
				},
			},
			Handler: broker.handleSelectTools,
		},
	}
}

func (broker *mcpBrokerImpl) handleDiscoverTools(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	broker.mcpLock.RLock()
	defer broker.mcpLock.RUnlock()

	categoryFilter, _ := req.GetArguments()["category"].(string)

	// resolve auth-based tool restrictions
	allowedTools := broker.resolveAllowedTools(req.Header)
	if allowedTools != nil && len(allowedTools) == 0 {
		// auth enforced but no tools allowed
		resp := discoverToolsResponse{Servers: []serverInfo{}}
		data, _ := json.Marshal(resp)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.NewTextContent(string(data))},
		}, nil
	}

	var servers []serverInfo
	for _, manager := range broker.mcpServers {
		conf := manager.MCP.GetConfig()
		category := conf.Category
		if category == "" {
			category = "uncategorised"
		}
		if categoryFilter != "" && category != categoryFilter {
			continue
		}

		var toolNames []string
		for _, tool := range manager.GetManagedTools() {
			if allowedTools != nil {
				allowed, serverInMap := allowedTools[conf.Name]
				if !serverInMap || !slices.Contains(allowed, tool.Name) {
					continue
				}
			}
			toolNames = append(toolNames, fmt.Sprintf("%s%s", manager.MCP.GetPrefix(), tool.Name))
		}
		if len(toolNames) == 0 {
			continue
		}

		servers = append(servers, serverInfo{
			Name:     conf.Name,
			Category: category,
			Hint:     conf.Hint,
			Tools:    toolNames,
		})
	}

	resp := discoverToolsResponse{Servers: servers}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal discover response: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(data)),
		},
	}, nil
}

// resolveAllowedTools parses the x-authorized-tools header and returns the
// allowed server->tools map. Returns nil when no auth filtering applies
// (header absent and enforcement off). Returns an empty map when auth is
// enforced but no header is present, or when the header is invalid.
func (broker *mcpBrokerImpl) resolveAllowedTools(headers http.Header) map[string][]string {
	headerValues, present := headers[authorizedToolsHeader]
	if !present {
		if broker.enforceToolFilter {
			return map[string][]string{}
		}
		return nil
	}
	allowedTools, err := broker.parseAuthorizedToolsJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-authorized-tools header in discovery", "error", err)
		return map[string][]string{}
	}
	return allowedTools
}

// isToolAuthorized checks whether a prefixed tool name is permitted by the
// allowed tools map (server name -> unprefixed tool names).
func (broker *mcpBrokerImpl) isToolAuthorized(prefixedName string, allowedTools map[string][]string) bool {
	for _, manager := range broker.mcpServers {
		prefix := manager.MCP.GetPrefix()
		if !strings.HasPrefix(prefixedName, prefix) {
			continue
		}
		unprefixed := strings.TrimPrefix(prefixedName, prefix)
		serverName := manager.MCP.GetName()
		if allowed, ok := allowedTools[serverName]; ok {
			return slices.Contains(allowed, unprefixed)
		}
		return false
	}
	return false
}

func (broker *mcpBrokerImpl) handleSelectTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	toolsRaw, ok := args["tools"]
	if !ok {
		return nil, fmt.Errorf("missing required parameter: tools")
	}

	toolsSlice, ok := toolsRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array of strings")
	}

	var tools []string
	for _, t := range toolsSlice {
		name, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("each tool must be a string")
		}
		tools = append(tools, name)
	}

	// validate all tools exist and are authorized
	if len(tools) > 0 {
		allTools := broker.listeningMCPServer.ListTools()
		allowedTools := broker.resolveAllowedTools(req.Header)
		for _, name := range tools {
			if _, exists := allTools[name]; !exists {
				return nil, fmt.Errorf("tool %q does not exist or is not authorized", name)
			}
			if allowedTools != nil {
				if !broker.isToolAuthorized(name, allowedTools) {
					return nil, fmt.Errorf("tool %q does not exist or is not authorized", name)
				}
			}
		}
	}

	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return nil, fmt.Errorf("no active session")
	}

	broker.sessionScopes.setScope(session.SessionID(), tools)

	if err := broker.listeningMCPServer.SendNotificationToClient(ctx, "notifications/tools/list_changed", nil); err != nil {
		slog.Error("failed to send tools/list_changed notification", "error", err)
	}

	if len(tools) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("Session scope cleared. All tools are now available."),
			},
		}, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(fmt.Sprintf("Session scoped to %d tools. Call tools/list to see the updated set.", len(tools))),
		},
	}, nil
}

// ensureBrokerTools ensures discover_tools and select_tools are always present
// in the tool list, even if upstream filters removed them.
func (broker *mcpBrokerImpl) ensureBrokerTools(tools []mcp.Tool) []mcp.Tool {
	if broker.listeningMCPServer == nil {
		return tools
	}
	has := make(map[string]bool)
	for _, t := range tools {
		if IsBrokerTool(t.Name) {
			has[t.Name] = true
		}
	}
	for _, st := range broker.listeningMCPServer.ListTools() {
		if IsBrokerTool(st.Tool.Name) && !has[st.Tool.Name] {
			tools = append(tools, st.Tool)
		}
	}
	return tools
}

// applySessionScopeFilter filters tools based on the session's selected scope.
// When no scope is set: if tool count exceeds discoveryToolThreshold, only meta-tools
// are visible (progressive discovery). Otherwise all tools are shown.
// After select_tools with tools: only those tools + meta-tools.
// After select_tools with empty list: all tools visible.
func (broker *mcpBrokerImpl) applySessionScopeFilter(sessionID string, tools []mcp.Tool) []mcp.Tool {
	scope, exists := broker.sessionScopes.getScope(sessionID)

	// explicitly reset to all tools
	if exists && len(scope) == 0 {
		return tools
	}

	// explicit scope: filter to meta-tools + scoped tools
	if exists {
		var filtered []mcp.Tool
		for _, tool := range tools {
			if IsBrokerTool(tool.Name) || scope[tool.Name] {
				filtered = append(filtered, tool)
			}
		}
		return filtered
	}

	// no scope set: check threshold
	nonMetaCount := 0
	for _, tool := range tools {
		if !IsBrokerTool(tool.Name) {
			nonMetaCount++
		}
	}
	if nonMetaCount <= broker.discoveryToolThreshold {
		return tools
	}

	// above threshold: default hidden, only meta-tools
	var filtered []mcp.Tool
	for _, tool := range tools {
		if IsBrokerTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
