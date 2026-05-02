/*
Package upstream is a package for managing upstream MCP servers
*/
package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yosida95/uritemplate/v3"
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

// ResourcesAdderDeleter defines the interface for interacting with the gateway's
// resource registry. Mirrors ToolsAdderDeleter. mcp-go does not expose template
// deletion, so AddResourceTemplates is purely additive — see resource-federation
// design doc.
type ResourcesAdderDeleter interface {
	AddResources(resources ...server.ServerResource)
	DeleteResources(uris ...string)
	AddResourceTemplates(templates ...server.ServerResourceTemplate)
}

const (
	notificationToolsListChanged     = "notifications/tools/list_changed"
	notificationResourcesListChanged = "notifications/resources/list_changed"
	gatewayServerID                  = "kuadrant/id"
)

type eventType int

const (
	eventTypeNotification eventType = iota
	eventTypeTimer
)

// ServerValidationStatus contains the validation results for an upstream MCP server
type ServerValidationStatus struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	LastValidated          time.Time         `json:"lastValidated"`
	Message                string            `json:"message"`
	Ready                  bool              `json:"ready"`
	TotalTools             int               `json:"totalTools"`
	InvalidTools           int               `json:"invalidTools"`
	InvalidToolList        []InvalidToolInfo `json:"invalidToolList,omitempty"`
	TotalResources         int               `json:"totalResources"`
	TotalResourceTemplates int               `json:"totalResourceTemplates"`
}

