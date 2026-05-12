/*
Package upstream is a package for managing upstream MCP servers
*/
package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
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

// PromptsAdderDeleter defines the interface for managing prompts on the gateway server
type PromptsAdderDeleter interface {
	AddPrompts(prompts ...server.ServerPrompt)
	DeletePrompts(names ...string)
	ListPrompts() map[string]*server.ServerPrompt
}

const (
	notificationToolsListChanged   = "notifications/tools/list_changed"
	notificationPromptsListChanged = "notifications/prompts/list_changed"
	gatewayServerID                = "kuadrant/id"
)

type eventType int

const (
	eventTypeTimer eventType = iota
	eventTypeToolNotification
	eventTypePromptNotification
)

// ServerValidationStatus contains the validation results for an upstream MCP server
type ServerValidationStatus struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	LastValidated     time.Time           `json:"lastValidated"`
	Message           string              `json:"message"`
	Ready             bool                `json:"ready"`
	TotalTools        int                 `json:"totalTools"`
	TotalPrompts      int                 `json:"totalPrompts"`
	InvalidTools      int                 `json:"invalidTools"`
	InvalidToolList   []InvalidToolInfo   `json:"invalidToolList,omitempty"`
	InvalidPrompts    int                 `json:"invalidPrompts"`
	InvalidPromptList []InvalidPromptInfo `json:"invalidPromptList,omitempty"`
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
	ListPrompts(context.Context, mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error)
	SupportsPrompts() bool
	SupportsPromptsListChanged() bool
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
	// serverTools is an internal copy that contains the managed MCP's tools with prefixed names. It is these that are externally available via the gateway
	serverTools []server.ServerTool
	// tools is the original set from MCP server with no prefix
	tools          []mcp.Tool
	toolsMap       map[string]*mcp.Tool
	servedToolsMap map[string]*mcp.Tool

	promptsServer PromptsAdderDeleter
	// serverPrompts is an internal copy with prefixed names
	serverPrompts []server.ServerPrompt
	// prompts is the original set from MCP server with no prefix
	prompts          []mcp.Prompt
	promptsMap       map[string]*mcp.Prompt
	servedPromptsMap map[string]*mcp.Prompt

	// toolsLock protects tools, serverTools, prompts, serverPrompts
	toolsLock sync.RWMutex
	// manageMu serializes concurrent manage calls from the ticker and notification goroutines
	manageMu sync.Mutex

	logger *slog.Logger

	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy mcpv1alpha1.InvalidToolPolicy

	stopOnce sync.Once     // ensures Stop() is only executed once
	done     chan struct{} // triggers the exit of the select and routine
	status   ServerValidationStatus
}

// DefaultTickerInterval is the default interval for backend health checks
const DefaultTickerInterval = time.Minute * 1

// NewUpstreamMCPManager creates a new MCPManager for managing a single upstream MCP server.
// The addTools and removeTools callbacks are used to update the gateway's tool registry.
// The tickerInterval controls how often the manager checks backend health (use 0 for default).
func NewUpstreamMCPManager(upstream MCP, gatewayServer ToolsAdderDeleter, promptsServer PromptsAdderDeleter, logger *slog.Logger, tickerInterval time.Duration, policy mcpv1alpha1.InvalidToolPolicy) (*MCPManager, error) {
	if gatewayServer == nil {
		return nil, fmt.Errorf("gateway server is required for upstream MCP manager")
	}
	if tickerInterval <= 0 {
		tickerInterval = DefaultTickerInterval
	}

	return &MCPManager{
		MCP:               upstream,
		gatewayServer:     gatewayServer,
		promptsServer:     promptsServer,
		tickerInterval:    tickerInterval,
		ticker:            time.NewTicker(tickerInterval),
		logger:            logger,
		invalidToolPolicy: policy,
		done:              make(chan struct{}),
		toolsMap:          map[string]*mcp.Tool{},
		servedToolsMap:    map[string]*mcp.Tool{},
		serverTools:       []server.ServerTool{},
		promptsMap:        map[string]*mcp.Prompt{},
		servedPromptsMap:  map[string]*mcp.Prompt{},
		serverPrompts:     []server.ServerPrompt{},
	}, nil
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
	man.ticker.Reset(man.tickerInterval)
	man.manage(ctx, eventTypeTimer)

	for {
		select {
		case <-ctx.Done():
			man.Stop()
		case <-man.ticker.C:
			man.logger.Debug("health check tick", "upstream mcp server", man.MCP.ID())
			man.manage(ctx, eventTypeTimer)
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
		man.ticker.Stop()
		man.removeAllPrompts()
		man.removeAllTools()
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
			switch notification.Method {
			case notificationToolsListChanged:
				man.logger.Debug("received notification", "upstream mcp server", man.MCP.ID(), "notification", notification.Method)
				man.manage(ctx, eventTypeToolNotification)
			case notificationPromptsListChanged:
				man.logger.Debug("received notification", "upstream mcp server", man.MCP.ID(), "notification", notification.Method)
				man.manage(ctx, eventTypePromptNotification)
			}
		})

		man.MCP.OnConnectionLost(func(err error) {
			// just logging for visibility as will be re-connected on next tick
			man.logger.Error("connection lost", "upstream mcp server", man.MCP.ID(), "error", err)
		})
	}
}

