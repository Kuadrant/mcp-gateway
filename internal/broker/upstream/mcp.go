package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
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

// tokenRequestTimeout bounds a single token-endpoint round trip. the source is
// built per Connect, so a wedged AS fails the connect into the manager's
// backoff rather than blocking the health tick.
const tokenRequestTimeout = 10 * time.Second

// cache scope values for upstream list response hints
const (
	CacheScopePublic  = "public"
	CacheScopePrivate = "private"
)

// CacheMetadata holds per-result-type cache hints from upstream list responses.
type CacheMetadata struct {
	TTLMs            int
	CacheScope       string // CacheScopePublic or CacheScopePrivate
	UserSpecificList bool   // from CRD, carried through for scope aggregation
}

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

	// cache metadata from the last tools/list and prompts/list responses,
	// guarded by clientMu
	toolsCacheMeta   CacheMetadata
	promptsCacheMeta CacheMetadata

	// notifyHandler receives list-changed notification methods. stored on
	// the upstream so it can be wired into each new client before its
	// session connects, leaving no registration gap.
	notifyMu      sync.RWMutex
	notifyHandler func(method string)

	// dc intercepts the SDK's server/discover response during Connect
	// to capture the upstream's SupportedVersions.
	dc *discoverCapture

	// supportedVersions lists protocol versions this upstream supports.
	supportedVersions []string
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
		toolsCacheMeta:   CacheMetadata{UserSpecificList: config.UserSpecificList},
		promptsCacheMeta: CacheMetadata{UserSpecificList: config.UserSpecificList},
	}
	up.headers = map[string]string{
		"user-agent":        "mcp-broker",
		"gateway-server-id": string(up.ID()),
		"x-client-id":       "broker",
	}
	// a token source owns Authorization; the two are mutually exclusive in the
	// CRD, but branch on OAuth2 first so a hand-written config cannot set both
	if up.OAuth2 == nil && up.Credential != "" {
		up.headers["Authorization"] = up.Credential
	}
	return up
}

// buildHTTPClient constructs the HTTP client used to talk to this upstream MCP
// server, with header injection via a custom round tripper. the trust pool is
// built from system roots, plus the gateway-level CA bundle (if set), plus the
// per-server CACert (if set). the discoverCapture is stored on up.dc to
// intercept the SDK's server/discover response during Connect.
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

	// the token source sits below the header injector so oauth2.Transport sets
	// Authorization last and wins over anything in up.headers
	var inner http.RoundTripper = base
	if up.OAuth2 != nil {
		ts, err := up.newTokenSource(base)
		if err != nil {
			return nil, err
		}
		inner = &oauth2.Transport{Source: ts, Base: base}
	}

	up.dc = &discoverCapture{
		base: &toolHintsTee{
			base: &transport.HeaderRoundTripper{Base: inner, Headers: up.headers},
			sink: up.storeToolHints,
		},
	}
	return &http.Client{Transport: up.dc}, nil
}

// sanitizedTokenSource replaces the oauth2 package's token error, which quotes
// the AS response body, with one naming the upstream only — the error becomes
// the /status Message. the detail is logged instead of dropped. it delegates
// caching to the wrapped ReuseTokenSource, so refresh still happens.
type sanitizedTokenSource struct {
	src    oauth2.TokenSource
	id     config.UpstreamMCPID
	logger *slog.Logger
}

func (s *sanitizedTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		s.logger.Error("token request failed", "upstream mcp server", s.id, "error", err)
		return nil, fmt.Errorf("failed to obtain access token for upstream %s", s.id)
	}
	return tok, nil
}

