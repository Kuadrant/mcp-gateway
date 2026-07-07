// Package broker tracks upstream MCP servers and manages the relationship from clients to upstream
package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var _ config.Observer = &mcpBrokerImpl{}

// unpaginatedPageSize disables list pagination in practice. not math.MaxInt:
// the SDK's paginateList computes pageSize+1, which must not overflow.
const unpaginatedPageSize = 1 << 30

// MCPBroker manages a set of MCP servers and their sessions
type MCPBroker interface {

	// Returns tool annotations for a given tool name. hints are nil when the
	// upstream left them unspecified (mark3labs *bool semantics).
	ToolAnnotations(serverID config.UpstreamMCPID, tool string) (upstream.ToolHints, bool)

	// Returns server info for a given tool name
	GetServerInfo(tool string) (*config.MCPServer, error)

	// Returns server info for a given prompt name
	GetServerInfoByPrompt(prompt string) (*config.MCPServer, error)

	// MCPServer gets an MCP server that federates the upstreams known to this MCPBroker
	MCPServer() *mcp.Server

	//RegisteredServers returns the map of registered servers
	RegisteredMCPServers() map[config.UpstreamMCPID]upstream.ActiveMCPServer

	// GetVirtualServerByHeader returns a virtual server definition based on a header where the header is the namespaced/name of the virtual server resource
	GetVirtualServerByHeader(namespaceName string) (config.VirtualServer, error)

	// ValidateAllServers performs comprehensive validation of all registered servers and returns status
	ValidateAllServers() StatusResponse

	// IsReady reports whether the broker can serve traffic.
	// Returns true when no upstream servers are configured (empty tool list is valid),
	// or when at least one configured upstream is healthy.
	// Returns false only when servers are configured but none have connected yet.
	IsReady() bool

	// HandleStatusRequest handles HTTP status endpoint requests
	HandleStatusRequest(w http.ResponseWriter, r *http.Request)

	// MCPHandler returns the composed /mcp HTTP handler
	MCPHandler() http.Handler

	// IsBrokerToolName returns true if the given tool name is a broker-internal meta-tool
	IsBrokerToolName(name string) bool

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
	// reloadMu serialises OnConfigChange end to end: config.Notify runs each
	// observer in its own goroutine and Stop deliberately runs outside
	// mcpLock, so overlapping reloads could interleave. lock order is
	// reloadMu before mcpLock; never taken on the request path.
	reloadMu sync.Mutex

	// gatewayServer wraps the mcp.Server and tracks tools/prompts
	gatewayServer *gatewayServer

	logger *slog.Logger

	// enforceCapabilityFilter if set will ensure only filtered capabilities are returned based on the x-mcp-authorized trusted header
	enforceCapabilityFilter bool

	// trustedHeadersPublicKey this is the key to verify that a trusted header came from the trusted source (the owner of the private key)
	trustedHeadersPublicKey string

	// managerTickerInterval is the interval for MCP manager backend health checks
	managerTickerInterval time.Duration

	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy mcpv1alpha1.InvalidToolPolicy

	// elicitationEnabled gates URL elicitation credential collection
	elicitationEnabled bool

	// discovery holds config for the discover_tools / select_tools feature
	discovery discoveryConfig

	// scopeStore manages per-session tool scoping
	scopeStore *scopeStore

	// sessionCache stores upstream MCP session IDs per gateway session
	sessionCache *session.Cache

	// userSpecificFetchTimeout is the per-server timeout for user-specific tool fetches
	userSpecificFetchTimeout time.Duration

	// userSpecificServers is precomputed in OnConfigChange to avoid per-request iteration
	userSpecificServers []userSpecificServer

	// gatewayCACertPEM is the current gateway-level CA certificate bundle PEM
	gatewayCACertPEM string

	// userSessionPool caches upstream client sessions per (gateway-session, server)
	// so that repeated tools/list calls reuse the same upstream session.
	userSessionPool sync.Map

	// tagsToolsRegistered tracks whether list_tags/filter_tools_by_tags are currently registered
	tagsToolsRegistered atomic.Bool

	// sessionIDGenerator provides session ID generation (JWT-based)
	sessionIDGenerator func() string

	// sessionValidator validates session tokens; returns (isInvalid, error)
	sessionValidator func(token string) (bool, error)

	// sessionTerminator cleans up backend session cache on session end
	sessionTerminator func(sessionID string) (bool, error)
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

// WithElicitationEnabled enables URL elicitation credential collection
func WithElicitationEnabled(enabled bool) Option {
	return func(mb *mcpBrokerImpl) {
		mb.elicitationEnabled = enabled
	}
}

// WithDiscoveryToolsEnabled enables or disables the discover_tools and select_tools meta-tools
func WithDiscoveryToolsEnabled(enabled bool) Option {
	return func(mb *mcpBrokerImpl) {
		mb.discovery.enabled = enabled
	}
}

// WithDiscoveryToolThreshold sets the tool count above which only meta-tools are shown
func WithDiscoveryToolThreshold(threshold int) Option {
	return func(mb *mcpBrokerImpl) {
		mb.discovery.threshold = threshold
	}
}

// WithSessionCache sets the session cache used for user-specific tool fetches
func WithSessionCache(cache *session.Cache) Option {
	return func(mb *mcpBrokerImpl) {
		mb.sessionCache = cache
	}
}

// WithSessionIDGenerator sets the function used to generate session IDs
func WithSessionIDGenerator(gen func() string) Option {
	return func(mb *mcpBrokerImpl) {
		mb.sessionIDGenerator = gen
	}
}

// WithSessionValidator sets the function used to validate session JWTs
func WithSessionValidator(v func(string) (bool, error)) Option {
	return func(mb *mcpBrokerImpl) {
		mb.sessionValidator = v
	}
}

// WithSessionTerminator sets the function called when a session ends
func WithSessionTerminator(t func(string) (bool, error)) Option {
	return func(mb *mcpBrokerImpl) {
		mb.sessionTerminator = t
	}
}

// WithUserSpecificFetchTimeout sets the per-server timeout for user-specific tool fetches
func WithUserSpecificFetchTimeout(timeout time.Duration) Option {
	return func(mb *mcpBrokerImpl) {
		mb.userSpecificFetchTimeout = timeout
	}
}

// NewBroker creates a new MCPBroker accepts optional config functions such as WithEnforceCapabilityFilter
func NewBroker(logger *slog.Logger, opts ...Option) MCPBroker {
	mcpBkr := &mcpBrokerImpl{
		mcpServers:               map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
		logger:                   logger,
		virtualServers:           map[string]*config.VirtualServer{},
		managerTickerInterval:    time.Second * 60,
		discovery:                discoveryConfig{enabled: true},
		userSpecificFetchTimeout: 30 * time.Second,
	}

	for _, option := range opts {
		option(mcpBkr)
	}

	if mcpBkr.discovery.enabled {
		mcpBkr.scopeStore = newScopeStore(defaultScopeTTL, defaultScopeMaxSize)
	}

	serverOpts := &mcp.ServerOptions{
		HasPrompts: true,
		// mark3labs never paginated list results; a page size no client can
		// reach keeps every list a single page with no nextCursor.
		PageSize:     unpaginatedPageSize,
		GetSessionID: mcpBkr.sessionIDGenerator,
		InitializedHandler: func(_ context.Context, req *mcp.InitializedRequest) {
			sessionID := req.Session.ID()
			mcpBkr.logger.Debug("gateway client session connected", "gatewaySessionID", internaljwt.LogSafeSessionID(sessionID))
			go func() {
				_ = req.Session.Wait()
				mcpBkr.onGatewaySessionEnd(sessionID)
			}()
		},
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{ListChanged: true},
		},
	}
	if mcpBkr.discovery.enabled {
		serverOpts.Instructions = gatewayInstructions
	}

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "Kuadrant MCP Gateway",
			Version: "0.0.1",
		},
		serverOpts,
	)

	// session validity is enforced at the HTTP boundary: the compat layer
	// gates every dispatch on the validator and drops invalidated sessions,
	// and resurrection re-validates before reconnecting unknown ids
	srv.AddReceivingMiddleware(mcpBkr.tracingMiddleware(), mcpBkr.filteringMiddleware())

	mcpBkr.gatewayServer = newGatewayServer(srv)
	srv.AddSendingMiddleware(mcpBkr.gatewayServer.notifyTargetMiddleware())

	if mcpBkr.discovery.enabled {
		mcpBkr.registerDiscoveryTools()
	}

	return mcpBkr
}

