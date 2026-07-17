package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport-level timeouts for upstream HTTP clients. We bound connection
// establishment and response-header reads instead of setting http.Client.Timeout,
// because the same client carries the long-lived SSE notification stream,
// which must not be capped.
var (
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 30 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

// MCPServer represents a connection to an upstream MCP server. It wraps the
// configuration and client, managing the connection lifecycle and storing
// initialization state from the MCP handshake.
type MCPServer struct {
	*config.MCPServer
	client           *mcp.Client
	session          *mcp.ClientSession
	clientMu         sync.RWMutex
	headers          map[string]string
	init             *mcp.InitializeResult
	gatewayCACertPEM string
	logger           *slog.Logger

	// notification watcher state for the current session, guarded by
	// clientMu; at most one watcher per connected session
	watcher       *notificationWatcher
	watcherCancel context.CancelFunc

	// toolHints preserves raw annotation fidelity from the last tools/list
	// exchange, keyed by served (prefixed) tool name. populated by the
	// transport-level tee, replaced wholesale per listing.
	hintsMu   sync.RWMutex
	toolHints map[string]ToolHints

	// notifyHandler receives list-changed notification methods. stored on
	// the upstream so it can be wired into each new client before its
	// session connects, leaving no registration gap.
	notifyMu      sync.RWMutex
	notifyHandler func(method string)
}

// NewUpstreamMCP creates a new MCPServer instance from the provided
// configuration. A nil logger discards output.
func NewUpstreamMCP(config *config.MCPServer, gatewayCACertPEM string, logger *slog.Logger) *MCPServer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	up := &MCPServer{
		MCPServer:        config,
		gatewayCACertPEM: gatewayCACertPEM,
		logger:           logger,
	}
	up.headers = map[string]string{
		"user-agent":        "mcp-broker",
		"gateway-server-id": string(up.ID()),
		"x-client-id":       "broker",
	}
	if up.Credential != "" {
		up.headers["Authorization"] = up.Credential
	}
	return up
}

// buildHTTPClient constructs the HTTP client used to talk to this upstream MCP
// server, with header injection via a custom round tripper. the trust pool is
// built from system roots, plus the gateway-level CA bundle (if set), plus the
// per-server CACert (if set).
func (up *MCPServer) buildHTTPClient() (*http.Client, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	base.ExpectContinueTimeout = defaultExpectContinueTimeout
	// bounds header wait only, not SSE body streaming. without it a silent
	// upstream wedges the manager on any POST (initialize, tools/list).
	base.ResponseHeaderTimeout = defaultResponseHeaderTimeout

	if up.gatewayCACertPEM != "" || up.CACert != "" {
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}
		if up.gatewayCACertPEM != "" {
			if !rootCAs.AppendCertsFromPEM([]byte(up.gatewayCACertPEM)) {
				return nil, fmt.Errorf("failed to parse gateway CA certificate bundle PEM")
			}
		}
		if up.CACert != "" {
			if !rootCAs.AppendCertsFromPEM([]byte(up.CACert)) {
				return nil, fmt.Errorf("failed to parse CA certificate PEM for upstream %s", up.Name)
			}
		}
		base.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
		}
	}

	return &http.Client{
		Transport: &toolHintsTee{
			base: &transport.HeaderRoundTripper{Base: base, Headers: up.headers},
			sink: up.storeToolHints,
		},
	}, nil
}

// storeToolHints replaces the hint set with the latest tools/list harvest.
func (up *MCPServer) storeToolHints(raw map[string]ToolHints) {
	prefixed := make(map[string]ToolHints, len(raw))
	for name, h := range raw {
		prefixed[prefixedName(up.Prefix, name)] = h
	}
	up.hintsMu.Lock()
	up.toolHints = prefixed
	up.hintsMu.Unlock()
}

// GetToolHints returns the raw annotation hints for a served (prefixed)
// tool name from the last tools/list exchange.
func (up *MCPServer) GetToolHints(served string) (ToolHints, bool) {
	up.hintsMu.RLock()
	defer up.hintsMu.RUnlock()
	h, ok := up.toolHints[served]
	return h, ok
}

// SetToolHintsForTesting seeds hints directly, keyed by served name.
// Only for use in tests.
func (up *MCPServer) SetToolHintsForTesting(hints map[string]ToolHints) {
	up.hintsMu.Lock()
	up.toolHints = hints
	up.hintsMu.Unlock()
}

