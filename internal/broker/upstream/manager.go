/*
Package upstream is a package for managing upstream MCP servers
*/
package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

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
	// tools registry manages the lifecycle of tools from this upstream server
	tools *entityRegistry[mcp.Tool, GatewayTool]

	promptsServer PromptsAdderDeleter
	// prompts registry manages the lifecycle of prompts from this upstream server
	prompts *entityRegistry[mcp.Prompt, GatewayPrompt]

	// toolsLock protects tools, serverTools, prompts, serverPrompts
	toolsLock sync.RWMutex

	logger *slog.Logger

	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy InvalidToolPolicy

	// toolEvents and promptEvents funnel notifications into the Start() loop.
	// Separate channels with buffer of 1 each ensure a tool notification cannot
	// block a prompt notification (or vice versa) while still coalescing rapid
	// same-type notifications.
	toolEvents   chan struct{}
	promptEvents chan struct{}
	done         chan struct{} // closed when the event loop exits
	status       ServerValidationStatus

	// consecutiveFailures counts connect/ping failures since the last
	// healthy pass. only touched from the event loop goroutine.
	consecutiveFailures int
}

// DefaultTickerInterval is the default interval for backend health checks
const DefaultTickerInterval = time.Minute * 1

// maxConsecutiveFailures is the number of consecutive connect/ping failures
// tolerated before cached tools and prompts are dropped from the gateway.
// tool calls route directly to the backend via envoy, so serving a cached
// list through a transient upstream blip is no worse than the staleness
// already allowed between health ticks, and avoids client-visible churn.
const maxConsecutiveFailures = 3

