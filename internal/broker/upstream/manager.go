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

	"k8s.io/apimachinery/pkg/util/wait"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	notificationToolsListChanged   = "notifications/tools/list_changed"
	notificationPromptsListChanged = "notifications/prompts/list_changed"
	gatewayServerID                = "kuadrant/id"

	// expectedProtocolVersion is the version the broker proposes in its
	// legacy initialize to upstreams (the SDK client's fallback proposal),
	// reported as /status expectedVersion exactly as mark3labs' constant
	// was. drift on SDK bumps is caught by TestExpectedVersionMatchesSDKProposal.
	expectedProtocolVersion = "2025-11-25"
)

type eventType int

const (
	eventTypeTimer eventType = iota
	eventTypeToolNotification
	eventTypePromptNotification
)

// ServerValidationStatus contains the validation results for an upstream MCP server
type ServerValidationStatus struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	LastValidated      time.Time           `json:"lastValidated"`
	Message            string              `json:"message"`
	Ready              bool                `json:"ready"`
	TotalTools         int                 `json:"totalTools"`
	TotalPrompts       int                 `json:"totalPrompts"`
	InvalidTools       int                 `json:"invalidTools"`
	InvalidToolList    []InvalidToolInfo   `json:"invalidToolList,omitempty"`
	InvalidPrompts     int                 `json:"invalidPrompts"`
	InvalidPromptList  []InvalidPromptInfo `json:"invalidPromptList,omitempty"`
	ProtocolValidation ProtocolValidation  `json:"protocolValidation"`
}

// ProtocolValidation reports the MCP protocol version negotiated with the upstream.
// supportedVersion is the version the upstream replied with during initialize;
// expectedVersion is the version the broker proposed (always the latest the broker knows).
type ProtocolValidation struct {
	IsValid          bool   `json:"isValid"`
	SupportedVersion string `json:"supportedVersion"`
	ExpectedVersion  string `json:"expectedVersion"`
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
	ListTools(context.Context) (*mcp.ListToolsResult, error)
	ListPrompts(context.Context) (*mcp.ListPromptsResult, error)
	SupportsPrompts() bool
	SupportsPromptsListChanged() bool
	OnNotification(func(method string))
	OnConnectionLost(func(err error))
	Ping(context.Context) error
	// IsEnabled returns true if the server should be connected to and have its tools registered.
	IsEnabled() bool
	// ProtocolInfo returns the initialize result (including the negotiated protocol version), or nil if not yet connected.
	ProtocolInfo() *mcp.InitializeResult
	// GetToolHints returns raw annotation hints for a served (prefixed) tool name.
	GetToolHints(served string) (ToolHints, bool)
}

// ActiveMCPServer is the handle returned by Start. It exposes read-only
// queries and a Stop method that cleanly shuts down the event loop.
type ActiveMCPServer interface {
	Stop()
	MCPName() string
	GetStatus() ServerValidationStatus
	GetManagedTools() []mcp.Tool
	GetServedManagedTool(toolName string) *mcp.Tool
	GetToolHints(toolName string) (ToolHints, bool)
	GetManagedPrompts() []mcp.Prompt
	GetServedManagedPrompt(promptName string) *mcp.Prompt
	Config() config.MCPServer
}

// GatewayTool pairs a tool definition with the handler the gateway
// registers for it.
type GatewayTool struct {
	Tool    mcp.Tool
	Handler mcp.ToolHandler
}

// GatewayPrompt pairs a prompt definition with the handler the gateway
// registers for it.
type GatewayPrompt struct {
	Prompt  mcp.Prompt
	Handler mcp.PromptHandler
}

// ToolsAdderDeleter defines the interface for interacting with the gateway directly
type ToolsAdderDeleter interface {
	AddTools(tools ...GatewayTool)
	DeleteTools(tools ...string)
	ListTools() map[string]*GatewayTool
}

// PromptsAdderDeleter defines the interface for managing prompts on the gateway server
type PromptsAdderDeleter interface {
	AddPrompts(prompts ...GatewayPrompt)
	DeletePrompts(names ...string)
	ListPrompts() map[string]*GatewayPrompt
}

