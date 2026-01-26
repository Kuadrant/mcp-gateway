/*
Package upstream is a package for managing upstream MCP servers
*/
package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolsAdderDeleter defines the interface for interacting with the gateway directly
type ToolsAdderDeleter interface {
	// AddToolsFunc is a callback function for adding tools to the gateway server
	AddTools(tools ...server.ServerTool)

	// RemoveToolsFunc is a callback function for removing tools from the gateway server by name
	DeleteTools(tools ...string)

	// ListTools will list all tools currently registered with the gateway
	ListTools() map[string]*server.ServerTool
}

const (
	notificationToolsListChanged = "notifications/tools/list_changed"
)

// ServerValidationStatus contains the validation results for an upstream MCP server
type ServerValidationStatus struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	LastValidated time.Time `json:"lastValidated"`
	Message       string    `json:"message"`
	Ready         bool      `json:"ready"`
	TotalTools    int       `json:"totalTools"`
}

// MCP defines the interface for the manager to interact with an MCP server
type MCP interface {
	GetName() string
	SupportsToolsListChanged() bool
	GetConfig() config.MCPServer
	ID() config.UpstreamMCPID
	GetPrefix() string
	Connect(context.Context, func()) error
	Disconnect() error
	ListTools(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	OnNotification(func(notification mcp.JSONRPCNotification))
	OnConnectionLost(func(err error))
	Ping(context.Context) error
}

// MCPManager manages a single backend MCPServer for the broker. It does not act on behalf of clients. It is the only thing that should be connecting to the MCP Server for the broker. It handles tools updates, disconnection, notifications, liveness checks and updating the status for the MCP server. It is responsible for adding and removing tools to the broker. It is intended to be long lived and have 1:1 relationship with a backend MCP server.
type MCPManager struct {
	MCP MCP
	// ticker allows for us to continue to probe and retry the backend
	ticker *time.Ticker
	// tickerInterval is the interval between backend health checks
	tickerInterval time.Duration
	gatewayServer  ToolsAdderDeleter
	// serverTools contains the managed MCP's tools with prefixed names. It is these that are externally available via the gateway
	serverTools []server.ServerTool
	// tools is the original set from MCP server with no prefix
	tools []mcp.Tool
	// toolsMap is a map of from tool name to tool for quick look up
	toolsMap map[string]mcp.Tool
	//servedToolsMap is a map of the served tools names (including prefix if any)
	servedToolsMap map[string]mcp.Tool
	// toolsLock protects tools, serverTools
	toolsLock sync.RWMutex

	logger *slog.Logger

	stopOnce sync.Once     // ensures Stop() is only executed once
	done     chan struct{} // triggers the exit of the select and routine
	status   ServerValidationStatus
}

// DefaultTickerInterval is the default interval for backend health checks
const DefaultTickerInterval = time.Minute * 1

// NewUpstreamMCPManager creates a new MCPManager for managing a single upstream MCP server.
// The addTools and removeTools callbacks are used to update the gateway's tool registry.
// The tickerInterval controls how often the manager checks backend health (use 0 for default).
func NewUpstreamMCPManager(upstream MCP, gatewaySever ToolsAdderDeleter, logger *slog.Logger, tickerInterval time.Duration) *MCPManager {
	if tickerInterval <= 0 {
		tickerInterval = DefaultTickerInterval
	}

	return &MCPManager{
		MCP:            upstream,
		gatewayServer:  gatewaySever,
		tickerInterval: tickerInterval,
		logger:         logger,
		done:           make(chan struct{}),
		toolsMap:       map[string]mcp.Tool{},
		servedToolsMap: map[string]mcp.Tool{},
	}
}

// MCPName returns the name of the upstream MCP server being managed
func (man *MCPManager) MCPName() string {
	return man.MCP.GetName()
}

// Start begins the management loop for the upstream MCP server. It connects to
// the server, discovers tools, and periodically validates the connection. It also
// registers notification callbacks to handle tool list changes. This method blocks
// until Stop is called or the context is cancelled.
func (man *MCPManager) Start(ctx context.Context) {
	man.ticker = time.NewTicker(man.tickerInterval)
	man.manage(ctx)

	for {
		select {
		case <-ctx.Done():
			man.Stop()
		case <-man.ticker.C:
			man.logger.Debug("health check tick", "upstream mcp server", man.MCP.ID())
			man.manage(ctx)
		case <-man.done:
			man.logger.Debug("shutting down manager", "upstream mcp server", man.MCP.ID())
			return
		}
	}
}

// Stop gracefully shuts down the manager. It stops the ticker, removes all tools
// from the gateway, disconnects from the upstream server, and waits for the Start
// goroutine to complete. Safe to call multiple times.
func (man *MCPManager) Stop() {
	man.stopOnce.Do(func() {
		if man.ticker != nil {
			man.ticker.Stop()
		}
		man.removeTools()
		if err := man.MCP.Disconnect(); err != nil {
			man.logger.Error("failed to disconnect during stop", "upstream mcp server", man.MCP.ID(), "error", err)
		}
		close(man.done)
		man.logger.Debug("manager stopped", "upstream mcp server", man.MCP.ID())
	})
}

func (man *MCPManager) registerCallbacks(ctx context.Context) func() {
	man.logger.Debug("registering callbacks", "upstream mcp server", man.MCP.ID())
	return func() {
		man.MCP.OnNotification(func(notification mcp.JSONRPCNotification) {
			if notification.Method == notificationToolsListChanged {
				man.logger.Debug("received notification", "upstream mcp server", man.MCP.ID(), "notification", notification)
				man.toolsLock.Lock()
				man.serverTools = []server.ServerTool{}
				man.toolsLock.Unlock()
				man.manage(ctx)
				return
			}
		})

		man.MCP.OnConnectionLost(func(err error) {
			// just logging for visibility as will be re-connected on next tick
			man.logger.Error("connection lost", "upstream mcp server", man.MCP.ID(), "error", err)
		})
	}
}

// manage should be the only entry point that triggers changes to tools
func (man *MCPManager) manage(ctx context.Context) {
	man.logger.Debug("managing connection", "upstream mcp server", man.MCP.ID())
	var numberOfTools = 0
	// during connect the client will validate the protocol. So we don't have a separate validate requirement currently. If a client already exists it will be re-used.
	man.logger.Debug("attempting to connect", "upstream mcp server", man.MCP.ID())
	man.logger.Debug("==01==> "+man.MCP.GetConfig().URL, "upstream mcp server", man.MCP.ID())
	if err := man.MCP.Connect(ctx, man.registerCallbacks(ctx)); err != nil {
		err = fmt.Errorf("failed to connect to upstream mcp %s removing tools : %w", man.MCP.ID(), err)
		man.removeTools()
		// we call disconnect here as we may have connected but failed to initialize
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools)
		return
	}
	// there may be an active client so we also ping
	if err := man.MCP.Ping(ctx); err != nil {
		err = fmt.Errorf("upstream mcp failed to ping server %s removing tools : %w", man.MCP.ID(), err)
		man.logger.Error("ping failed", "upstream mcp server", man.MCP.ID(), "error", err)
		man.removeTools()
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools)
		return
	}

	if man.hasTools() && man.MCP.SupportsToolsListChanged() {
		man.logger.Debug("tools already registered, waiting for change notification", "upstream mcp server", man.MCP.ID())
		return
	}

	man.logger.Debug("syncing tools", "upstream mcp server", man.MCP.ID())
	current, fetched, err := man.getTools(ctx)
	if err != nil {
		err = fmt.Errorf("upstream mcp failed to list tools server %s : %w", man.MCP.ID(), err)
		man.logger.Error("failed to list tools", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, numberOfTools)
		return
	}
	toAdd, toRemove := man.diffTools(current, fetched)
	if err := man.findToolConflicts(toAdd); err != nil {
		err = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.MCP.ID(), err)
		man.logger.Error("tool conflict detected", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, numberOfTools)
		return
	}
	man.toolsLock.Lock()
	man.tools = fetched
	numberOfTools = len(fetched)
	// set a tools map for quick look up by other functions
	man.toolsMap = map[string]mcp.Tool{}
	man.servedToolsMap = map[string]mcp.Tool{}
	for _, newTool := range fetched {
		man.toolsMap[newTool.Name] = newTool
		toolName := prefixedName(man.MCP.GetPrefix(), newTool.Name)
		man.servedToolsMap[toolName] = newTool
	}
	// serverTools will have the prefix if one is set
	man.logger.Debug("updating gateway tools", "upstream mcp server", man.MCP.ID(), "adding", len(toAdd), "removing", len(toRemove))
	man.gatewayServer.DeleteTools(toRemove...)
	man.gatewayServer.AddTools(toAdd...)
	man.serverTools = toAdd
	man.toolsLock.Unlock()
	man.setStatus(nil, numberOfTools)
}