// newTokenSource builds a client-credentials token source for this upstream.
// the AS is reached through base — the same trust pool as the upstream, so a
// private CA in the gateway bundle covers both — and deliberately not through
// the header chain: the AS is not an MCP server and must not see broker
// identity headers. the ReuseTokenSource underneath re-requests near expiry;
// wrapping it in another one would disable refresh.
func (up *MCPServer) newTokenSource(base http.RoundTripper) (oauth2.TokenSource, error) {
	// CEL guards the CRD, but the broker reads a mounted file
	if !strings.HasPrefix(up.OAuth2.TokenURL, "https://") {
		return nil, fmt.Errorf("token URL for upstream %s must use https", up.Name)
	}
	cc := &clientcredentials.Config{
		ClientID:     up.OAuth2.ClientID,
		ClientSecret: up.OAuth2.ClientSecret,
		TokenURL:     up.OAuth2.TokenURL,
		Scopes:       up.OAuth2.Scopes,
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient,
		&http.Client{Transport: base, Timeout: tokenRequestTimeout})
	return &sanitizedTokenSource{src: cc.TokenSource(ctx), id: up.ID(), logger: up.logger}, nil
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
	var guardrailsConfigIDs []string
	if len(up.GuardrailsConfigIDs) > 0 {
		guardrailsConfigIDs = make([]string, len(up.GuardrailsConfigIDs))
		copy(guardrailsConfigIDs, up.GuardrailsConfigIDs)
	}
	var supportedProtocolVersions []string
	if len(up.SupportedProtocolVersions) > 0 {
		supportedProtocolVersions = make([]string, len(up.SupportedProtocolVersions))
		copy(supportedProtocolVersions, up.SupportedProtocolVersions)
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
		OAuth2:              up.OAuth2,
		UserSpecificList:    up.UserSpecificList,
		Category:            cat,
		Hint:                up.Hint,
		Tags:                tags,
		GuardrailsConfigIDs: guardrailsConfigIDs,

		SupportedProtocolVersions: supportedProtocolVersions,
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
		// go-sdk <= v1.7.0 opens subscriptions/listen during Connect when these
		// handlers are set. servers that return 404 for subscriptions/listen
		// (e.g. GitHub MCP, FastMCP 4.x) cause Connect to fail fatally.
		// re-add once go-sdk >= v1.8.0 is adopted (PR #1193 wraps the failure).
		// without handlers the broker relies on TTL-based polling for freshness.
	})
	// wire the notification handler before the session exists so nothing
	// arriving during or right after the handshake is missed
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			up.logger.Debug("upstream receiving middleware", "upstream", up.ID(), "method", method)
			if method == "notifications/tools/list_changed" || method == "notifications/prompts/list_changed" {
				up.logger.Debug("upstream notification received", "upstream", up.ID(), "notification", method)
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
	up.logger.Debug("connecting to upstream", "upstream", up.ID(), "endpoint", up.URL)
	session, err := client.Connect(connectCtx, streamTransport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect client for upstream %s : %w", up.ID(), err)
	}

	up.clientMu.Lock()
	up.session = session
	up.clientMu.Unlock()

	// store the initialize result
	up.init = session.InitializeResult()

	negotiated := up.init.ProtocolVersion
	up.dc.SetConnected()
	// An operator override short-circuits discovery: skip the discover-capture
	// wait entirely and trust the configured list.
	var captured []string
	if len(up.SupportedProtocolVersions) == 0 {
		if c, verr := up.dc.Versions(); verr == nil {
			captured = c
		}
	}
	up.supportedVersions = resolveSupportedVersions(up.SupportedProtocolVersions, captured, negotiated)
	up.logger.Debug("upstream connected", "upstream", up.ID(), "negotiated-protocol", negotiated, "supported-versions", up.supportedVersions, "uses-stateless", up.UsesStatelessProtocol())

	// session-ful 2025 upstreams use the custom GET SSE notificationWatcher for
	// push notifications. stateless upstreams rely on TTL-based polling: 2026
	// has no notification handlers until go-sdk >= v1.8.0 (see client options
	// above), and a session-less 2025 upstream has no Mcp-Session-Id to key the
	// standalone GET SSE stream on.
	if !up.UsesStatelessProtocol() {
		up.logger.Debug("starting GET SSE notification watcher", "upstream", up.ID(), "negotiated-protocol", negotiated)
		up.startNotificationWatcher(ctx, httpC, session)
	}

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
// upstreams that return no Mcp-Session-Id from initialize do not need
// connection-loss monitoring: the SDK's subscriptions/listen stream
// will fail immediately with an empty session ID, which is expected
// and not a real connection loss. the ticker health check and ping
// handle failure detection for these upstreams.
func (up *MCPServer) OnConnectionLost(handler func(err error)) {
	session := up.currentSession()
	if session == nil {
		return
	}
	if session.ID() == "" {
		up.logger.Debug("skipping connection-lost watcher, no session ID from upstream", "upstream", up.ID())
		return
	}
	go func() {
		if err := session.Wait(); err != nil {
			// only fire for the currently tracked session; a planned
			// Disconnect replaces up.session before the old Wait returns
			if up.currentSession() != session {
				up.logger.Debug("ignoring connection loss from superseded session", "upstream", up.ID())
				return
			}
			handler(err)
		}
	}()
}

// UsesStatelessProtocol returns true if the upstream is stateless: it either
// negotiated protocol version 2026-07-28 or later, or it issued no
// Mcp-Session-Id — an upstream on an older MCP SDK down-negotiates to an
// earlier 2025 revision and runs the streamable-HTTP transport in stateless
// mode. Returns false when not connected: nothing to classify yet.
func (up *MCPServer) UsesStatelessProtocol() bool {
	if up.init == nil {
		return false
	}
	if up.init.ProtocolVersion >= protocol.Version2026 {
		return true
	}
	session := up.currentSession()
	return session != nil && session.ID() == ""
}

// resolveSupportedVersions determines the protocol versions the broker treats
// an upstream as supporting. An explicit operator override wins over the
// versions captured from server/discover, which in turn win over the single
// version negotiated at initialize. The negotiated version is always included,
// since the upstream demonstrably serves it. The returned slice never aliases
// the override or captured input.
func resolveSupportedVersions(override, captured []string, negotiated string) []string {
	var versions []string
	switch {
	case len(override) > 0:
		versions = slices.Clone(override)
	case len(captured) > 0:
		versions = slices.Clone(captured)
	}
	if negotiated != "" && !slices.Contains(versions, negotiated) {
		versions = append(versions, negotiated)
	}
	return versions
}

// SupportedVersions returns the list of protocol versions this upstream supports.
// Returns nil if not yet connected (init is nil).
func (up *MCPServer) SupportedVersions() []string {
	if len(up.supportedVersions) == 0 {
		return nil
	}
	// return a copy to prevent mutation
	result := make([]string, len(up.supportedVersions))
	copy(result, up.supportedVersions)
	return result
}

// SupportsVersion returns true if this upstream supports the given protocol version.
func (up *MCPServer) SupportsVersion(v string) bool {
	return slices.Contains(up.supportedVersions, v)
}

// Ping sends a ping request to the upstream MCP server to check connectivity.
// Returns nil for stateless upstreams: on 2026-07-28 the SDK does not inject
// the required _meta fields on ping requests (SDK bug), and a session-less
// upstream has no session to ping at all. For both, a successful Connect is
// sufficient proof of connectivity.
func (up *MCPServer) Ping(ctx context.Context) error {
	if up.UsesStatelessProtocol() {
		return nil
	}
	session := up.currentSession()
	if session == nil {
		return fmt.Errorf("client not connected")
	}
	return session.Ping(ctx, nil)
}

// ToolsCacheMetadata returns the cache metadata from the last tools/list response.
func (up *MCPServer) ToolsCacheMetadata() CacheMetadata {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()
	return up.toolsCacheMeta
}

// PromptsCacheMetadata returns the cache metadata from the last prompts/list response.
func (up *MCPServer) PromptsCacheMetadata() CacheMetadata {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()
	return up.promptsCacheMeta
}

// SetCacheMetadataForTesting sets cache metadata directly for testing.
func (up *MCPServer) SetCacheMetadataForTesting(toolsMeta, promptsMeta CacheMetadata) {
	up.clientMu.Lock()
	defer up.clientMu.Unlock()
	up.toolsCacheMeta = toolsMeta
	up.promptsCacheMeta = promptsMeta
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
	result, err := session.ListPrompts(ctx, nil)
	if err != nil {
		return nil, err
	}
	// partial update: preserve UserSpecificList set at construction
	up.clientMu.Lock()
	up.promptsCacheMeta.TTLMs = result.TTLMs
	up.promptsCacheMeta.CacheScope = result.CacheScope
	up.clientMu.Unlock()
	return result, nil
}

// ListTools retrieves the list of available tools from the upstream MCP server
func (up *MCPServer) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	session := up.currentSession()
	if session == nil {
		return nil, fmt.Errorf("client not connected")
	}
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	// partial update: preserve UserSpecificList set at construction
	up.clientMu.Lock()
	up.toolsCacheMeta.TTLMs = result.TTLMs
	up.toolsCacheMeta.CacheScope = result.CacheScope
	up.clientMu.Unlock()
	return result, nil
}

// SupportsResources checks if the upstream server declared resource capabilities
func (up *MCPServer) SupportsResources() bool {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()
	if up.init == nil || up.init.Capabilities == nil {
		return false
	}
	return up.init.Capabilities.Resources != nil
}

// ListResources retrieves the list of available resources from the upstream MCP server
func (up *MCPServer) ListResources(ctx context.Context) (*mcp.ListResourcesResult, error) {
	session := up.currentSession()
	if session == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return session.ListResources(ctx, nil)
}