// NewToolResultError creates a CallToolResult with the error flag set
func NewToolResultError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// NewToolResultText creates a CallToolResult with text content
func NewToolResultText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// NoopToolHandler is a placeholder handler for pass-through tools
func NoopToolHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return NewToolResultError("Kagenti MCP Broker doesn't forward tool calls"), nil
}

// NoopPromptHandler is a placeholder handler for pass-through prompts
func NoopPromptHandler(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{}, nil
}

// MCPManager manages a single backend MCPServer for the broker. It does not act on behalf of clients. It is the only thing that should be connecting to the MCP Server for the broker. It handles tools updates, disconnection, notifications, liveness checks and updating the status for the MCP server. It is responsible for adding and removing tools to the broker. It is intended to be long lived and have 1:1 relationship with a backend MCP server.
type MCPManager struct {
	mcp MCP
	// ticker allows for us to continue to probe and retry the backend
	ticker *time.Ticker
	// tickerInterval is the interval between backend health checks
	tickerInterval time.Duration
	// backoff is used to calculate the next interval on failure
	backoff wait.Backoff
	// baseBackoff stores the initial backoff configuration for resets
	baseBackoff   wait.Backoff
	gatewayServer ToolsAdderDeleter
	// serverTools is an internal copy that contains the managed MCP's tools with prefixed names. It is these that are externally available via the gateway
	serverTools []GatewayTool
	// tools is the original set from MCP server with no prefix
	tools          []mcp.Tool
	toolsMap       map[string]*mcp.Tool
	servedToolsMap map[string]*mcp.Tool

	promptsServer PromptsAdderDeleter
	// serverPrompts is an internal copy with prefixed names
	serverPrompts []GatewayPrompt
	// prompts is the original set from MCP server with no prefix
	prompts          []mcp.Prompt
	promptsMap       map[string]*mcp.Prompt
	servedPromptsMap map[string]*mcp.Prompt

	// toolsLock protects tools, serverTools, prompts, serverPrompts
	toolsLock sync.RWMutex

	logger *slog.Logger

	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy mcpv1alpha1.InvalidToolPolicy

	// toolEvents and promptEvents funnel notifications into the Start() loop.
	// Separate channels with buffer of 1 each ensure a tool notification cannot
	// block a prompt notification (or vice versa) while still coalescing rapid
	// same-type notifications.
	toolEvents   chan struct{}
	promptEvents chan struct{}
	done         chan struct{} // closed when the event loop exits
	status       ServerValidationStatus
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

	bo := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2,
		Jitter:   0.1,
		Steps:    10, // effectively unlimited if we cap it
		Cap:      tickerInterval,
	}

	return &MCPManager{
		mcp:               upstream,
		gatewayServer:     gatewayServer,
		promptsServer:     promptsServer,
		tickerInterval:    tickerInterval,
		ticker:            time.NewTicker(tickerInterval),
		backoff:           bo,
		baseBackoff:       bo,
		logger:            logger,
		invalidToolPolicy: policy,
		toolEvents:        make(chan struct{}, 1),
		promptEvents:      make(chan struct{}, 1),
		done:              make(chan struct{}),
		toolsMap:          map[string]*mcp.Tool{},
		servedToolsMap:    map[string]*mcp.Tool{},
		serverTools:       []GatewayTool{},
		promptsMap:        map[string]*mcp.Prompt{},
		servedPromptsMap:  map[string]*mcp.Prompt{},
		serverPrompts:     []GatewayPrompt{},
	}, nil
}

// MCPName returns the name of the upstream MCP server being managed
func (man *MCPManager) MCPName() string {
	return man.mcp.GetName()
}