// NewUpstreamMCPManager creates a new MCPManager for managing a single upstream MCP server.
// The addTools and removeTools callbacks are used to update the gateway's tool registry.
// The tickerInterval controls how often the manager checks backend health (use 0 for default).
func NewUpstreamMCPManager(upstream MCP, gatewayServer ToolsAdderDeleter, promptsServer PromptsAdderDeleter, logger *slog.Logger, tickerInterval time.Duration, policy InvalidToolPolicy) (*MCPManager, error) {
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
		tools: newToolRegistry(
			upstream.GetPrefix(),
			string(upstream.ID()),
			gatewayServer,
			func(ctx context.Context) ([]mcp.Tool, error) {
				res, err := upstream.ListTools(ctx)
				if err != nil {
					return nil, err
				}
				fetched := make([]mcp.Tool, 0, len(res.Tools))
				for _, t := range res.Tools {
					if t == nil {
						continue
					}
					fetched = append(fetched, *t)
				}
				return fetched, nil
			},
			logger,
		),
		prompts: newPromptRegistry(
			upstream.GetPrefix(),
			string(upstream.ID()),
			promptsServer,
			func(ctx context.Context) ([]mcp.Prompt, error) {
				res, err := upstream.ListPrompts(ctx)
				if err != nil {
					return nil, err
				}
				fetched := make([]mcp.Prompt, 0, len(res.Prompts))
				for _, p := range res.Prompts {
					if p == nil {
						continue
					}
					fetched = append(fetched, *p)
				}
				return fetched, nil
			},
			logger,
		),
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
				man.prompts.removeAll(&man.toolsLock)
				man.tools.removeAll(&man.toolsLock)

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
		man.tools.removeAll(&man.toolsLock)
		man.prompts.removeAll(&man.toolsLock)
		_ = man.mcp.Disconnect()
		man.consecutiveFailures = 0
		man.setStatus(fmt.Errorf("server is disabled"), 0, 0, nil, nil)
		return
	}

	numberOfTools := len(man.tools.items)
	numberOfPrompts := len(man.prompts.items)
	// during connect the client will validate the protocol. So we don't have a separate validate requirement currently. If a client already exists it will be re-used.
	man.logger.DebugContext(ctx, "attempting to connect", "upstream mcp server", man.mcp.ID())
	if err := man.mcp.Connect(ctx, man.registerCallbacks()); err != nil {
		err = fmt.Errorf("failed to connect to upstream mcp %s removing tools : %w", man.mcp.ID(), err)
		man.handleConnectionFailure(ctx, span, err, numberOfTools, numberOfPrompts)
		return
	}
	// there may be an active client so we also ping
	if err := man.mcp.Ping(ctx); err != nil {
		// if we fail to ping we disconnect to ensure a fresh connection next time around
		err = fmt.Errorf("upstream mcp failed to ping server %s removing tools : %w", man.mcp.ID(), err)
		man.handleConnectionFailure(ctx, span, err, numberOfTools, numberOfPrompts)
		return
	}
	man.consecutiveFailures = 0

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
		current, fetched, err := man.tools.get(ctx)
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
				if man.invalidToolPolicy == InvalidToolPolicyRejectServer {
					toolErr = fmt.Errorf("upstream mcp %s rejected: %d invalid tools found", man.mcp.ID(), len(invalids))
					man.recordBackendError(span, toolErr)
					man.tools.removeAll(&man.toolsLock)
				} else {
					fetched = validTools
				}
			}

			if toolErr == nil {
				// always compare the tools without prefix
				toAdd, toRemove := man.tools.diff(current, fetched)
				if conflictErr := man.tools.findConflicts(toAdd); conflictErr != nil {
					toolErr = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.mcp.ID(), conflictErr)
					man.recordBackendError(span, toolErr)
					man.logger.ErrorContext(ctx, "tool conflict detected", "upstream mcp server", man.mcp.ID(), "error", toolErr)
				} else {
					man.toolsLock.Lock()
					man.tools.items = fetched
					numberOfTools = len(fetched)
					man.tools.byName = make(map[string]*mcp.Tool, len(fetched))
					man.tools.byServedName = make(map[string]*mcp.Tool, len(fetched))
					for i := range fetched {
						man.tools.byName[fetched[i].Name] = &fetched[i]
						toolName := prefixedName(man.mcp.GetPrefix(), fetched[i].Name)
						man.tools.byServedName[toolName] = &fetched[i]
					}
					man.tools.serverItems = slices.DeleteFunc(man.tools.serverItems, func(tool GatewayTool) bool {
						return slices.Contains(toRemove, tool.Tool.Name)
					})
					man.tools.serverItems = append(man.tools.serverItems, toAdd...)
					man.toolsLock.Unlock()

					man.logger.DebugContext(ctx, "updating gateway tools", "upstream mcp server", man.mcp.ID(), "adding", len(toAdd), "removing", len(toRemove))
					if len(toRemove) > 0 {
						man.gatewayServer.DeleteTools(toRemove...)
					}
					if len(toAdd) > 0 {
						man.gatewayServer.AddTools(toAdd...)
					}
					man.logger.DebugContext(ctx, "internal tools", "upstream mcp server", man.mcp.ID(), "total", len(man.tools.serverItems))
				}
			}
		}
	}

	var promptErr error
	var invalidPrompts []InvalidPromptInfo
	if man.promptsServer != nil && man.mcp.SupportsPrompts() && man.shouldFetchPrompts(event) {
		currentPrompts, fetchedPrompts, listErr := man.prompts.get(ctx)
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

			toAddPrompts, toRemovePrompts := man.prompts.diff(currentPrompts, fetchedPrompts)
			if conflictErr := man.prompts.findConflicts(toAddPrompts); conflictErr != nil {
				promptErr = fmt.Errorf("upstream mcp failed to add prompts to gateway %s : %w", man.mcp.ID(), conflictErr)
				man.recordBackendError(span, promptErr)
				man.logger.ErrorContext(ctx, "prompt conflict detected", "upstream mcp server", man.mcp.ID(), "error", promptErr)
			} else {
				man.toolsLock.Lock()
				man.prompts.items = fetchedPrompts
				numberOfPrompts = len(fetchedPrompts)
				man.prompts.byName = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				man.prompts.byServedName = make(map[string]*mcp.Prompt, len(fetchedPrompts))
				for i := range fetchedPrompts {
					man.prompts.byName[fetchedPrompts[i].Name] = &fetchedPrompts[i]
					promptName := prefixedName(man.mcp.GetPrefix(), fetchedPrompts[i].Name)
					man.prompts.byServedName[promptName] = &fetchedPrompts[i]
				}
				man.prompts.serverItems = slices.DeleteFunc(man.prompts.serverItems, func(prompt GatewayPrompt) bool {
					return slices.Contains(toRemovePrompts, prompt.Prompt.Name)
				})
				man.prompts.serverItems = append(man.prompts.serverItems, toAddPrompts...)
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

// shouldFetchTools reports whether this event warrants a tools/list.
// list-changed pushes from the notification watcher trigger an immediate
// fetch; the timer re-list backstops them regardless of the upstream's
// listChanged capability, since the watcher stops permanently for
// upstreams without the stream and events sent while it reconnects are
// lost unless the upstream replays them.
func (man *MCPManager) shouldFetchTools(event eventType) bool {
	// fetch if no support for tools list change notifications
	if !man.mcp.SupportsToolsListChanged() {
		return true
	}
	if event == eventTypeToolNotification {
		return true
	}
	return event == eventTypeTimer
}

func (man *MCPManager) shouldFetchPrompts(event eventType) bool {
	if !man.mcp.SupportsPromptsListChanged() {
		return true
	}
	if event == eventTypePromptNotification {
		return true
	}
	return event == eventTypeTimer
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

// handleConnectionFailure records a connect/ping failure and backs off the
// retry. cached tools and prompts stay served through transient failures
// and are only dropped once the failure persists across
// maxConsecutiveFailures attempts; the session is torn down either way so
// the next attempt starts fresh.
func (man *MCPManager) handleConnectionFailure(ctx context.Context, span trace.Span, err error, numberOfTools, numberOfPrompts int) {
	man.consecutiveFailures++
	man.recordBackendError(span, err)
	man.logger.ErrorContext(ctx, "upstream connection failure", "upstream mcp server", man.mcp.ID(), "error", err, "consecutive failures", man.consecutiveFailures)
	if man.consecutiveFailures >= maxConsecutiveFailures {
		man.logger.ErrorContext(ctx, "failure threshold reached, removing tools and prompts", "upstream mcp server", man.mcp.ID(), "threshold", maxConsecutiveFailures)
		man.tools.removeAll(&man.toolsLock)
		man.prompts.removeAll(&man.toolsLock)
	}
	_ = man.mcp.Disconnect()
	man.setStatus(err, numberOfTools, numberOfPrompts, nil, nil)
	man.applyBackoff()
}

func (man *MCPManager) resetBackoff() {
	man.backoff = man.baseBackoff
	man.resetTicker(man.tickerInterval)
}

func (man *MCPManager) applyBackoff() {
	duration := man.backoff.Step()
	man.logger.Debug("applying backoff", "duration", duration, "upstream mcp server", man.mcp.ID())
	man.resetTicker(duration)
}

// resetTicker re-arms the ticker and drops a tick that fired while manage
// was in flight, so the new interval applies instead of an immediate
// re-run. only called from the event loop goroutine.
func (man *MCPManager) resetTicker(d time.Duration) {
	man.ticker.Reset(d)
	select {
	case <-man.ticker.C:
	default:
	}
}

func (man *MCPManager) recordBackendError(span trace.Span, err error) {
	mcpotel.SpanError(span, err, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error_source", "backend"),
		attribute.String("mcp.server", man.mcp.GetName()),
	)
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

func prefixedName(prefix, tool string) string {
	if prefix == "" {
		return tool
	}
	return fmt.Sprintf("%s%s", prefix, tool)
}

// GetManagedTools returns a copy of all tools discovered from the upstream server.
// The returned tools have their original names without the gateway prefix.
func (man *MCPManager) GetManagedTools() []mcp.Tool {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.tools.getManagedCopy()
}

// GetServedManagedTool will return the tool if present that is actually being served by the gateway.
// It expects a prefixed tool if a prefix is present.
// returns the map pointer directly to avoid per-lookup alloc -- callers must not modify.
func (man *MCPManager) GetServedManagedTool(toolName string) *mcp.Tool {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.tools.getServed(toolName)
}

// SetToolsForTesting sets the tools directly for testing purposes.
// This bypasses the normal tool discovery flow and should only be used in tests.
// TODO look to remove the need for this
func (man *MCPManager) SetToolsForTesting(tools []mcp.Tool) {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	man.tools.setForTesting(tools, man.mcp.GetPrefix())
}

// GetManagedPrompts returns a copy of all prompts discovered from the upstream server.
func (man *MCPManager) GetManagedPrompts() []mcp.Prompt {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.prompts.getManagedCopy()
}

// GetServedManagedPrompt returns the prompt if present that is being served by the gateway.
func (man *MCPManager) GetServedManagedPrompt(promptName string) *mcp.Prompt {
	man.toolsLock.RLock()
	defer man.toolsLock.RUnlock()
	return man.prompts.getServed(promptName)
}

// SetPromptsForTesting sets prompts directly for testing purposes.
func (man *MCPManager) SetPromptsForTesting(prompts []mcp.Prompt) {
	man.toolsLock.Lock()
	defer man.toolsLock.Unlock()
	man.prompts.setForTesting(prompts, man.mcp.GetPrefix())
}