// onGatewaySessionEnd releases all per-session state once the SDK session
// terminates: scope entries, pooled upstream client sessions and cached
// backend session IDs.
func (m *mcpBrokerImpl) onGatewaySessionEnd(sessionID string) {
	m.logger.Debug("gateway client session unregistered", "gatewaySessionID", internaljwt.LogSafeSessionID(sessionID))
	if m.scopeStore != nil {
		m.scopeStore.deleteScope(sessionID)
	}
	m.evictUserSessions(sessionID)
	if m.sessionTerminator != nil {
		// the wired terminator (JWTManager.Terminate) bounds its own cache
		// deletion, so no watchdog is needed here
		if _, err := m.sessionTerminator(sessionID); err != nil {
			m.logger.Error("session termination failed", "sessionID", internaljwt.LogSafeSessionID(sessionID), "error", err)
		}
	}
}

// tracingMiddleware wraps each request in a span (replaces the old
// BeforeAny/OnSuccess/OnError hooks)
func (m *mcpBrokerImpl) tracingMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			ctx, span := brokerTracer().Start(ctx, "mcp-broker.handle-request", trace.WithAttributes(
				brokerComponentAttr,
				attribute.String("mcp.method", method),
			))
			defer span.End()
			// LogSafeSessionID hashes/decodes per call; only pay for it when
			// the span is sampled
			if span.IsRecording() {
				if sess := req.GetSession(); sess != nil {
					if sid := sess.ID(); sid != "" {
						span.SetAttributes(attribute.String("mcp.session.id", internaljwt.LogSafeSessionID(sid)))
					}
				}
			}
			m.logger.DebugContext(ctx, "processing request", "method", method)

			result, err := next(ctx, method, req)
			if err != nil {
				m.logger.ErrorContext(ctx, "mcp server error", "method", method, "error", err)
				recordBrokerError(span, err)
			}
			return result, err
		}
	}
}