// Start launches the event loop in a background goroutine and returns an
// ActiveMCPServer handle. The cancel func is captured inside the returned
// wrapper so there is no shared mutable state between Start and Stop.
func (man *MCPManager) Start(ctx context.Context) ActiveMCPServer {
	ctx, cancel := context.WithCancel(ctx)
	man.ticker.Reset(man.tickerInterval)

	go func() {
		man.manage(ctx, eventTypeTimer)
		for {
			select {
			case <-ctx.Done():
				man.ticker.Stop()
				if err := man.mcp.Disconnect(); err != nil {
					man.logger.Error("failed to disconnect during stop", "upstream mcp server", man.mcp.ID(), "error", err)
				}
				man.removeAllPrompts()
				man.removeAllTools()

				close(man.done)
				man.logger.Debug("manager stopped", "upstream mcp server", man.mcp.ID())
				return
			case <-man.ticker.C:
				man.logger.DebugContext(ctx, "health check tick", "upstream mcp server", man.mcp.ID())
				man.manage(ctx, eventTypeTimer)
			case <-man.toolEvents:
				man.logger.DebugContext(ctx, "received tool notification", "upstream mcp server", man.mcp.ID())
				man.manage(ctx, eventTypeToolNotification)
			case <-man.promptEvents:
				man.logger.DebugContext(ctx, "received prompt notification", "upstream mcp server", man.mcp.ID())
				man.manage(ctx, eventTypePromptNotification)
			}
		}
	}()

	return &activeMCP{manager: man, cancel: cancel}
}

// activeMCP implements ActiveMCPServer. It holds the cancel func returned by
// context.WithCancel so that Stop can shut down the event loop without any
// shared mutable field on MCPManager.
type activeMCP struct {
	manager *MCPManager
	cancel  context.CancelFunc
}

func (a *activeMCP) Stop()                             { a.cancel(); <-a.manager.done }
func (a *activeMCP) MCPName() string                   { return a.manager.MCPName() }
func (a *activeMCP) GetStatus() ServerValidationStatus { return a.manager.GetStatus() }
func (a *activeMCP) GetManagedTools() []mcp.Tool       { return a.manager.GetManagedTools() }
func (a *activeMCP) GetServedManagedTool(t string) *mcp.Tool {
	return a.manager.GetServedManagedTool(t)
}

func (a *activeMCP) GetToolHints(t string) (ToolHints, bool) {
	return a.manager.mcp.GetToolHints(t)
}
func (a *activeMCP) GetManagedPrompts() []mcp.Prompt { return a.manager.GetManagedPrompts() }
func (a *activeMCP) GetServedManagedPrompt(p string) *mcp.Prompt {
	return a.manager.GetServedManagedPrompt(p)
}
func (a *activeMCP) Config() config.MCPServer { return a.manager.mcp.GetConfig() }

func (man *MCPManager) registerCallbacks() func() {
	man.logger.Debug("registering callbacks", "upstream mcp server", man.mcp.ID())
	// the notification handler registers before Connect so it is wired into
	// the client ahead of the session handshake; nothing sent immediately
	// after connect can slip past it
	man.mcp.OnNotification(func(method string) {
		switch method {
		case notificationToolsListChanged:
			man.logger.Debug("received notification", "upstream mcp server", man.mcp.ID(), "notification", method)
			select {
			case man.toolEvents <- struct{}{}:
			default:
			}
		case notificationPromptsListChanged:
			man.logger.Debug("received notification", "upstream mcp server", man.mcp.ID(), "notification", method)
			select {
			case man.promptEvents <- struct{}{}:
			default:
			}
		}
	})
	return func() {
		man.mcp.OnConnectionLost(func(err error) {
			// just logging for visibility as will be re-connected on next tick
			man.logger.Error("connection lost", "upstream mcp server", man.mcp.ID(), "error", err)
		})
	}
}

