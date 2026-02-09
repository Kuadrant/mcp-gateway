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

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var _ config.Observer = &mcpBrokerImpl{}

// MCPBroker manages a set of MCP servers and their sessions
type MCPBroker interface {

	// Returns tool annotations for a given tool name
	ToolAnnotations(serverID config.UpstreamMCPID, tool string) (mcp.ToolAnnotation, bool)

	// Returns server info for a given tool name
	GetServerInfo(tool string) (*config.MCPServer, error)

	// MCPServer gets an MCP server that federates the upstreams known to this MCPBroker
	MCPServer() *server.MCPServer

	//RegisteredServers returns the map of registered servers
	RegisteredMCPServers() map[config.UpstreamMCPID]*upstream.MCPManager

	// GetVirtualSeverByHeader returns a virtual server definition based on a header where the header is the namespaced/name of the virtual server resource
	GetVirtualSeverByHeader(namespaceName string) (config.VirtualServer, error)

	// ValidateAllServers performs comprehensive validation of all registered servers and returns status
	ValidateAllServers() StatusResponse

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
	mcpServers map[config.UpstreamMCPID]*upstream.MCPManager
	// protects mcpServers
	mcpLock sync.RWMutex

	// listeningMCPServer returns an actual listening MCP server that federates registered MCP servers
	listeningMCPServer *server.MCPServer

	logger *slog.Logger

	// enforceToolFilter if set will ensure only a filtered list of tools is returned this list is based on the x-authorized-tools trusted header
	enforceToolFilter bool

	// trustedHeadersPublicKey this is the key to verify that a trusted header came from the trusted source (the owner of the private key)
	trustedHeadersPublicKey string

	// managerTickerInterval is the interval for MCP manager backend health checks
	managerTickerInterval time.Duration
}

// this ensures that mcpBrokerImpl implements the MCPBroker interface
var _ MCPBroker = &mcpBrokerImpl{}

// WithEnforceToolFilter defines enforceToolFilter setting and is intended for use with the NewBroker function
func WithEnforceToolFilter(enforce bool) func(mb *mcpBrokerImpl) {
	return func(mb *mcpBrokerImpl) {
		mb.enforceToolFilter = enforce
	}
}

// WithTrustedHeadersPublicKey defines the public key used to verify signed headers and is intended for use with the NewBroker function
func WithTrustedHeadersPublicKey(key string) func(mb *mcpBrokerImpl) {
	return func(mb *mcpBrokerImpl) {
		mb.trustedHeadersPublicKey = key
	}
}

// WithManagerTickerInterval sets the interval for MCP manager backend health checks
func WithManagerTickerInterval(interval time.Duration) func(mb *mcpBrokerImpl) {
	return func(mb *mcpBrokerImpl) {
		mb.managerTickerInterval = interval
	}
}