// filteringMiddleware replaces the old AfterListTools/AfterListPrompts hooks
func (m *mcpBrokerImpl) filteringMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil {
				return result, err
			}

			switch method {
			case "tools/list":
				toolsResult, ok := result.(*mcp.ListToolsResult)
				if !ok || toolsResult == nil {
					return result, nil
				}
				var headers http.Header
				if extra := req.GetExtra(); extra != nil {
					headers = extra.Header
				}
				var sessionID string
				if s := req.GetSession(); s != nil {
					sessionID = s.ID()
				}
				m.FetchUserSpecificTools(ctx, headers, toolsResult)
				m.FilterTools(ctx, headers, sessionID, toolsResult)

			case "prompts/list":
				promptsResult, ok := result.(*mcp.ListPromptsResult)
				if !ok || promptsResult == nil {
					return result, nil
				}
				var headers http.Header
				if extra := req.GetExtra(); extra != nil {
					headers = extra.Header
				}
				m.FilterPrompts(ctx, headers, promptsResult)
			}

			return result, nil
		}
	}
}

func (m *mcpBrokerImpl) OnConfigChange(ctx context.Context, conf *config.MCPServersConfig) {
	// serialise reloads: Notify spawns a goroutine per observer and
	// secret-mount updates fire several fsnotify events back to back. with
	// Stop outside mcpLock, an unserialised stale reload's Stop could delete
	// a replacement manager's tools and then re-apply an old snapshot.
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	// Take a consistent snapshot before acquiring mcpLock; LoadConfig may be
	// concurrently replacing conf.Servers/VirtualServers under its own write lock.
	servers := conf.ListServers()
	virtualServers := conf.ListVirtualServers()
	newGatewayCACert := conf.GetGatewayCACertPEM()

	m.logger.DebugContext(ctx, "Broker OnConfigChange start", "total servers", len(servers))

	// phase 1: deregister removed/changed managers under the lock
	toStop := m.deregisterStaleManagers(ctx, servers, newGatewayCACert)

	// phase 2: stop them outside the lock, and strictly before starting
	// replacements. Stop() blocks on session teardown (network I/O), so
	// holding mcpLock would deadlock IsReady() and middleware readers; and
	// its removeAllTools deletes tools by name with no ownership check, so
	// a replacement that registered first would lose its tools until the
	// next upstream change.
	for _, man := range toStop {
		m.logger.InfoContext(ctx, "stopping manager", "server", man.MCPName())
		man.Stop()
	}

	// phase 3: start new/replacement managers and swap derived state
	m.startManagers(ctx, servers, virtualServers)

	m.logger.DebugContext(ctx, "Broker OnConfigChange done", "total servers", len(servers))
}