// manage should be the only entry point that triggers changes to tools
func (man *MCPManager) manage(ctx context.Context, event eventType) {
	man.logger.DebugContext(ctx, "managing connection", "upstream mcp server", man.mcp.ID(), "event type", event)

	ctx, span := otel.Tracer(mcpotel.BrokerTracerName).Start(ctx, "mcp-broker.upstream-manage",
		trace.WithAttributes(
			attribute.String("component", "mcp-broker"),
			attribute.String("mcp.server", man.mcp.GetName()),
		),
	)
	defer span.End()

	// Check if the server is enabled before attempting any connection or tool registration.
	// If disabled, remove all tools and prompts, set status to not ready, and return.
	// The manager stays alive and will check again on the next ticker tick.
	if !man.mcp.IsEnabled() {
		man.logger.Debug("server is not enabled, removing tools and prompts", "upstream mcp server", man.mcp.ID())
		man.removeAllTools()
		man.removeAllPrompts()
		_ = man.mcp.Disconnect()
		man.setStatus(fmt.Errorf("server is disabled"), 0, 0, nil, nil)
		return
	}

	numberOfTools := len(man.tools)
	numberOfPrompts := len(man.prompts)
	// during connect the client will validate the protocol. So we don't have a separate validate requirement currently. If a client already exists it will be re-used.
	man.logger.DebugContext(ctx, "attempting to connect", "upstream mcp server", man.mcp.ID())
	if err := man.mcp.Connect(ctx, man.registerCallbacks()); err != nil {
		err = fmt.Errorf("failed to connect to upstream mcp %s removing tools : %w", man.mcp.ID(), err)
		man.recordBackendError(span, err)
		man.logger.ErrorContext(ctx, "connection failed", "upstream mcp server", man.mcp.ID(), "error", err)
		man.removeAllTools()
		man.removeAllPrompts()
		// we call disconnect here as we may have connected but failed to initialize
		_ = man.mcp.Disconnect()
		man.setStatus(err, numberOfTools, numberOfPrompts, nil, nil)
		return
	}
	// there may be an active client so we also ping
	if err := man.mcp.Ping(ctx); err != nil {
		// if we fail to ping we disconnect to ensure a fresh connection next time around
		err = fmt.Errorf("upstream mcp failed to ping server %s removing tools : %w", man.mcp.ID(), err)
		man.recordBackendError(span, err)
		man.logger.ErrorContext(ctx, "ping failed", "upstream mcp server", man.mcp.ID(), "error", err)
		man.removeAllTools()
		man.removeAllPrompts()
		_ = man.mcp.Disconnect()
		man.setStatus(err, numberOfTools, numberOfPrompts, nil, nil)
		return
	}

	if man.mcp.GetConfig().UserSpecificList {
		man.logger.Debug("userSpecificList server healthy, tools fetched per-user", "upstream mcp server", man.mcp.ID())
		man.status.ID = string(man.mcp.ID())
		man.status.LastValidated = time.Now()
		man.status.Name = man.MCPName()
		man.status.Ready = true
		man.status.Message = "userSpecificList server healthy, tools fetched per-user"
		man.resetBackoff()
		return
	}

	var toolErr error
	var invalidTools []InvalidToolInfo
	if !man.shouldFetchTools(event) {
		man.logger.DebugContext(ctx, "not fetching tools", "event", event, "upstream mcp server", man.mcp.ID(), "waiting for notification", notificationToolsListChanged)
	} else {
		man.logger.DebugContext(ctx, "fetching tools", "upstream mcp server", man.mcp.ID())
		current, fetched, err := man.getTools(ctx)
		if err != nil {
			toolErr = fmt.Errorf("upstream mcp failed to list tools server %s : %w", man.mcp.ID(), err)
			man.recordBackendError(span, toolErr)
			man.logger.ErrorContext(ctx, "failed to list tools", "upstream mcp server", man.mcp.ID(), "error", toolErr)
		} else {
			// validate fetched tools
			validTools, invalids := ValidateTools(fetched)
			if len(invalids) > 0 {
				man.logger.ErrorContext(ctx, "invalid tools detected", "upstream mcp server", man.mcp.ID(), "invalid", len(invalids), "valid", len(validTools))
				for _, info := range invalids {
					man.logger.ErrorContext(ctx, "invalid tool", "upstream mcp server", man.mcp.ID(), "tool", info.Name, "errors", info.Errors)
				}
				invalidTools = invalids
				if man.invalidToolPolicy == mcpv1alpha1.InvalidToolPolicyRejectServer {
					toolErr = fmt.Errorf("upstream mcp %s rejected: %d invalid tools found", man.mcp.ID(), len(invalids))
					man.recordBackendError(span, toolErr)
					man.removeAllTools()
				} else {
					fetched = validTools
				}
			}

			if toolErr == nil {
				// always compare the tools without prefix
				toAdd, toRemove := man.diffTools(current, fetched)
				if conflictErr := man.findToolConflicts(toAdd); conflictErr != nil {
					toolErr = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.mcp.ID(), conflictErr)
					man.recordBackendError(span, toolErr)
					man.logger.ErrorContext(ctx, "tool conflict detected", "upstream mcp server", man.mcp.ID(), "error", toolErr)
				} else {
					man.toolsLock.Lock()
					man.tools = fetched
					numberOfTools = len(fetched)
					man.toolsMap = make(map[string]*mcp.Tool, len(fetched))
					man.servedToolsMap = make(map[string]*mcp.Tool, len(fetched))
					for i := range fetched {
						man.toolsMap[fetched[i].Name] = &fetched[i]
						toolName := prefixedName(man.mcp.GetPrefix(), fetched[i].Name)
						man.servedToolsMap[toolName] = &fetched[i]
					}
					man.serverTools = slices.DeleteFunc(man.serverTools, func(tool GatewayTool) bool {
						return slices.Contains(toRemove, tool.Tool.Name)
					})
					man.serverTools = append(man.serverTools, toAdd...)
					man.toolsLock.Unlock()

					man.logger.DebugContext(ctx, "updating gateway tools", "upstream mcp server", man.mcp.ID(), "adding", len(toAdd), "removing", len(toRemove))
					if len(toRemove) > 0 {
						man.gatewayServer.DeleteTools(toRemove...)
					}
					if len(toAdd) > 0 {
						man.gatewayServer.AddTools(toAdd...)
					}
					man.logger.DebugContext(ctx, "internal tools", "upstream mcp server", man.mcp.ID(), "total", len(man.serverTools))
				}
			}
		}
	}

	var promptErr error
	var invalidPrompts []InvalidPromptInfo
	if man.promptsServer != nil && man.mcp.SupportsPrompts() && man.shouldFetchPrompts(event) {
		currentPrompts, fetchedPrompts, listErr := man.getPrompts(ctx)
		if listErr != nil {
			promptErr = fmt.Errorf("upstream mcp failed to list prompts server %s : %w", man.mcp.ID(), listErr)
			man.recordBackendError(span, promptErr)
			man.logger.ErrorContext(ctx, "failed to list prompts", "upstream mcp server", man.mcp.ID(), "error", promptErr)
		} else {
			validPrompts, invalids := ValidatePrompts(fetchedPrompts)
			if len(invalids) > 0 {
				man.logger.ErrorContext(ctx, "invalid prompts detected", "upstream mcp server", man.mcp.ID(), "invalid", len(invalids), "valid", len(validPrompts))
				for _, info := range invalids {
					man.logger.ErrorContext(ctx, "invalid prompt", "upstream mcp server", man.mcp.ID(), "prompt", info.Name, "errors", info.Errors)
				}
				invalidPrompts = invalids
				fetchedPrompts = validPrompts
			}

			toAddPrompts, toRemovePrompts := man.diffPrompts(currentPrompts, fetchedPrompts)
			if conflictErr := man.findPromptConflicts(toAddPrompts); conflictErr != nil {
				promptErr = fmt.Errorf("upstream mcp failed to add prompts to gateway %s : %w", man.mcp.ID(), conflictErr)
				man.recordBackendError(span, promptErr)
				man.logger.ErrorContext(ctx, "prompt conflict detected", "upstream mcp server", man.mcp.ID(), "error", promptErr)
			} else {
				man.toolsLock.Lock()
				man.prompts = fetchedPrompts
				numberOfPrompts = len(fetchedPrompts)
				man.promptsMap = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				man.servedPromptsMap = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				for i := range fetchedPrompts {
					man.promptsMap[fetchedPrompts[i].Name] = &fetchedPrompts[i]
					promptName := prefixedName(man.mcp.GetPrefix(), fetchedPrompts[i].Name)
					man.servedPromptsMap[promptName] = &fetchedPrompts[i]
				}
				man.serverPrompts = slices.DeleteFunc(man.serverPrompts, func(prompt GatewayPrompt) bool {
					return slices.Contains(toRemovePrompts, prompt.Prompt.Name)
				})
				man.serverPrompts = append(man.serverPrompts, toAddPrompts...)
				man.toolsLock.Unlock()

				man.logger.DebugContext(ctx, "updating gateway prompts", "upstream mcp server", man.mcp.ID(), "adding", len(toAddPrompts), "removing", len(toRemovePrompts))
				if len(toRemovePrompts) > 0 {
					man.promptsServer.DeletePrompts(toRemovePrompts...)
				}
				if len(toAddPrompts) > 0 {
					man.promptsServer.AddPrompts(toAddPrompts...)
				}
			}
		}
	}
	jointErr := errors.Join(toolErr, promptErr)
	man.setStatus(jointErr, numberOfTools, numberOfPrompts, invalidTools, invalidPrompts)
	if jointErr != nil {
		man.applyBackoff()
	} else {
		man.resetBackoff()
	}
}