// MCP defines the interface for the manager to interact with an MCP server
type MCP interface {
	GetName() string
	SupportsToolsListChanged() bool
	SupportsResources() bool
	SupportsResourcesListChanged() bool
	GetConfig() config.MCPServer
	ID() config.UpstreamMCPID
	GetPrefix() string
	Connect(context.Context, func()) error
	Disconnect() error
	ListTools(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	ListResources(context.Context, mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error)
	ListResourceTemplates(context.Context, mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error)
	OnNotification(func(notification mcp.JSONRPCNotification))
	OnConnectionLost(func(err error))
	Ping(context.Context) error
}

// MCPManager manages a single backend MCPServer for the broker. It does not act on behalf of clients. It is the only thing that should be connecting to the MCP Server for the broker. It handles tools updates, disconnection, notifications, liveness checks and updating the status for the MCP server. It is responsible for adding and removing tools and resources from the broker. It is intended to be long lived and have 1:1 relationship with a backend MCP server.
type MCPManager struct {
	MCP MCP
	// ticker allows for us to continue to probe and retry the backend
	ticker *time.Ticker
	// tickerInterval is the interval between backend health checks
	tickerInterval time.Duration
	gatewayServer  ToolsAdderDeleter
	// gatewayResources is the same listening MCP server, exposed via the resource-management surface.
	// In practice mcp-go's *server.MCPServer satisfies both ToolsAdderDeleter and ResourcesAdderDeleter;
	// keeping them as separate fields documents which surface each path uses.
	gatewayResources ResourcesAdderDeleter
	// serverTools is an internal copy that contains the managed MCP's tools with prefixed names. It is these that are externally available via the gateway
	serverTools []server.ServerTool
	// tools is the original set from MCP server with no prefix
	tools          []mcp.Tool
	toolsMap       map[string]*mcp.Tool
	servedToolsMap map[string]*mcp.Tool
	// toolsLock protects tools, serverTools
	toolsLock sync.RWMutex

	// resources/templates federation state. Mirrors the tool fields above. URIs in resources
	// and resourcesMap are upstream (unprefixed); URIs in serverResources and servedResourcesMap
	// have the gateway "<prefix>+" scheme prefix applied.
	serverResources    []server.ServerResource
	resources          []mcp.Resource
	resourcesMap       map[string]*mcp.Resource
	servedResourcesMap map[string]*mcp.Resource
	// resourceTemplates is the upstream template set; serverResourceTemplates is the
	// federated set served via the gateway. mcp-go has no per-template delete API,
	// so we use SetResourceTemplates wholesale on each refresh.
	resourceTemplates []mcp.ResourceTemplate
	// resourcesLock protects all resource and template fields above
	resourcesLock sync.RWMutex

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
// gatewayTools and gatewayResources are typically the same listening *server.MCPServer
// — passing them as separate interfaces documents which surface each federation path uses.
// The tickerInterval controls how often the manager checks backend health (use 0 for default).
func NewUpstreamMCPManager(upstream MCP, gatewayTools ToolsAdderDeleter, gatewayResources ResourcesAdderDeleter, logger *slog.Logger, tickerInterval time.Duration, policy mcpv1alpha1.InvalidToolPolicy) *MCPManager {
	if tickerInterval <= 0 {
		tickerInterval = DefaultTickerInterval
	}

	return &MCPManager{
		MCP:                upstream,
		gatewayServer:      gatewayTools,
		gatewayResources:   gatewayResources,
		tickerInterval:     tickerInterval,
		ticker:             time.NewTicker(tickerInterval),
		logger:             logger,
		invalidToolPolicy:  policy,
		done:               make(chan struct{}),
		toolsMap:           map[string]*mcp.Tool{},
		servedToolsMap:     map[string]*mcp.Tool{},
		serverTools:        []server.ServerTool{},
		resourcesMap:       map[string]*mcp.Resource{},
		servedResourcesMap: map[string]*mcp.Resource{},
		serverResources:    []server.ServerResource{},
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

// Stop gracefully shuts down the manager. It stops the ticker, removes all tools and
// resources from the gateway, disconnects from the upstream server, and waits for the
// Start goroutine to complete. Safe to call multiple times.
func (man *MCPManager) Stop() {
	man.stopOnce.Do(func() {
		man.ticker.Stop()
		man.removeAllTools()
		man.removeAllResources()
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
			case notificationToolsListChanged, notificationResourcesListChanged:
				man.logger.Debug("received notification", "upstream mcp server", man.MCP.ID(), "notification", notification)
				man.manage(ctx, eventTypeNotification)
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
	man.logger.Debug("managing connection", "upstream mcp server", man.MCP.ID(), "event type", event)
	var numberOfTools = 0
	// during connect the client will validate the protocol. So we don't have a separate validate requirement currently. If a client already exists it will be re-used.
	man.logger.Debug("attempting to connect", "upstream mcp server", man.MCP.ID())
	if err := man.MCP.Connect(ctx, man.registerCallbacks(ctx)); err != nil {
		err = fmt.Errorf("failed to connect to upstream mcp %s removing tools : %w", man.MCP.ID(), err)
		man.removeAllTools()
		// we call disconnect here as we may have connected but failed to initialize
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools, nil)
		return
	}
	// there may be an active client so we also ping
	if err := man.MCP.Ping(ctx); err != nil {
		// if we fail to ping we disconnect to ensure a fresh connection next time around
		err = fmt.Errorf("upstream mcp failed to ping server %s removing tools : %w", man.MCP.ID(), err)
		man.logger.Error("ping failed", "upstream mcp server", man.MCP.ID(), "error", err)
		man.removeAllTools()
		_ = man.MCP.Disconnect()
		man.setStatus(err, numberOfTools, nil)
		return
	}

	if !man.shouldFetchTools(event) {
		man.logger.Debug("not fetching tools", "event", event, "upstream mcp server", man.MCP.ID(), "waiting for notification", notificationToolsListChanged)
		return
	}

	man.logger.Debug("fetching tools", "upstream mcp server", man.MCP.ID())
	current, fetched, err := man.getTools(ctx)
	if err != nil {
		err = fmt.Errorf("upstream mcp failed to list tools server %s : %w", man.MCP.ID(), err)
		man.logger.Error("failed to list tools", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, numberOfTools, nil)
		return
	}

	// validate fetched tools
	validTools, invalidTools := ValidateTools(fetched)
	if len(invalidTools) > 0 {
		man.logger.Error("invalid tools detected", "upstream mcp server", man.MCP.ID(), "invalid", len(invalidTools), "valid", len(validTools))
		for _, info := range invalidTools {
			man.logger.Error("invalid tool", "upstream mcp server", man.MCP.ID(), "tool", info.Name, "errors", info.Errors)
		}
		if man.invalidToolPolicy == mcpv1alpha1.InvalidToolPolicyRejectServer {
			err = fmt.Errorf("upstream mcp %s rejected: %d invalid tools found", man.MCP.ID(), len(invalidTools))
			man.removeAllTools()
			man.setStatus(err, numberOfTools, invalidTools)
			return
		}
		// FilterOut: use only valid tools
		fetched = validTools
	}

	// always compare the tools without prefix
	toAdd, toRemove := man.diffTools(current, fetched)
	if err := man.findToolConflicts(toAdd); err != nil {
		err = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.MCP.ID(), err)
		man.logger.Error("tool conflict detected", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, numberOfTools, invalidTools)
		return
	}
	man.toolsLock.Lock()
	man.tools = fetched
	numberOfTools = len(fetched)
	// set a tools map for quick look up by other functions
	man.toolsMap = make(map[string]*mcp.Tool, len(fetched))
	man.servedToolsMap = make(map[string]*mcp.Tool, len(fetched))
	for i := range fetched {
		man.toolsMap[fetched[i].Name] = &fetched[i]
		toolName := prefixedName(man.MCP.GetPrefix(), fetched[i].Name)
		man.servedToolsMap[toolName] = &fetched[i]
	}
	// serverTools will have the prefix if one is set
	man.logger.Debug("updating gateway tools", "upstream mcp server", man.MCP.ID(), "adding", len(toAdd), "removing", len(toRemove))
	if len(toRemove) > 0 {
		man.gatewayServer.DeleteTools(toRemove...)
	}
	if len(toAdd) > 0 {
		man.gatewayServer.AddTools(toAdd...)
	}

	// rebuild our internal tools
	man.serverTools = slices.DeleteFunc(man.serverTools, func(tool server.ServerTool) bool {
		return slices.Contains(toRemove, tool.Tool.Name)
	})

	man.serverTools = append(man.serverTools, toAdd...)
	man.logger.Debug("internal tools", "upstream mcp server", man.MCP.ID(), "total", len(man.serverTools))
	man.toolsLock.Unlock()

	// discover and federate resources after tools. this is intentionally a separate step
	// so a resource discovery failure cannot tear down the working tool set.
	resourceCount, templateCount, resourceErr := man.manageResources(ctx, event)
	if resourceErr != nil {
		// surface the error in status while keeping the (successful) tool count
		man.logger.Error("failed to manage resources", "upstream mcp server", man.MCP.ID(), "error", resourceErr)
		man.setStatusWithResources(resourceErr, numberOfTools, invalidTools, resourceCount, templateCount)
		return
	}
	man.setStatusWithResources(nil, numberOfTools, invalidTools, resourceCount, templateCount)
}

// manageResources federates resources and resource templates from the upstream.
// Returns the discovered resource count, template count, and any error. When the
// upstream does not advertise resources support, this is a no-op returning (0, 0, nil).
func (man *MCPManager) manageResources(ctx context.Context, event eventType) (int, int, error) {
	if !man.MCP.SupportsResources() {
		// backend does not implement resources at all; clear any stale state and exit clean.
		man.removeAllResources()
		return 0, 0, nil
	}

	if !man.shouldFetchResources(event) {
		man.resourcesLock.RLock()
		current, currentTemplates := len(man.resources), len(man.resourceTemplates)
		man.resourcesLock.RUnlock()
		man.logger.Debug("not fetching resources", "event", event, "upstream mcp server", man.MCP.ID(), "waiting for notification", notificationResourcesListChanged)
		return current, currentTemplates, nil
	}

	current, fetched, err := man.getResources(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("upstream mcp failed to list resources server %s : %w", man.MCP.ID(), err)
	}
	toAdd, toRemove := man.diffResources(current, fetched)
	if conflictErr := man.findResourceConflicts(toAdd); conflictErr != nil {
		return 0, 0, fmt.Errorf("upstream mcp failed to add resources to gateway %s : %w", man.MCP.ID(), conflictErr)
	}

	man.resourcesLock.Lock()
	man.resources = fetched
	man.resourcesMap = make(map[string]*mcp.Resource, len(fetched))
	man.servedResourcesMap = make(map[string]*mcp.Resource, len(fetched))
	for i := range fetched {
		man.resourcesMap[fetched[i].URI] = &fetched[i]
		federated := prefixedURI(man.MCP.GetPrefix(), fetched[i].URI)
		man.servedResourcesMap[federated] = &fetched[i]
	}
	man.logger.Debug("updating gateway resources", "upstream mcp server", man.MCP.ID(), "adding", len(toAdd), "removing", len(toRemove))
	if len(toRemove) > 0 {
		man.gatewayResources.DeleteResources(toRemove...)
	}
	if len(toAdd) > 0 {
		man.gatewayResources.AddResources(toAdd...)
	}
	man.serverResources = slices.DeleteFunc(man.serverResources, func(res server.ServerResource) bool {
		return slices.Contains(toRemove, res.Resource.URI)
	})
	man.serverResources = append(man.serverResources, toAdd...)
	resourceCount := len(fetched)
	man.resourcesLock.Unlock()

	// resource templates: mcp-go has no per-template delete; we merge via AddResourceTemplates.
	// Removed upstream templates may remain until broker-level reconcile — see design doc.
	templates, templateErr := man.getResourceTemplates(ctx)
	if templateErr != nil {
		return resourceCount, 0, fmt.Errorf("upstream mcp failed to list resource templates server %s : %w", man.MCP.ID(), templateErr)
	}
	man.resourcesLock.Lock()
	man.resourceTemplates = templates
	templateCount := len(templates)
	man.resourcesLock.Unlock()
	if len(templates) > 0 {
		serverTemplates := make([]server.ServerResourceTemplate, 0, len(templates))
		for i := range templates {
			serverTemplates = append(serverTemplates, man.templateToServerTemplate(templates[i]))
		}
		man.gatewayResources.AddResourceTemplates(serverTemplates...)
	}
	return resourceCount, templateCount, nil
}

func (man *MCPManager) shouldFetchTools(event eventType) bool {
	// fetch if no support for tools list change notifications
	if !man.MCP.SupportsToolsListChanged() {
		return true
	}
	// fetch if it is a notification
	if event == eventTypeNotification {
		return true
	}
	// fetch if timer and we have no tools
	return event == eventTypeTimer && len(man.serverTools) == 0
}

// shouldFetchResources mirrors shouldFetchTools for the resource side. Notifications
// always trigger a refetch; in their absence we poll on the timer if we have no
// resources cached or the upstream does not advertise list_changed support.
func (man *MCPManager) shouldFetchResources(event eventType) bool {
	if !man.MCP.SupportsResourcesListChanged() {
		return true
	}
	if event == eventTypeNotification {
		return true
	}
	man.resourcesLock.RLock()
	empty := len(man.serverResources) == 0
	man.resourcesLock.RUnlock()
	return event == eventTypeTimer && empty
}

// GetStatus returns the current status of the MCP Server
// no locking is done here as it is expected to be called multiple times
func (man *MCPManager) GetStatus() ServerValidationStatus {
	return man.status
}

func (man *MCPManager) setStatus(err error, toolCount int, invalidTools []InvalidToolInfo) {
	man.setStatusWithResources(err, toolCount, invalidTools, 0, 0)
}

// setStatusWithResources is the canonical status setter — setStatus delegates to it
// for early-exit paths that have no resource counts yet. Keep these in sync.
func (man *MCPManager) setStatusWithResources(err error, toolCount int, invalidTools []InvalidToolInfo, resourceCount, templateCount int) {
	man.status.ID = string(man.MCP.ID())
	man.status.LastValidated = time.Now()
	man.status.Name = man.MCPName()
	man.status.InvalidTools = len(invalidTools)
	man.status.InvalidToolList = invalidTools
	if err != nil {
		man.status.Message = err.Error()
		man.status.Ready = false
		// preserve the counts the caller managed to gather before the error so /status reflects partial progress
		man.status.TotalTools = toolCount
		man.status.TotalResources = resourceCount
		man.status.TotalResourceTemplates = templateCount
		return
	}
	man.status.TotalTools = toolCount
	man.status.TotalResources = resourceCount
	man.status.TotalResourceTemplates = templateCount
	man.status.Ready = true
	man.status.Message = fmt.Sprintf("server added successfully. Total tools added %d, resources %d, resource templates %d", len(man.serverTools), resourceCount, templateCount)
}

func (man *MCPManager) findToolConflicts(mcpTools []server.ServerTool) error {
	gatewayServerTools := man.gatewayServer.ListTools()
	var conflictingToolNames []string
	for _, tool := range mcpTools {
		for existingToolName, existingToolInfo := range gatewayServerTools {
			existingTool := existingToolInfo.Tool
			// TODO revisit as this is in the tool definition
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

func prefixedName(toolPrefix, tool string) string {
	if toolPrefix == "" {
		return tool
	}
	return fmt.Sprintf("%s%s", toolPrefix, tool)
}

// prefixedURI rewrites the scheme of a resource URI to include the server prefix,
// e.g. "file:///x" with prefix "weather_" becomes "weather_+file:///x". The result
// is reversible (strip the leading "<prefix>+" from the scheme to recover the
// upstream URI). When prefix is empty, the URI passes through unchanged.
//
// We do not try to parse the URI: the +-prefix only mutates the scheme portion,
// which by RFC 3986 §3.1 is everything before the first ":". This works for both
// hierarchical URIs ("file:///x") and opaque ones ("embedded:info"), and is safe
// for RFC 6570 URI templates where the scheme is always outside the template body.
func prefixedURI(prefix, uri string) string {
	if prefix == "" {
		return uri
	}
	return fmt.Sprintf("%s+%s", prefix, uri)
}

// stripURIPrefix is the inverse of prefixedURI. It returns (upstreamURI, true) when
// the URI's scheme starts with the expected "<prefix>+" marker, and ("", false)
// otherwise. An empty prefix is treated as a pass-through (always matches).
func stripURIPrefix(prefix, federatedURI string) (string, bool) {
	if prefix == "" {
		return federatedURI, true
	}
	marker := prefix + "+"
	colon := strings.Index(federatedURI, ":")
	if colon < 0 {
		return "", false
	}
	scheme := federatedURI[:colon]
	if !strings.HasPrefix(scheme, marker) {
		return "", false
	}
	return scheme[len(marker):] + federatedURI[colon:], true
}

// getResources returns the previously-fetched resources and the freshly fetched set.
// The caller diffs the two to compute add/remove operations.
func (man *MCPManager) getResources(ctx context.Context) ([]mcp.Resource, []mcp.Resource, error) {
	man.resourcesLock.RLock()
	current := make([]mcp.Resource, len(man.resources))
	copy(current, man.resources)
	man.resourcesLock.RUnlock()
	res, err := man.MCP.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil {
		return current, current, fmt.Errorf("failed to list resources: %w", err)
	}
	return current, res.Resources, nil
}

// getResourceTemplates returns the freshly fetched template set from upstream.
func (man *MCPManager) getResourceTemplates(ctx context.Context) ([]mcp.ResourceTemplate, error) {
	res, err := man.MCP.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list resource templates: %w", err)
	}
	return res.ResourceTemplates, nil
}

// resourceToServerResource decorates an upstream resource with the gateway-internal
// id meta marker and rewrites its URI for federation. The handler is a stub: the
// router routes resources/read requests directly to the upstream via Envoy, so the
// broker's listening server never executes this handler in production. We keep it
// for parity with toolToServerTool and as a safety net should a request ever reach
// the broker (in which case we return an error instead of silently returning empty).
func (man *MCPManager) resourceToServerResource(newRes mcp.Resource) server.ServerResource {
	upstreamURI := newRes.URI
	newRes.URI = prefixedURI(man.MCP.GetPrefix(), newRes.URI)
	newRes.Meta = mcp.NewMetaFromMap(map[string]any{
		gatewayServerID: string(man.MCP.ID()),
	})
	return server.ServerResource{
		Resource: newRes,
		Handler: func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return nil, fmt.Errorf("Kuadrant MCP Broker does not forward resource reads (uri: %s)", upstreamURI)
		},
	}
}

// templateToServerTemplate is the resource-template counterpart to resourceToServerResource.
// URITemplate is rewritten the same way as a concrete URI: the scheme prefix is applied to
// the template string so clients can construct federated URIs that round-trip through the
// gateway. If parsing the prefixed template fails (it shouldn't — adding "<prefix>+" to a
// valid scheme leaves the template syntactically valid) we fall through and serve the
// original template, logging the error.
func (man *MCPManager) templateToServerTemplate(newTpl mcp.ResourceTemplate) server.ServerResourceTemplate {
	if newTpl.URITemplate != nil && newTpl.URITemplate.Template != nil {
		raw := newTpl.URITemplate.Template.Raw()
		if raw != "" {
			prefixed, err := uritemplate.New(prefixedURI(man.MCP.GetPrefix(), raw))
			if err != nil {
				man.logger.Error("failed to rewrite URI template scheme; serving template unprefixed", "upstream mcp server", man.MCP.ID(), "raw", raw, "error", err)
			} else {
				newTpl.URITemplate = &mcp.URITemplate{Template: prefixed}
			}
		}
	}
	newTpl.Meta = mcp.NewMetaFromMap(map[string]any{
		gatewayServerID: string(man.MCP.ID()),
	})
	return server.ServerResourceTemplate{
		Template: newTpl,
		Handler: func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return nil, fmt.Errorf("Kuadrant MCP Broker does not forward resource reads")
		},
	}
}

// diffResources mirrors diffTools. Returns the federated additions (with prefixed URIs
// and meta applied) and the prefixed URIs that need to be removed from the gateway.
func (man *MCPManager) diffResources(oldResources, newResources []mcp.Resource) ([]server.ServerResource, []string) {
	oldMap := make(map[string]mcp.Resource, len(oldResources))
	for _, r := range oldResources {
		oldMap[r.URI] = r
	}

	newMap := make(map[string]mcp.Resource, len(newResources))
	for _, r := range newResources {
		newMap[r.URI] = r
	}

	added := make([]server.ServerResource, 0)
	for _, r := range newMap {
		if _, ok := oldMap[r.URI]; !ok {
			added = append(added, man.resourceToServerResource(r))
		}
	}

	removed := make([]string, 0)
	for _, r := range oldMap {
		if _, ok := newMap[r.URI]; !ok {
			removed = append(removed, prefixedURI(man.MCP.GetPrefix(), r.URI))
		}
	}

	return added, removed
}

// findResourceConflicts is the resource counterpart to findToolConflicts but is
// intentionally a stub in this PR.
//
// Tool conflict detection works because mcp-go's *server.MCPServer exposes a public
// ListTools(); the manager walks it and compares against the candidate set's prefixed
// names. mcp-go has no equivalent ListResources() on the server — see the same
// discussion in prompts-federation.md — so cross-manager URI conflict detection
// must live at the broker layer, which has visibility into every manager's
// servedResourcesMap. The follow-up PR that adds JWT-based resource authorization
// will introduce broker.findResourceConflicts as part of broker.OnConfigChange's
// validation pass, mirroring the prompts plan.
//
// In the single-manager and unique-prefix-per-manager cases (the common case) no
// federated URI collision is possible because the prefix uniquely identifies the
// owning server. Operators who reuse a prefix across registrations and trip a true
// collision will see the symptom as a non-deterministic backend selection by
// GetServerInfoByResourceURI. The design doc calls this out under URI Namespacing
// → Conflict detection.
func (man *MCPManager) findResourceConflicts(candidates []server.ServerResource) error {
	_ = candidates
	return nil
}

// GetManagedResources returns a copy of all resources discovered from the upstream
// server. The returned resources have their original URIs without the gateway prefix.
func (man *MCPManager) GetManagedResources() []mcp.Resource {
	man.resourcesLock.RLock()
	defer man.resourcesLock.RUnlock()
	result := make([]mcp.Resource, len(man.resources))
	copy(result, man.resources)
	return result
}

// GetServedManagedResource returns the upstream resource keyed by its federated URI,
// or nil if no such resource is currently served by this manager. Returns the map
// pointer directly to avoid a per-lookup alloc — callers must not modify.
func (man *MCPManager) GetServedManagedResource(federatedURI string) *mcp.Resource {
	man.resourcesLock.RLock()
	defer man.resourcesLock.RUnlock()
	return man.servedResourcesMap[federatedURI]
}

// SetResourcesForTesting bypasses upstream discovery and seeds the manager's resource
// state directly. Test-only.
func (man *MCPManager) SetResourcesForTesting(resources []mcp.Resource) {
	man.resourcesLock.Lock()
	defer man.resourcesLock.Unlock()
	man.resources = resources
	man.resourcesMap = make(map[string]*mcp.Resource, len(resources))
	man.servedResourcesMap = make(map[string]*mcp.Resource, len(resources))
	for i := range resources {
		man.resourcesMap[resources[i].URI] = &resources[i]
		man.servedResourcesMap[prefixedURI(man.MCP.GetPrefix(), resources[i].URI)] = &resources[i]
	}
}

func (man *MCPManager) removeAllResources() {
	man.resourcesLock.Lock()
	defer man.resourcesLock.Unlock()
	if man.gatewayResources != nil && len(man.serverResources) > 0 {
		urisToRemove := make([]string, 0, len(man.serverResources))
		for _, r := range man.serverResources {
			urisToRemove = append(urisToRemove, r.Resource.URI)
		}
		man.gatewayResources.DeleteResources(urisToRemove...)
	}
	man.serverResources = []server.ServerResource{}
	man.resources = []mcp.Resource{}
	man.resourcesMap = map[string]*mcp.Resource{}
	man.servedResourcesMap = map[string]*mcp.Resource{}
	// templates: mcp-go offers no per-template delete and we share the gateway server
	// with sibling managers, so we do not unregister templates here (would affect siblings).
	man.resourceTemplates = nil
}