// GetConfig return the config for the backend mcp server
func (up *MCPServer) GetConfig() config.MCPServer {
	var cat []string
	if len(up.Category) > 0 {
		cat = make([]string, len(up.Category))
		copy(cat, up.Category)
	}
	var tags []string
	if len(up.Tags) > 0 {
		tags = make([]string, len(up.Tags))
		copy(tags, up.Tags)
	}
	return config.MCPServer{
		Name:                up.Name,
		URL:                 up.URL,
		Prefix:              up.Prefix,
		State:               up.State,
		Hostname:            up.Hostname,
		Credential:          up.Credential,
		CACert:              up.CACert,
		TokenURLElicitation: up.TokenURLElicitation,
		UserSpecificList:    up.UserSpecificList,
		Category:            cat,
		Hint:                up.Hint,
		Tags:                tags,
	}
}

// IsEnabled returns true if the server should be connected to and have its tools registered.
func (up *MCPServer) IsEnabled() bool {
	return up.State == "" || up.State == string(mcpv1.ServerStateEnabled)
}

// ProtocolInfo returns the initialize result with the protocol information stored in it
func (up *MCPServer) ProtocolInfo() *mcp.InitializeResult {
	return up.init
}

// GetPrefix returns the prefix for this server
func (up *MCPServer) GetPrefix() string {
	return up.Prefix
}

// GetName returns the name of the MCP Server
func (up *MCPServer) GetName() string {
	return up.Name
}

// SupportsToolsListChanged validates the mcp server supports tools/list_changed notifications.
// safe to read up.init without clientMu: init is written once during Connect() which
// happens-before any capability check (manager calls Connect then registers tools).
func (up *MCPServer) SupportsToolsListChanged() bool {
	if up.init == nil || up.init.Capabilities == nil || up.init.Capabilities.Tools == nil {
		return false
	}
	return up.init.Capabilities.Tools.ListChanged
}

// Connect establishes a connection to the upstream MCP server using the
// official SDK's Client+ClientSession pattern.
func (up *MCPServer) Connect(ctx context.Context, onConnection func()) error {
	up.clientMu.RLock()
	if up.session != nil {
		up.clientMu.RUnlock()
		return nil
	}
	up.clientMu.RUnlock()

	httpC, err := up.buildHTTPClient()
	if err != nil {
		return fmt.Errorf("failed to build HTTP client: %w", err)
	}

	streamTransport := &mcp.StreamableClientTransport{
		Endpoint:   up.URL,
		HTTPClient: httpC,
		MaxRetries: 3,
		// the sdk opens the standalone GET SSE stream synchronously inside
		// Connect on a context detached from ours and treats its failure as
		// session-fatal (MaxRetries x ResponseHeaderTimeout blocked, ~125s).
		// upstreams that mishandle the GET must not poison the session, so
		// the sdk never owns that stream: the broker's notification watcher
		// holds it with non-fatal semantics, and the manager's periodic
		// re-list backstops freshness.
		DisableStandaloneSSE: true,
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-broker",
		Version: "0.0.1",
	}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{
			// roots.listChanged matches what mark3labs declared upstream;
			// deprecated in SEP-2577 but still what legacy upstreams expect
			RootsV2:     &mcp.RootCapabilities{ListChanged: true}, //nolint:staticcheck // deliberate parity with mark3labs
			Elicitation: &mcp.ElicitationCapabilities{},
		},
	})
	// wire the notification handler before the session exists so nothing
	// arriving during or right after the handshake is missed
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "notifications/tools/list_changed" || method == "notifications/prompts/list_changed" {
				up.notify(method)
			}
			return next(ctx, method, req)
		}
	})

	up.clientMu.Lock()
	up.client = client
	up.clientMu.Unlock()

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := client.Connect(connectCtx, streamTransport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect client for upstream %s : %w", up.ID(), err)
	}

	up.clientMu.Lock()
	up.session = session
	up.clientMu.Unlock()

	// store the initialize result
	up.init = session.InitializeResult()

	up.startNotificationWatcher(ctx, httpC, session)

	// register notification and connection-lost handlers after session is
	// assigned so OnConnectionLost can start session.Wait() immediately
	onConnection()

	return nil
}