func (man *MCPManager) shouldFetchTools(event eventType) bool {
	// fetch if no support for tools list change notifications
	if !man.mcp.SupportsToolsListChanged() {
		return true
	}
	if event == eventTypeToolNotification {
		return true
	}
	return event == eventTypeTimer && len(man.serverTools) == 0
}

func (man *MCPManager) shouldFetchPrompts(event eventType) bool {
	if !man.mcp.SupportsPromptsListChanged() {
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
	man.status.ID = string(man.mcp.ID())
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
	// always report the version we expect; fill in the negotiated version once it is known
	man.status.ProtocolValidation = ProtocolValidation{ExpectedVersion: expectedProtocolVersion}
	if info := man.mcp.ProtocolInfo(); info != nil {
		man.status.ProtocolValidation.IsValid = true
		man.status.ProtocolValidation.SupportedVersion = info.ProtocolVersion
	}
}

func (man *MCPManager) resetBackoff() {
	man.backoff = man.baseBackoff
	man.ticker.Reset(man.tickerInterval)
}

func (man *MCPManager) applyBackoff() {
	duration := man.backoff.Step()
	man.logger.Debug("applying backoff", "duration", duration, "upstream mcp server", man.mcp.ID())
	man.ticker.Reset(duration)
}

func (man *MCPManager) recordBackendError(span trace.Span, err error) {
	mcpotel.SpanError(span, err, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error_source", "backend"),
		attribute.String("mcp.server", man.mcp.GetName()),
	)
}

func (man *MCPManager) findToolConflicts(mcpTools []GatewayTool) error {
	gatewayServerTools := man.gatewayServer.ListTools()
	var conflictingToolNames []string
	for _, tool := range mcpTools {
		for existingToolName, existingToolInfo := range gatewayServerTools {
			existingTool := existingToolInfo.Tool
			if existingTool.Meta == nil {
				man.logger.Debug("skipping conflict check, tool meta is nil", "upstream mcp server", man.mcp.ID(), "tool", existingToolName)
				continue
			}
			existingToolID, ok := existingTool.Meta[gatewayServerID]
			if !ok {
				man.logger.Debug("skipping conflict check, tool id is missing", "upstream mcp server", man.mcp.ID(), "tool", existingToolName)
				continue
			}
			toolID, is := existingToolID.(string)
			if !is {
				man.logger.Debug("skipping conflict check, tool id is not a string", "upstream mcp server", man.mcp.ID(), "type", reflect.TypeOf(existingToolID))
				continue
			}

			if existingToolName == tool.Tool.Name && toolID != string(man.mcp.ID()) {
				man.logger.Debug("tool name conflict found", "upstream mcp server", man.mcp.ID(), "existing", existingToolName, "new", tool.Tool.Name, "conflicting server", toolID)
				conflictingToolNames = append(conflictingToolNames, tool.Tool.Name)
			}

		}
	}
	if len(conflictingToolNames) > 0 {
		return fmt.Errorf("conflicting tools discovered. conflicting tool names %v", conflictingToolNames)
	}

	return nil
}

// getTools return the existing, and new tools. Must only be called from the Start() event loop.
func (man *MCPManager) getTools(ctx context.Context) ([]mcp.Tool, []mcp.Tool, error) {
	tools := make([]mcp.Tool, len(man.tools))
	copy(tools, man.tools)
	res, err := man.mcp.ListTools(ctx)
	if err != nil {
		return tools, tools, fmt.Errorf("failed to get tools: %w", err)
	}
	fetched := make([]mcp.Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			man.logger.Debug("skipping nil tool from upstream", "server", man.mcp.ID())
			continue
		}
		fetched = append(fetched, *t)
	}
	return tools, fetched, nil
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
		man.servedToolsMap[prefixedName(man.mcp.GetPrefix(), tools[i].Name)] = &tools[i]
	}
}