// manage should be the only entry point that triggers changes to tools
func (man *MCPManager) manage(ctx context.Context, event eventType) {
	man.manageMu.Lock()
	defer man.manageMu.Unlock()
	man.logger.Debug("managing connection", "upstream mcp server", man.MCP.ID(), "event type", event)
	var numberOfTools int
	var numberOfPrompts int
	// during connect the client will validate the protocol. So we don't have a separate validate requirement currently. If a client already exists it will be re-used.
	man.logger.Debug("attempting to connect", "upstream mcp server", man.MCP.ID())
	if err := man.MCP.Connect(ctx, man.registerCallbacks(ctx)); err != nil {
		err = fmt.Errorf("failed to connect to upstream mcp %s removing tools : %w", man.MCP.ID(), err)
		man.removeAllTools()
		man.removeAllPrompts()
		// we call disconnect here as we may have connected but failed to initialize
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools, numberOfPrompts, nil, nil)
		return
	}
	// there may be an active client so we also ping
	if err := man.MCP.Ping(ctx); err != nil {
		// if we fail to ping we disconnect to ensure a fresh connection next time around
		err = fmt.Errorf("upstream mcp failed to ping server %s removing tools : %w", man.MCP.ID(), err)
		man.logger.Error("ping failed", "upstream mcp server", man.MCP.ID(), "error", err)
		man.removeAllTools()
		man.removeAllPrompts()
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools, numberOfPrompts, nil, nil)
		return
	}

	var toolErr error
	var invalidTools []InvalidToolInfo
	if !man.shouldFetchTools(event) {
		man.logger.Debug("not fetching tools", "event", event, "upstream mcp server", man.MCP.ID(), "waiting for notification", notificationToolsListChanged)
	} else {
		man.logger.Debug("fetching tools", "upstream mcp server", man.MCP.ID())
		current, fetched, err := man.getTools(ctx)
		if err != nil {
			toolErr = fmt.Errorf("upstream mcp failed to list tools server %s : %w", man.MCP.ID(), err)
			man.logger.Error("failed to list tools", "upstream mcp server", man.MCP.ID(), "error", toolErr)
		} else {
			validTools, invalids := ValidateTools(fetched)
			if len(invalids) > 0 {
				man.logger.Error("invalid tools detected", "upstream mcp server", man.MCP.ID(), "invalid", len(invalids), "valid", len(validTools))
				for _, info := range invalids {
					man.logger.Error("invalid tool", "upstream mcp server", man.MCP.ID(), "tool", info.Name, "errors", info.Errors)
				}
				invalidTools = invalids
				if man.invalidToolPolicy == mcpv1alpha1.InvalidToolPolicyRejectServer {
					toolErr = fmt.Errorf("upstream mcp %s rejected: %d invalid tools found", man.MCP.ID(), len(invalids))
					man.removeAllTools()
				} else {
					fetched = validTools
				}
			}

			if toolErr == nil {
				toAdd, toRemove := man.diffTools(current, fetched)
				if conflictErr := man.findToolConflicts(toAdd); conflictErr != nil {
					toolErr = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.MCP.ID(), conflictErr)
					man.logger.Error("tool conflict detected", "upstream mcp server", man.MCP.ID(), "error", toolErr)
				} else {
					man.toolsLock.Lock()
					man.tools = fetched
					numberOfTools = len(fetched)
					man.toolsMap = make(map[string]*mcp.Tool, len(fetched))
					man.servedToolsMap = make(map[string]*mcp.Tool, len(fetched))
					for i := range fetched {
						man.toolsMap[fetched[i].Name] = &fetched[i]
						toolName := prefixedName(man.MCP.GetPrefix(), fetched[i].Name)
						man.servedToolsMap[toolName] = &fetched[i]
					}
					man.logger.Debug("updating gateway tools", "upstream mcp server", man.MCP.ID(), "adding", len(toAdd), "removing", len(toRemove))
					if len(toRemove) > 0 {
						man.gatewayServer.DeleteTools(toRemove...)
					}
					if len(toAdd) > 0 {
						man.gatewayServer.AddTools(toAdd...)
					}
					man.serverTools = slices.DeleteFunc(man.serverTools, func(tool server.ServerTool) bool {
						return slices.Contains(toRemove, tool.Tool.Name)
					})
					man.serverTools = append(man.serverTools, toAdd...)
					man.logger.Debug("internal tools", "upstream mcp server", man.MCP.ID(), "total", len(man.serverTools))
					man.toolsLock.Unlock()
				}
			}
		}
	}

	var promptErr error
	var invalidPrompts []InvalidPromptInfo
	if man.promptsServer != nil && man.MCP.SupportsPrompts() && man.shouldFetchPrompts(event) {
		currentPrompts, fetchedPrompts, listErr := man.getPrompts(ctx)
		if listErr != nil {
			man.logger.Error("failed to list prompts", "upstream mcp server", man.MCP.ID(), "error", listErr)
		} else {
			validPrompts, invalids := ValidatePrompts(fetchedPrompts)
			if len(invalids) > 0 {
				man.logger.Error("invalid prompts detected", "upstream mcp server", man.MCP.ID(), "invalid", len(invalids), "valid", len(validPrompts))
				for _, info := range invalids {
					man.logger.Error("invalid prompt", "upstream mcp server", man.MCP.ID(), "prompt", info.Name, "errors", info.Errors)
				}
				invalidPrompts = invalids
				fetchedPrompts = validPrompts
			}

			toAddPrompts, toRemovePrompts := man.diffPrompts(currentPrompts, fetchedPrompts)
			if conflictErr := man.findPromptConflicts(toAddPrompts); conflictErr != nil {
				promptErr = fmt.Errorf("upstream mcp failed to add prompts to gateway %s : %w", man.MCP.ID(), conflictErr)
				man.logger.Error("prompt conflict detected", "upstream mcp server", man.MCP.ID(), "error", promptErr)
			} else {
				numberOfPrompts = len(fetchedPrompts)
				man.toolsLock.Lock()
				man.prompts = fetchedPrompts
				man.promptsMap = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				man.servedPromptsMap = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				for i := range fetchedPrompts {
					man.promptsMap[fetchedPrompts[i].Name] = &fetchedPrompts[i]
					promptName := prefixedName(man.MCP.GetPrefix(), fetchedPrompts[i].Name)
					man.servedPromptsMap[promptName] = &fetchedPrompts[i]
				}
				man.logger.Debug("updating gateway prompts", "upstream mcp server", man.MCP.ID(), "adding", len(toAddPrompts), "removing", len(toRemovePrompts))
				if len(toRemovePrompts) > 0 {
					man.promptsServer.DeletePrompts(toRemovePrompts...)
				}
				if len(toAddPrompts) > 0 {
					man.promptsServer.AddPrompts(toAddPrompts...)
				}
				man.serverPrompts = slices.DeleteFunc(man.serverPrompts, func(prompt server.ServerPrompt) bool {
					return slices.Contains(toRemovePrompts, prompt.Prompt.Name)
				})
				man.serverPrompts = append(man.serverPrompts, toAddPrompts...)
				man.toolsLock.Unlock()
			}
		}
	}
	man.setStatus(errors.Join(toolErr, promptErr), numberOfTools, numberOfPrompts, invalidTools, invalidPrompts)
}

