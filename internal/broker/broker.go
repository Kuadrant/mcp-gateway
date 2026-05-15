// Package broker tracks upstream MCP servers and manages the relationship from clients to upstream
package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var _ config.Observer = &mcpBrokerImpl{}

// MCPBroker manages a set of MCP servers and their sessions
type MCPBroker interface {

	// Returns tool annotations for a given tool name
	ToolAnnotations(serverID config.UpstreamMCPID, tool string) (mcp.ToolAnnotation, bool)

	// Returns server info for a given tool name
	GetServerInfo(tool string) (*config.MCPServer, error)

	// Returns server info for a given prompt name
	GetServerInfoByPrompt(prompt string) (*config.MCPServer, error)

	// MCPServer gets an MCP server that federates the upstreams known to this MCPBroker
	MCPServer() *server.MCPServer

	//RegisteredServers returns the map of registered servers
	RegisteredMCPServers() map[config.UpstreamMCPID]upstream.ActiveMCPServer

	// GetVirtualSeverByHeader returns a virtual server definition based on a header where the header is the namespaced/name of the virtual server resource
	GetVirtualSeverByHeader(namespaceName string) (config.VirtualServer, error)

	// ValidateAllServers performs comprehensive validation of all registered servers and returns status
	ValidateAllServers() StatusResponse

	// IsReady reports whether the broker can serve traffic.
	// Returns true when no upstream servers are configured (empty tool list is valid),
	// or when at least one configured upstream is healthy.
	// Returns false only when servers are configured but none have connected yet.
	IsReady() bool

	// HandleStatusRequest handles HTTP status endpoint requests
	HandleStatusRequest(w http.ResponseWriter, r *http.Request)

	// Shutdown closes any resources associated with this Broker
	Shutdown(ctx context.Context) error

	config.Observer
}

// mcpBrokerImpl implements MCPBroker
type mcpBrokerImpl struct {
	virtualServers map[string]*config.VirtualServer
	vsLock         sync.RWMutex //vsLock is for managing access to the virtual servers

	// mcpServers tracks the known servers
	mcpServers map[config.UpstreamMCPID]upstream.ActiveMCPServer
	// protects mcpServers
	mcpLock sync.RWMutex

	// listeningMCPServer returns an actual listening MCP server that federates registered MCP servers
	listeningMCPServer *server.MCPServer

	logger *slog.Logger

	// enforceCapabilityFilter if set will ensure only filtered capabilities are returned based on the x-mcp-authorized trusted header
	enforceCapabilityFilter bool

	// trustedHeadersPublicKey this is the key to verify that a trusted header came from the trusted source (the owner of the private key)
	trustedHeadersPublicKey string

	// managerTickerInterval is the interval for MCP manager backend health checks
	managerTickerInterval time.Duration

	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy mcpv1alpha1.InvalidToolPolicy
}

// this ensures that mcpBrokerImpl implements the MCPBroker interface
var _ MCPBroker = &mcpBrokerImpl{}

// Option configures a broker instance
type Option func(mb *mcpBrokerImpl)

// WithEnforceCapabilityFilter defines enforceCapabilityFilter setting and is intended for use with the NewBroker function
func WithEnforceCapabilityFilter(enforce bool) Option {
	return func(mb *mcpBrokerImpl) {
		mb.enforceCapabilityFilter = enforce
	}
}

// WithTrustedHeadersPublicKey defines the public key used to verify signed headers and is intended for use with the NewBroker function
func WithTrustedHeadersPublicKey(key string) Option {
	return func(mb *mcpBrokerImpl) {
		mb.trustedHeadersPublicKey = key
	}
}

// WithManagerTickerInterval sets the interval for MCP manager backend health checks
func WithManagerTickerInterval(interval time.Duration) Option {
	return func(mb *mcpBrokerImpl) {
		mb.managerTickerInterval = interval
	}
}

// WithInvalidToolPolicy sets the policy for handling upstream tools with invalid schemas
func WithInvalidToolPolicy(policy mcpv1alpha1.InvalidToolPolicy) Option {
	return func(mb *mcpBrokerImpl) {
		mb.invalidToolPolicy = policy
	}
}