// SetStatusForTesting sets the status directly for testing purposes.
// This bypasses the normal status update flow and should only be used in tests.
func (man *MCPManager) SetStatusForTesting(status ServerValidationStatus) {
	man.status = status
}

// NewActiveForTesting wraps a manager as an ActiveMCPServer without starting
// the event loop. Stop is a no-op. Only for use in tests that need a static
// manager with pre-seeded tools/status.
func NewActiveForTesting(man *MCPManager) ActiveMCPServer {
	return &activeMCP{manager: man, cancel: func() {}}
}

func (man *MCPManager) removeAllTools() {
	man.toolsLock.Lock()
	toolsToRemove := make([]string, 0, len(man.serverTools))
	man.logger.Debug("removing tools from gateway", "upstream mcp server", man.mcp.ID(), "total", len(man.serverTools))
	for _, tool := range man.serverTools {
		man.logger.Debug("removing tool from server ", "upstream mcp server", man.mcp.ID(), "tool", tool.Tool.Name)
		toolsToRemove = append(toolsToRemove, tool.Tool.Name)
	}
	man.serverTools = []GatewayTool{}
	man.tools = []mcp.Tool{}
	man.toolsMap = map[string]*mcp.Tool{}
	man.servedToolsMap = map[string]*mcp.Tool{}
	man.toolsLock.Unlock()
	man.gatewayServer.DeleteTools(toolsToRemove...)
	man.logger.Debug("removed all tools", "upstream mcp server", man.mcp.ID(), "count", len(toolsToRemove))
}