// GetStatus returns the current status of the MCP Server
// no locking is done here as it is expected to be called multiple times
func (man *MCPManager) GetStatus() ServerValidationStatus {
	return man.status
}

func (man *MCPManager) hasTools() bool {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return len(man.serverTools) > 0
}

func (man *MCPManager) setStatus(err error, toolCount int) {
	man.status.ID = string(man.MCP.ID())
	man.status.LastValidated = time.Now()
	man.status.Name = man.MCPName()
	if err != nil {
		man.status.Message = err.Error()
		man.status.Ready = false
		return
	}
	man.status.TotalTools = toolCount
	man.status.Ready = true
	man.status.Message = fmt.Sprintf("server added successfully. Total tools added %d", len(man.serverTools))
}

func (man *MCPManager) findToolConflicts(mcpTools []server.ServerTool) error {
	gatewayServerTools := man.gatewayServer.ListTools()
	var conflictingToolNames []string
	for _, tool := range mcpTools {
		for existingToolName, existingToolInfo := range gatewayServerTools {
			existingTool := existingToolInfo.Tool
			// TODO revisit as this is in the tool definition
			existingToolID, ok := existingTool.Meta.AdditionalFields["id"]
			if !ok {
				// should never happen as we are adding every time
				man.logger.Error("unable to check conflict, tool id is missing", "upstream mcp server", man.MCP.ID())
				continue
			}
			toolID, is := existingToolID.(string)
			if !is {
				// also should never happen
				man.logger.Error("unable to check conflict, tool id is not a string", "upstream mcp server", man.MCP.ID(), "type", reflect.TypeOf(existingToolID))
				continue
			}

			if existingToolName == tool.Tool.GetName() && toolID != string(man.MCP.ID()) {
				man.logger.Debug("tool name conflict found", "upstream mcp server", man.MCP.ID(), "existing", existingToolName, "new", tool.Tool.GetName(), "conflicting server", toolID)
				conflictingToolNames = append(conflictingToolNames, toolID)
			}

		}
	}
	if len(conflictingToolNames) > 0 {
		return fmt.Errorf("conflicting tools discovered. conflicting tool names %v", conflictingToolNames)
	}

	return nil
}