// NewBroker creates a new MCPBroker accepts optional config functions such as WithEnforceCapabilityFilter
func NewBroker(logger *slog.Logger, opts ...Option) MCPBroker {
	mcpBkr := &mcpBrokerImpl{
		mcpServers:            map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
		logger:                logger,
		virtualServers:        map[string]*config.VirtualServer{},
		managerTickerInterval: time.Second * 60,
	}

	for _, option := range opts {
		option(mcpBkr)
	}

	hooks := &server.Hooks{}

	// Enhanced session registration to log gateway session assignment
	hooks.AddOnRegisterSession(func(_ context.Context, session server.ClientSession) {
		// Note that AddOnRegisterSession is for GET, not POST, for a session.
		// https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#listening-for-messages-from-the-server
		mcpBkr.logger.Debug("gateway client session connected", "gatewaySessionID", session.SessionID())
	})

	hooks.AddOnUnregisterSession(func(_ context.Context, session server.ClientSession) {
		mcpBkr.logger.Debug("gateway client session unregistered", "gatewaySessionID", session.SessionID())
	})

	hooks.AddBeforeAny(func(_ context.Context, _ any, method mcp.MCPMethod, _ any) {
		mcpBkr.logger.Debug("processing request", "method", method)
	})

	hooks.AddOnError(func(_ context.Context, _ any, method mcp.MCPMethod, _ any, err error) {
		mcpBkr.logger.Error("mcp server error", "method", method, "error", err)
	})

	hooks.AddAfterListTools(func(ctx context.Context, id any, message *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
		mcpBkr.FilterTools(ctx, id, message, result)
	})

	hooks.AddAfterListPrompts(func(ctx context.Context, id any, message *mcp.ListPromptsRequest, result *mcp.ListPromptsResult) {
		mcpBkr.FilterPrompts(ctx, id, message, result)
	})

	mcpBkr.listeningMCPServer = server.NewMCPServer(
		"Kuadrant MCP Gateway",
		"0.0.1",
		server.WithHooks(hooks),
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
	)
	return mcpBkr
}

func (m *mcpBrokerImpl) OnConfigChange(ctx context.Context, conf *config.MCPServersConfig) {
	// Take a consistent snapshot before acquiring mcpLock; LoadConfig may be
	// concurrently replacing conf.Servers/VirtualServers under its own write lock.
	servers := conf.ListServers()
	virtualServers := conf.ListVirtualServers()

	m.logger.Debug("Broker OnConfigChange start", "Total managers for upstream mcp servers", len(m.mcpServers), "total servers", len(servers))
	// unregister decommissioned servers
	m.mcpLock.Lock()
	defer m.mcpLock.Unlock()

	for serverID := range m.mcpServers {
		if !slices.ContainsFunc(servers, func(s *config.MCPServer) bool {
			return serverID == s.ID()
		}) {
			m.logger.Info("un-register upstream server", "server id", serverID)
			if man, ok := m.mcpServers[serverID]; ok {
				m.logger.Info("stopping manager for unregistered server", "server id", serverID)
				man.Stop()
				delete(m.mcpServers, serverID)
			}
		}
	}
	// ensure new servers registered

	for _, mcpServer := range servers {
		man, ok := m.mcpServers[mcpServer.ID()]
		if ok {
			m.logger.Info("Server is registered", "mcpID", mcpServer.ID())
			// already have a manger
			if mcpServer.ConfigChanged(man.Config()) {
				// todo prob could look at just updating the config
				m.logger.Info("Server Config Changed removing manager", "mcpID", mcpServer.ID())
				man.Stop()
				delete(m.mcpServers, mcpServer.ID())
			}
		}
		// check if we need to setup a new manager
		if _, ok := m.mcpServers[mcpServer.ID()]; !ok {
			m.logger.Info("starting new manager", "server id", mcpServer.ID())
			manager, err := upstream.NewUpstreamMCPManager(upstream.NewUpstreamMCP(mcpServer), m.listeningMCPServer, m.listeningMCPServer, m.logger.With("sub-component", "mcp-manager"), m.managerTickerInterval, m.invalidToolPolicy)
			if err != nil {
				m.logger.Error("failed to create manager", "server id", mcpServer.ID(), "error", err)
				continue
			}
			m.logger.Info("Starting manager for", "mcpID", mcpServer.ID())
			m.mcpServers[mcpServer.ID()] = manager.Start(ctx)
		}
	}
	// register virtual servers
	m.vsLock.Lock()
	for _, vs := range virtualServers {
		m.virtualServers[vs.Name] = vs
	}
	m.vsLock.Unlock()
	m.logger.Debug("Broker OnConfigChange done", "Total managers for upstream mcp servers", len(m.mcpServers), "total servers", len(servers))
}

func (m *mcpBrokerImpl) RegisteredMCPServers() map[config.UpstreamMCPID]upstream.ActiveMCPServer {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	return m.mcpServers
}

func (m *mcpBrokerImpl) GetVirtualSeverByHeader(namespaceName string) (config.VirtualServer, error) {
	m.vsLock.RLock()
	defer m.vsLock.RUnlock()
	for _, vs := range m.virtualServers {
		if vs.Name == namespaceName {
			return *vs, nil
		}
	}
	return config.VirtualServer{}, fmt.Errorf("virtual server %s not found", namespaceName)
}