// NewBroker creates a new MCPBroker accepts optional config functions such as WithEnforceToolFilter
func NewBroker(logger *slog.Logger, opts ...func(*mcpBrokerImpl)) MCPBroker {
	mcpBkr := &mcpBrokerImpl{
		mcpServers:            map[config.UpstreamMCPID]*upstream.MCPManager{},
		logger:                logger,
		virtualServers:        map[string]*config.VirtualServer{},
		managerTickerInterval: time.Second * 60,
	}

	for _, option := range opts {
		option(mcpBkr)
	}

	hooks := &server.Hooks{}

	// Enhanced session registration to log gateway session assignment
	hooks.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		// Note that AddOnRegisterSession is for GET, not POST, for a session.
		// https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#listening-for-messages-from-the-server
		_, span := startSpan(ctx, "mcp-broker.session-register",
			attribute.String("mcp.session_id", session.SessionID()),
		)
		defer span.End()
		slog.Info("Broker: Gateway client session connected with session", "gatewaySessionID", session.SessionID())
	})

	hooks.AddOnUnregisterSession(func(ctx context.Context, session server.ClientSession) {
		_, span := startSpan(ctx, "mcp-broker.session-unregister",
			attribute.String("mcp.session_id", session.SessionID()),
		)
		defer span.End()
		slog.Info("Broker: Gateway client session unregister ", "gatewaySessionID", session.SessionID())
	})

	hooks.AddBeforeAny(func(ctx context.Context, id any, method mcp.MCPMethod, _ any) {
		_, span := startSpan(ctx, "mcp-broker.handle-request",
			attribute.String("mcp.method", string(method)),
		)
		// store span in context for AfterAny to end it
		if sessionID, ok := id.(string); ok {
			span.SetAttributes(attribute.String("mcp.session_id", sessionID))
		}
		slog.Info("Processing request", "method", method)
		// span will be ended by AfterAny or OnError
		span.End()
	})

	hooks.AddOnError(func(ctx context.Context, id any, method mcp.MCPMethod, _ any, err error) {
		span := trace.SpanFromContext(ctx)
		if span.IsRecording() {
			recordError(span, err, "broker")
		}
		slog.Info("MCP server error", "method", method, "error", err)
	})

	hooks.AddAfterListTools(func(ctx context.Context, id any, message *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
		span := trace.SpanFromContext(ctx)
		if span.IsRecording() && result != nil {
			span.SetAttributes(attribute.Int("mcp.tools_count", len(result.Tools)))
		}
		mcpBkr.FilterTools(ctx, id, message, result)
	})

	mcpBkr.listeningMCPServer = server.NewMCPServer(
		"Kagenti MCP Broker",
		"0.0.1",
		server.WithHooks(hooks),
		server.WithToolCapabilities(true),
	)
	return mcpBkr
}

func (m *mcpBrokerImpl) OnConfigChange(ctx context.Context, conf *config.MCPServersConfig) {
	m.logger.Debug("Broker OnConfigChange start", "Total managers for upstream mcp servers", len(m.mcpServers), "total servers", len(conf.Servers))
	// unregister decommissioned servers
	m.mcpLock.Lock()
	defer m.mcpLock.Unlock()

	for serverID := range m.mcpServers {
		if !slices.ContainsFunc(conf.Servers, func(s *config.MCPServer) bool {
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

	for _, mcpServer := range conf.Servers {
		man, ok := m.mcpServers[mcpServer.ID()]
		if ok {
			m.logger.Info("Server is registered", "mcpID", mcpServer.ID())
			// already have a manger
			if mcpServer.ConfigChanged(man.MCP.GetConfig()) {
				// todo prob could look at just updating the config
				m.logger.Info("Server Config Changed removing manager", "mcpID", mcpServer.ID())
				man.Stop()
				delete(m.mcpServers, mcpServer.ID())
			}
		}
		// check if we need to setup a new manager
		if _, ok := m.mcpServers[mcpServer.ID()]; !ok {
			m.logger.Info("starting new manager", "server id", mcpServer.ID())
			manager := upstream.NewUpstreamMCPManager(upstream.NewUpstreamMCP(mcpServer), m.listeningMCPServer, m.logger.With("sub-component", "mcp-manager"), m.managerTickerInterval)
			m.mcpServers[mcpServer.ID()] = manager
			go func() {
				m.logger.Info("Starting manager for", "mcpID", mcpServer.ID())
				manager.Start(ctx)
			}()
		}
	}
	// register virtual servers
	m.vsLock.Lock()
	for _, vs := range conf.VirtualServers {
		m.virtualServers[vs.Name] = vs
	}
	m.vsLock.Unlock()
	m.logger.Debug("Broker OnConfigChange done", "Total managers for upstream mcp servers", len(m.mcpServers), "total servers", len(conf.Servers))
}

func (m *mcpBrokerImpl) RegisteredMCPServers() map[config.UpstreamMCPID]*upstream.MCPManager {
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
			slog.Info("[EXT-PROC] Found matching server",
				"toolName", tool,
				"serverPrefix", upstream.MCP.GetPrefix(),
				"serverName", upstream.MCP.GetName())
			retval := upstream.MCP.GetConfig()
			return &retval, nil
		}
	}

	return nil, fmt.Errorf("tool name %q doesn't match any configured server", tool)
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

	for _, upstream := range m.RegisteredMCPServers() {
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