// getTools return the existing, and new tools
func (man *MCPManager) getTools(ctx context.Context) ([]mcp.Tool, []mcp.Tool, error) {
	man.toolsLock.RLock()
	tools := make([]mcp.Tool, len(man.tools))
	copy(tools, man.tools)
	man.toolsLock.RUnlock()
	res, err := man.MCP.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return tools, tools, fmt.Errorf("failed to get tools: %w", err)
	}
	return tools, res.Tools, nil
}

// GetManagedTools returns a copy of all tools discovered from the upstream server.
// The returned tools have their original names without the gateway prefix.
func (man *MCPManager) GetManagedTools() []mcp.Tool {
	man.toolsLock.RLock()
	result := make([]mcp.Tool, len(man.tools))
	copy(result, man.tools)
	man.toolsLock.RUnlock()
	return result
}

// GetServedManagedTool will return the tool if present that is actually beng served by the gateway.
// It expects a prefixed tool if a prefix is present.
func (man *MCPManager) GetServedManagedTool(toolName string) *mcp.Tool {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	tool, ok := man.servedToolsMap[toolName]
	if ok {
		return &tool
	}
	return nil
}

// SetToolsForTesting sets the tools directly for testing purposes.
// This bypasses the normal tool discovery flow and should only be used in tests.
// TODO look to remove the need for this
func (man *MCPManager) SetToolsForTesting(tools []mcp.Tool) {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	man.tools = tools
	// set a tools map for quick look up by other functions
	for _, newTool := range tools {
		man.toolsMap[newTool.Name] = newTool
		man.servedToolsMap[prefixedName(man.MCP.GetPrefix(), newTool.Name)] = newTool
	}
}

// SetStatusForTesting sets the status directly for testing purposes.
// This bypasses the normal status update flow and should only be used in tests.
func (man *MCPManager) SetStatusForTesting(status ServerValidationStatus) {
	man.status = status
}

func (man *MCPManager) removeTools() {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	toolsToRemove := make([]string, 0, len(man.serverTools))
	for _, tool := range man.serverTools {
		toolsToRemove = append(toolsToRemove, tool.Tool.Name)
	}
	man.serverTools = nil
	man.tools = nil
	man.gatewayServer.DeleteTools(toolsToRemove...)
	man.logger.Debug("removed all tools", "upstream mcp server", man.MCP.ID(), "count", len(toolsToRemove))
}

func (man *MCPManager) toolToServerTool(newTool mcp.Tool) server.ServerTool {
	newTool.Name = prefixedName(man.MCP.GetPrefix(), newTool.Name)
	newTool.Meta = mcp.NewMetaFromMap(map[string]any{
		"id": string(man.MCP.ID()),
	})
	return server.ServerTool{
		Tool: newTool,
		Handler: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultError("Kagenti MCP Broker doesn't forward tool calls"), nil
		},
	}
}

func (man *MCPManager) diffTools(oldTools, newTools []mcp.Tool) ([]server.ServerTool, []string) {
	oldToolMap := make(map[string]mcp.Tool)
	for _, oldTool := range oldTools {
		oldToolMap[oldTool.Name] = oldTool
	}

	newToolMap := make(map[string]mcp.Tool)
	for _, newTool := range newTools {
		newToolMap[newTool.Name] = newTool
	}

	addedTools := make([]server.ServerTool, 0)
	for _, newTool := range newToolMap {
		_, ok := oldToolMap[newTool.Name]
		if !ok {
			addedTools = append(addedTools, man.toolToServerTool(newTool))
		}
	}

	removedTools := make([]string, 0)
	for _, oldTool := range oldToolMap {
		_, ok := newToolMap[oldTool.Name]
		if !ok {
			removedTools = append(removedTools, prefixedName(man.MCP.GetPrefix(), oldTool.Name))
		}
	}

	return addedTools, removedTools
}

func prefixedName(toolPrefix, tool string) string {
	if toolPrefix == "" {
		return tool
	}
	return fmt.Sprintf("%s%s", toolPrefix, tool)
}