func (man *MCPManager) shouldFetchTools(event eventType) bool {
	if !man.MCP.SupportsToolsListChanged() {
		return true
	}
	if event == eventTypeToolNotification {
		return true
	}
	return event == eventTypeTimer && len(man.serverTools) == 0
}

func (man *MCPManager) shouldFetchPrompts(event eventType) bool {
	if !man.MCP.SupportsPromptsListChanged() {
		return true
	}
	if event == eventTypePromptNotification {
		return true
	}
	return event == eventTypeTimer && len(man.serverPrompts) == 0
}

// GetStatus returns the current status of the MCP Server
// no locking is done here as it is expected to be called multiple times
func (man *MCPManager) GetStatus() ServerValidationStatus {
	return man.status
}

func (man *MCPManager) setStatus(err error, toolCount int, promptCount int, invalidTools []InvalidToolInfo, invalidPrompts []InvalidPromptInfo) {
	man.status.ID = string(man.MCP.ID())
	man.status.LastValidated = time.Now()
	man.status.Name = man.MCPName()
	man.status.InvalidTools = len(invalidTools)
	man.status.InvalidToolList = invalidTools
	man.status.InvalidPrompts = len(invalidPrompts)
	man.status.InvalidPromptList = invalidPrompts
	if err != nil {
		man.status.Message = err.Error()
		man.status.Ready = false
		return
	}
	man.status.TotalTools = toolCount
	man.status.TotalPrompts = promptCount
	man.status.Ready = true
	man.status.Message = fmt.Sprintf("server added successfully. Total tools added %d. Total prompts added %d", toolCount, promptCount)
}