// startNotificationWatcher watches the upstream's standalone SSE stream
// for the lifetime of the session. ctx is the manager's: cancellation on
// manager stop ends the watch even without an explicit Disconnect.
func (up *MCPServer) startNotificationWatcher(ctx context.Context, httpC *http.Client, session *mcp.ClientSession) {
	watchCtx, cancel := context.WithCancel(ctx)
	w := &notificationWatcher{
		endpoint:   up.URL,
		httpClient: httpC,
		// the sdk exposes the server-assigned Mcp-Session-Id directly;
		// empty for stateless upstreams, which then see a bare GET
		sessionID:       session.ID(),
		protocolVersion: up.init.ProtocolVersion,
		serverID:        string(up.ID()),
		notify:          up.notify,
		logger:          up.logger,
		done:            make(chan struct{}),
	}
	up.clientMu.Lock()
	up.watcher = w
	up.watcherCancel = cancel
	up.clientMu.Unlock()
	go w.watch(watchCtx)
}

// stopNotificationWatcher cancels the current watcher, if any, and waits
// for its goroutine to exit. must not be called holding clientMu.
func (up *MCPServer) stopNotificationWatcher() {
	up.clientMu.Lock()
	w, cancel := up.watcher, up.watcherCancel
	up.watcher, up.watcherCancel = nil, nil
	up.clientMu.Unlock()
	if cancel != nil {
		cancel()
		<-w.done
	}
}

// Disconnect closes the connection to the upstream MCP server.
func (up *MCPServer) Disconnect() error {
	up.stopNotificationWatcher()

	up.clientMu.Lock()
	defer up.clientMu.Unlock()

	if up.session != nil {
		if err := up.session.Close(); err != nil {
			up.session = nil
			up.client = nil
			return fmt.Errorf("failed to close session %w", err)
		}
	}
	up.session = nil
	up.client = nil
	return nil
}

// currentSession snapshots the session pointer under the lock. callers use
// the snapshot without holding clientMu: holding an RLock across network
// I/O would block Disconnect, and the SDK session is safe for concurrent
// use after a racing Disconnect (calls just fail).
func (up *MCPServer) currentSession() *mcp.ClientSession {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()
	return up.session
}

// OnNotification registers the tool/prompt list changed notification
// handler. May be called before Connect: the handler is stored on the
// upstream and dispatched by middleware wired into every client this
// upstream creates, before its session is established.
func (up *MCPServer) OnNotification(handler func(method string)) {
	up.notifyMu.Lock()
	up.notifyHandler = handler
	up.notifyMu.Unlock()
}

// notify dispatches a notification method to the registered handler.
func (up *MCPServer) notify(method string) {
	up.notifyMu.RLock()
	handler := up.notifyHandler
	up.notifyMu.RUnlock()
	if handler != nil {
		handler(method)
	}
}

// OnConnectionLost registers a connection lost handler.
// In the official SDK, connection loss is observed via session.Wait().
func (up *MCPServer) OnConnectionLost(handler func(err error)) {
	session := up.currentSession()
	if session != nil {
		go func() {
			if err := session.Wait(); err != nil {
				handler(err)
			}
		}()
	}
}

// Ping sends a ping request to the upstream MCP server to check connectivity
func (up *MCPServer) Ping(ctx context.Context) error {
	session := up.currentSession()
	if session == nil {
		return fmt.Errorf("client not connected")
	}
	return session.Ping(ctx, nil)
}

// SupportsPrompts checks if the upstream server declared prompt capabilities
func (up *MCPServer) SupportsPrompts() bool {
	if up.init == nil || up.init.Capabilities == nil {
		return false
	}
	return up.init.Capabilities.Prompts != nil
}

// SupportsPromptsListChanged validates the mcp server supports prompts/list_changed notifications
func (up *MCPServer) SupportsPromptsListChanged() bool {
	if up.init == nil || up.init.Capabilities == nil || up.init.Capabilities.Prompts == nil {
		return false
	}
	return up.init.Capabilities.Prompts.ListChanged
}

// ListPrompts retrieves the list of available prompts from the upstream MCP server
func (up *MCPServer) ListPrompts(ctx context.Context) (*mcp.ListPromptsResult, error) {
	session := up.currentSession()
	if session == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return session.ListPrompts(ctx, nil)
}

// ListTools retrieves the list of available tools from the upstream MCP server
func (up *MCPServer) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	session := up.currentSession()
	if session == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return session.ListTools(ctx, nil)
}