// deregisterStaleManagers removes managers whose servers were deleted or
// whose config changed, returning them for the caller to stop.
func (m *mcpBrokerImpl) deregisterStaleManagers(ctx context.Context, servers []*config.MCPServer, newGatewayCACert string) []upstream.ActiveMCPServer {
	m.mcpLock.Lock()
	defer m.mcpLock.Unlock()

	// when gateway CA cert changes, TLS-using managers must be recreated
	gatewayCACertChanged := m.gatewayCACertPEM != newGatewayCACert
	if gatewayCACertChanged {
		m.logger.InfoContext(ctx, "gateway CA certificate bundle changed, recreating TLS managers")
		m.gatewayCACertPEM = newGatewayCACert
	}

	var toStop []upstream.ActiveMCPServer
	for serverID, man := range m.mcpServers {
		if !slices.ContainsFunc(servers, func(s *config.MCPServer) bool {
			return serverID == s.ID()
		}) {
			m.logger.InfoContext(ctx, "un-register upstream server", "server id", serverID)
			toStop = append(toStop, man)
			delete(m.mcpServers, serverID)
		}
	}

	for _, mcpServer := range servers {
		man, ok := m.mcpServers[mcpServer.ID()]
		if !ok {
			continue
		}
		serverUsesTLS := strings.HasPrefix(mcpServer.URL, "https://")
		if (gatewayCACertChanged && serverUsesTLS) || mcpServer.ConfigChanged(man.Config()) {
			m.logger.InfoContext(ctx, "Server Config Changed removing manager", "mcpID", mcpServer.ID())
			toStop = append(toStop, man)
			delete(m.mcpServers, mcpServer.ID())
		}
	}
	return toStop
}

// startManagers creates managers for servers without one and refreshes
// derived state (tags tools, user-specific servers, virtual servers).
func (m *mcpBrokerImpl) startManagers(ctx context.Context, servers []*config.MCPServer, virtualServers []*config.VirtualServer) {
	m.mcpLock.Lock()
	defer m.mcpLock.Unlock()

	for _, mcpServer := range servers {
		if _, ok := m.mcpServers[mcpServer.ID()]; ok {
			continue
		}
		manager, err := upstream.NewUpstreamMCPManager(upstream.NewUpstreamMCP(mcpServer, m.gatewayCACertPEM), m.gatewayServer, m.gatewayServer, m.logger.With("sub-component", "mcp-manager"), m.managerTickerInterval, m.invalidToolPolicy)
		if err != nil {
			m.logger.ErrorContext(ctx, "failed to create manager", "server id", mcpServer.ID(), "error", err)
			continue
		}
		m.logger.InfoContext(ctx, "Starting manager for", "mcpID", mcpServer.ID())
		m.mcpServers[mcpServer.ID()] = manager.Start(ctx)
	}

	m.syncTagsTools(ctx, servers)

	// precompute userSpecificList servers for FetchUserSpecificTools
	m.userSpecificServers = nil
	for _, srv := range servers {
		if srv.UserSpecificList {
			m.userSpecificServers = append(m.userSpecificServers, userSpecificServer{
				id:     srv.ID(),
				name:   srv.Name,
				url:    srv.URL,
				prefix: srv.Prefix,
			})
		}
	}

	// replace virtual servers with the new snapshot so deleted entries are removed
	m.vsLock.Lock()
	next := make(map[string]*config.VirtualServer, len(virtualServers))
	for _, vs := range virtualServers {
		next[vs.Name] = vs
	}
	m.virtualServers = next
	m.vsLock.Unlock()
}