func (man *MCPManager) findToolConflicts(mcpTools []server.ServerTool) error {
	gatewayServerTools := man.gatewayServer.ListTools()
	var conflictingToolNames []string
	for _, tool := range mcpTools {
		for existingToolName, existingToolInfo := range gatewayServerTools {
			existingTool := existingToolInfo.Tool
			if existingTool.Meta == nil || existingTool.Meta.AdditionalFields == nil {
				man.logger.Error("unable to check conflict, tool meta is nil", "upstream mcp server", man.MCP.ID(), "tool", existingToolName)
				continue
			}
			existingToolID, ok := existingTool.Meta.AdditionalFields[gatewayServerID]
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
				conflictingToolNames = append(conflictingToolNames, tool.Tool.GetName())
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

// GetServedManagedTool will return the tool if present that is actually being served by the gateway.
// It expects a prefixed tool if a prefix is present.
// returns the map pointer directly to avoid per-lookup alloc -- callers must not modify.
func (man *MCPManager) GetServedManagedTool(toolName string) *mcp.Tool {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.servedToolsMap[toolName]
}

// SetToolsForTesting sets the tools directly for testing purposes.
// This bypasses the normal tool discovery flow and should only be used in tests.
// TODO look to remove the need for this
func (man *MCPManager) SetToolsForTesting(tools []mcp.Tool) {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	man.tools = tools
	// set a tools map for quick look up by other functions
	for i := range tools {
		man.toolsMap[tools[i].Name] = &tools[i]
		man.servedToolsMap[prefixedName(man.MCP.GetPrefix(), tools[i].Name)] = &tools[i]
	}
}

// SetStatusForTesting sets the status directly for testing purposes.
// This bypasses the normal status update flow and should only be used in tests.
func (man *MCPManager) SetStatusForTesting(status ServerValidationStatus) {
	man.status = status
}

func (man *MCPManager) removeAllTools() {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	toolsToRemove := make([]string, 0, len(man.serverTools))
	man.logger.Debug("removing tools from gateway", "upstream mcp server", man.MCP.ID(), "total", len(man.serverTools))
	for _, tool := range man.serverTools {
		man.logger.Debug("removing tool from server ", "upstream mcp server", man.MCP.ID(), "tool", tool.Tool.Name)
		toolsToRemove = append(toolsToRemove, tool.Tool.Name)
	}
	man.serverTools = []server.ServerTool{}
	man.tools = []mcp.Tool{}
	man.toolsMap = map[string]*mcp.Tool{}
	man.servedToolsMap = map[string]*mcp.Tool{}
	man.gatewayServer.DeleteTools(toolsToRemove...)
	man.logger.Debug("removed all tools", "upstream mcp server", man.MCP.ID(), "count", len(toolsToRemove))
}

func (man *MCPManager) toolToServerTool(newTool mcp.Tool) server.ServerTool {
	newTool.Name = prefixedName(man.MCP.GetPrefix(), newTool.Name)
	newTool.Meta = mcp.NewMetaFromMap(map[string]any{
		gatewayServerID: string(man.MCP.ID()),
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

func prefixedName(prefix, tool string) string {
	if prefix == "" {
		return tool
	}
	return fmt.Sprintf("%s%s", prefix, tool)
}

func (man *MCPManager) promptToServerPrompt(newPrompt mcp.Prompt) server.ServerPrompt {
	newPrompt.Name = prefixedName(man.MCP.GetPrefix(), newPrompt.Name)
	newPrompt.Meta = mcp.NewMetaFromMap(map[string]any{
		gatewayServerID: string(man.MCP.ID()),
	})
	return server.ServerPrompt{
		Prompt: newPrompt,
		Handler: func(_ context.Context, _ mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		},
	}
}

func (man *MCPManager) diffPrompts(oldPrompts, newPrompts []mcp.Prompt) ([]server.ServerPrompt, []string) {
	oldPromptMap := make(map[string]mcp.Prompt)
	for _, p := range oldPrompts {
		oldPromptMap[p.Name] = p
	}

	newPromptMap := make(map[string]mcp.Prompt)
	for _, p := range newPrompts {
		newPromptMap[p.Name] = p
	}

	addedPrompts := make([]server.ServerPrompt, 0)
	for _, newPrompt := range newPromptMap {
		if _, ok := oldPromptMap[newPrompt.Name]; !ok {
			addedPrompts = append(addedPrompts, man.promptToServerPrompt(newPrompt))
		}
	}

	removedPrompts := make([]string, 0)
	for _, oldPrompt := range oldPromptMap {
		if _, ok := newPromptMap[oldPrompt.Name]; !ok {
			removedPrompts = append(removedPrompts, prefixedName(man.MCP.GetPrefix(), oldPrompt.Name))
		}
	}

	return addedPrompts, removedPrompts
}

func (man *MCPManager) getPrompts(ctx context.Context) ([]mcp.Prompt, []mcp.Prompt, error) {
	man.toolsLock.RLock()
	prompts := make([]mcp.Prompt, len(man.prompts))
	copy(prompts, man.prompts)
	man.toolsLock.RUnlock()
	res, err := man.MCP.ListPrompts(ctx, mcp.ListPromptsRequest{})
	if err != nil {
		return prompts, prompts, fmt.Errorf("failed to get prompts: %w", err)
	}
	return prompts, res.Prompts, nil
}

func (man *MCPManager) findPromptConflicts(mcpPrompts []server.ServerPrompt) error {
	if man.promptsServer == nil {
		return nil
	}
	gatewayServerPrompts := man.promptsServer.ListPrompts()
	var conflictingPromptNames []string
	for _, prompt := range mcpPrompts {
		for existingPromptName, existingPromptInfo := range gatewayServerPrompts {
			existingPrompt := existingPromptInfo.Prompt
			if existingPrompt.Meta == nil || existingPrompt.Meta.AdditionalFields == nil {
				man.logger.Error("unable to check conflict, prompt meta is nil", "upstream mcp server", man.MCP.ID(), "prompt", existingPromptName)
				continue
			}
			existingPromptID, ok := existingPrompt.Meta.AdditionalFields[gatewayServerID]
			if !ok {
				man.logger.Error("unable to check conflict, prompt id is missing", "upstream mcp server", man.MCP.ID())
				continue
			}
			promptID, is := existingPromptID.(string)
			if !is {
				man.logger.Error("unable to check conflict, prompt id is not a string", "upstream mcp server", man.MCP.ID(), "type", reflect.TypeOf(existingPromptID))
				continue
			}
			if existingPromptName == prompt.Prompt.Name && promptID != string(man.MCP.ID()) {
				conflictingPromptNames = append(conflictingPromptNames, prompt.Prompt.Name)
			}
		}
	}
	if len(conflictingPromptNames) > 0 {
		return fmt.Errorf("conflicting prompts discovered. conflicting prompt names %v", conflictingPromptNames)
	}
	return nil
}

// GetManagedPrompts returns a copy of all prompts discovered from the upstream server.
func (man *MCPManager) GetManagedPrompts() []mcp.Prompt {
	man.toolsLock.RLock()
	result := make([]mcp.Prompt, len(man.prompts))
	copy(result, man.prompts)
	man.toolsLock.RUnlock()
	return result
}

// GetServedManagedPrompt returns the prompt if present that is being served by the gateway.
func (man *MCPManager) GetServedManagedPrompt(promptName string) *mcp.Prompt {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.servedPromptsMap[promptName]
}

// SetPromptsForTesting sets prompts directly for testing purposes.
func (man *MCPManager) SetPromptsForTesting(prompts []mcp.Prompt) {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	man.prompts = prompts
	for i := range prompts {
		man.promptsMap[prompts[i].Name] = &prompts[i]
		man.servedPromptsMap[prefixedName(man.MCP.GetPrefix(), prompts[i].Name)] = &prompts[i]
	}
}

func (man *MCPManager) removeAllPrompts() {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	if man.promptsServer == nil {
		return
	}
	promptsToRemove := make([]string, 0, len(man.serverPrompts))
	for _, prompt := range man.serverPrompts {
		promptsToRemove = append(promptsToRemove, prompt.Prompt.Name)
	}
	man.serverPrompts = []server.ServerPrompt{}
	man.prompts = []mcp.Prompt{}
	man.promptsMap = map[string]*mcp.Prompt{}
	man.servedPromptsMap = map[string]*mcp.Prompt{}
	man.promptsServer.DeletePrompts(promptsToRemove...)
}