func (man *MCPManager) removeAllPrompts() {
	if man.promptsServer == nil {
		return
	}
	man.toolsLock.Lock()
	promptsToRemove := make([]string, 0, len(man.serverPrompts))
	for _, prompt := range man.serverPrompts {
		promptsToRemove = append(promptsToRemove, prompt.Prompt.Name)
	}
	man.serverPrompts = []GatewayPrompt{}
	man.prompts = []mcp.Prompt{}
	man.promptsMap = map[string]*mcp.Prompt{}
	man.servedPromptsMap = map[string]*mcp.Prompt{}
	man.toolsLock.Unlock()
	man.promptsServer.DeletePrompts(promptsToRemove...)
}

func (man *MCPManager) toolToServerTool(newTool mcp.Tool) GatewayTool {
	newTool.Name = prefixedName(man.mcp.GetPrefix(), newTool.Name)
	newTool.Meta = mcp.Meta{
		gatewayServerID: string(man.mcp.ID()),
	}
	return GatewayTool{
		Tool:    newTool,
		Handler: NoopToolHandler,
	}
}

func (man *MCPManager) diffTools(oldTools, newTools []mcp.Tool) ([]GatewayTool, []string) {
	oldToolMap := make(map[string]mcp.Tool)
	for _, oldTool := range oldTools {
		oldToolMap[oldTool.Name] = oldTool
	}

	newToolMap := make(map[string]mcp.Tool)
	for _, newTool := range newTools {
		newToolMap[newTool.Name] = newTool
	}

	addedTools := make([]GatewayTool, 0)
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
			removedTools = append(removedTools, prefixedName(man.mcp.GetPrefix(), oldTool.Name))
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

func (man *MCPManager) promptToServerPrompt(newPrompt mcp.Prompt) GatewayPrompt {
	newPrompt.Name = prefixedName(man.mcp.GetPrefix(), newPrompt.Name)
	newPrompt.Meta = mcp.Meta{
		gatewayServerID: string(man.mcp.ID()),
	}
	return GatewayPrompt{
		Prompt:  newPrompt,
		Handler: NoopPromptHandler,
	}
}

func (man *MCPManager) diffPrompts(oldPrompts, newPrompts []mcp.Prompt) ([]GatewayPrompt, []string) {
	oldPromptMap := make(map[string]mcp.Prompt)
	for _, p := range oldPrompts {
		oldPromptMap[p.Name] = p
	}

	newPromptMap := make(map[string]mcp.Prompt)
	for _, p := range newPrompts {
		newPromptMap[p.Name] = p
	}

	addedPrompts := make([]GatewayPrompt, 0)
	for _, newPrompt := range newPromptMap {
		if _, ok := oldPromptMap[newPrompt.Name]; !ok {
			addedPrompts = append(addedPrompts, man.promptToServerPrompt(newPrompt))
		}
	}

	removedPrompts := make([]string, 0)
	for _, oldPrompt := range oldPromptMap {
		if _, ok := newPromptMap[oldPrompt.Name]; !ok {
			removedPrompts = append(removedPrompts, prefixedName(man.mcp.GetPrefix(), oldPrompt.Name))
		}
	}

	return addedPrompts, removedPrompts
}

// getPrompts returns the existing and new prompts. Must only be called from the Start() event loop.
func (man *MCPManager) getPrompts(ctx context.Context) ([]mcp.Prompt, []mcp.Prompt, error) {
	prompts := make([]mcp.Prompt, len(man.prompts))
	copy(prompts, man.prompts)
	res, err := man.mcp.ListPrompts(ctx)
	if err != nil {
		return prompts, prompts, fmt.Errorf("failed to get prompts: %w", err)
	}
	fetched := make([]mcp.Prompt, 0, len(res.Prompts))
	for _, p := range res.Prompts {
		if p == nil {
			man.logger.Debug("skipping nil prompt from upstream", "server", man.mcp.ID())
			continue
		}
		fetched = append(fetched, *p)
	}
	return prompts, fetched, nil
}

func (man *MCPManager) findPromptConflicts(mcpPrompts []GatewayPrompt) error {
	if man.promptsServer == nil {
		return nil
	}
	gatewayServerPrompts := man.promptsServer.ListPrompts()
	var conflictingPromptNames []string
	for _, prompt := range mcpPrompts {
		for existingPromptName, existingPromptInfo := range gatewayServerPrompts {
			existingPrompt := existingPromptInfo.Prompt
			if existingPrompt.Meta == nil {
				man.logger.Debug("skipping conflict check, prompt meta is nil", "upstream mcp server", man.mcp.ID(), "prompt", existingPromptName)
				continue
			}
			existingPromptID, ok := existingPrompt.Meta[gatewayServerID]
			if !ok {
				man.logger.Debug("skipping conflict check, prompt id is missing", "upstream mcp server", man.mcp.ID(), "prompt", existingPromptName)
				continue
			}
			promptID, is := existingPromptID.(string)
			if !is {
				man.logger.Debug("skipping conflict check, prompt id is not a string", "upstream mcp server", man.mcp.ID(), "type", reflect.TypeOf(existingPromptID))
				continue
			}
			if existingPromptName == prompt.Prompt.Name && promptID != string(man.mcp.ID()) {
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
		man.servedPromptsMap[prefixedName(man.mcp.GetPrefix(), prompts[i].Name)] = &prompts[i]
	}
}