func (m *mcpBrokerImpl) RegisteredMCPServers() map[config.UpstreamMCPID]upstream.ActiveMCPServer {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	return m.mcpServers
}

func (m *mcpBrokerImpl) GetVirtualServerByHeader(namespaceName string) (config.VirtualServer, error) {
	m.vsLock.RLock()
	defer m.vsLock.RUnlock()
	if vs, ok := m.virtualServers[namespaceName]; ok {
		return *vs, nil
	}
	return config.VirtualServer{}, fmt.Errorf("virtual server %s not found", namespaceName)
}

func (m *mcpBrokerImpl) ToolAnnotations(serverID config.UpstreamMCPID, tool string) (upstream.ToolHints, bool) {
	// Avoid race with OnConfigChange()
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	up, ok := m.mcpServers[serverID]
	if !ok {
		return upstream.ToolHints{}, false
	}
	if up.GetServedManagedTool(tool) == nil {
		return upstream.ToolHints{}, false
	}
	// a served tool with no harvested hints is all-unspecified, exactly as
	// mark3labs' zero-value ToolAnnotation was
	h, _ := up.GetToolHints(tool)
	return h, true
}

// upstreamsSnapshot copies the current upstream set under one RLock so
// callers can do repeated per-tool lookups without re-locking.
func (m *mcpBrokerImpl) upstreamsSnapshot() []upstream.ActiveMCPServer {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	ups := make([]upstream.ActiveMCPServer, 0, len(m.mcpServers))
	for _, up := range m.mcpServers {
		ups = append(ups, up)
	}
	return ups
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

	// userSpecificList servers don't cache tools, so match by longest prefix
	var bestMatch config.MCPServer
	var found bool
	for _, upstream := range m.mcpServers {
		cfg := upstream.Config()
		if cfg.UserSpecificList && cfg.Prefix != "" && strings.HasPrefix(tool, cfg.Prefix) {
			if !found || len(cfg.Prefix) > len(bestMatch.Prefix) {
				bestMatch = cfg
				found = true
			}
		}
	}
	if found {
		m.logger.Debug("matched user-specific server by prefix",
			"toolName", tool,
			"serverPrefix", bestMatch.Prefix,
			"serverName", bestMatch.Name)
		return &bestMatch, nil
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

// IsBrokerToolName returns true if the given tool name belongs to a broker-internal
// meta-tool. The router uses this to decide whether to pass a tools/call through
// to the broker instead of looking for an upstream server.
func (m *mcpBrokerImpl) IsBrokerToolName(name string) bool {
	if m.tagsToolsRegistered.Load() && (name == listTagsName || name == filterToolsByTagsName) {
		return true
	}
	if !m.discovery.enabled {
		return false
	}
	return name == discoverToolsName || name == selectToolsName
}

func (m *mcpBrokerImpl) Shutdown(_ context.Context) error {
	// Avoid race with OnConfigChange()
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()

	for _, mcpServer := range m.mcpServers {
		if mcpServer != nil {
			mcpServer.Stop()
		}
	}
	if m.scopeStore != nil {
		m.scopeStore.stop()
	}
	m.drainUserSessionPool()
	return nil
}

// MCPServer is a listening MCP server that federates the endpoints
func (m *mcpBrokerImpl) MCPServer() *mcp.Server {
	return m.gatewayServer.MCPServer()
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

	scopedSessions := 0
	if m.scopeStore != nil {
		scopedSessions = m.scopeStore.size()
	}

	response := StatusResponse{
		Servers:          make([]upstream.ServerValidationStatus, 0),
		OverallValid:     true,
		TotalServers:     len(m.mcpServers),
		HealthyServers:   0,
		UnHealthyServers: 0,
		ToolConflicts:    0,
		ScopedSessions:   scopedSessions,
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