func (m *mcpBrokerImpl) ToolAnnotations(serverID config.UpstreamMCPID, tool string) (mcp.ToolAnnotation, bool) {
	// Avoid race with OnConfigChange()
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	upstream, ok := m.mcpServers[serverID]
	if !ok {
		return mcp.ToolAnnotation{}, false
	}
	t := upstream.GetServedManagedTool(tool)
	if t != nil {
		return t.Annotations, true
	}
	return mcp.ToolAnnotation{}, false
}

// GetServerInfo implements MCPBroker by providing a lookup of the server that implements a tool.
func (m *mcpBrokerImpl) GetServerInfo(tool string) (*config.MCPServer, error) {
	// Avoid race with OnConfigChange()
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, upstream := range m.mcpServers {
		t := upstream.GetServedManagedTool(tool)
		if t != nil {
			m.logger.Debug("found matching server",
				"toolName", tool,
				"serverPrefix", upstream.Config().Prefix,
				"serverName", upstream.MCPName())
			retval := upstream.Config()
			return &retval, nil
		}
	}

	return nil, fmt.Errorf("tool name %q doesn't match any configured server", tool)
}

// GetServerInfoByPrompt implements MCPBroker by providing a lookup of the server that implements a prompt.
func (m *mcpBrokerImpl) GetServerInfoByPrompt(prompt string) (*config.MCPServer, error) {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, upstream := range m.mcpServers {
		p := upstream.GetServedManagedPrompt(prompt)
		if p != nil {
			cfg := upstream.Config()
			m.logger.Debug("found matching server for prompt",
				"promptName", prompt,
				"serverPrefix", cfg.Prefix,
				"serverName", upstream.MCPName())
			retval := cfg
			return &retval, nil
		}
	}

	return nil, fmt.Errorf("prompt name %q doesn't match any configured server", prompt)
}

func (m *mcpBrokerImpl) Shutdown(_ context.Context) error {
	// Avoid race with OnConfigChange()
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	// Close the long-running notification channel
	for _, mcpServer := range m.mcpServers {
		if mcpServer != nil {
			mcpServer.Stop()
		}
	}
	return nil
}

// MCPServer is a listening MCP server that federates the endpoints
func (m *mcpBrokerImpl) MCPServer() *server.MCPServer {
	return m.listeningMCPServer
}

// HandleStatusRequest handles HTTP status endpoint requests
func (m *mcpBrokerImpl) HandleStatusRequest(w http.ResponseWriter, r *http.Request) {
	handler := NewStatusHandler(m, *m.logger)
	handler.ServeHTTP(w, r)
}

// ValidateAllServers performs comprehensive validation of all registered servers and returns status
func (m *mcpBrokerImpl) ValidateAllServers() StatusResponse {
	// The race is with len(m.mcpServers), which is not thread-safe in Go
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	response := StatusResponse{
		Servers:          make([]upstream.ServerValidationStatus, 0),
		OverallValid:     true,
		TotalServers:     len(m.mcpServers),
		HealthyServers:   0,
		UnHealthyServers: 0,
		ToolConflicts:    0,
		Timestamp:        time.Now(),
	}

	m.logger.Debug("ValidateAllServers: checking servers", "# servers", len(m.mcpServers))

	// access m.mcpServers directly; RLock is already held.
	// Calling RegisteredMCPServers() here would attempt a second RLock on the same
	// goroutine. Go's sync.RWMutex blocks new readers when a writer is waiting, so a
	// concurrent OnConfigChange() Lock() causes both goroutines to deadlock.
	for _, upstream := range m.mcpServers {
		status := upstream.GetStatus()
		response.Servers = append(response.Servers, status)

		if !status.Ready {
			response.UnHealthyServers++
			response.OverallValid = false
		} else {
			response.HealthyServers++
		}
	}

	m.logger.Info("Server validation completed",
		"totalServers", response.TotalServers,
		"healthyServers", response.HealthyServers,
		"unhealthyServers", response.UnHealthyServers,
		"overallValid", response.OverallValid)

	return response
}

// IsReady reports whether the broker can serve traffic.
// Accesses m.mcpServers directly (lock already not held here) rather than
// calling RegisteredMCPServers to avoid nested RLock under a pending writer.
func (m *mcpBrokerImpl) IsReady() bool {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	if len(m.mcpServers) == 0 {
		return true
	}
	for _, mgr := range m.mcpServers {
		if mgr.GetStatus().Ready {
			return true
		}
	}
	return false
}
