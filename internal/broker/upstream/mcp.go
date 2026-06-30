package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Transport-level timeouts for upstream HTTP clients. We bound connection
// establishment and response-header reads instead of setting http.Client.Timeout,
// because the streamable HTTP client reuses the same client for the long-lived
// SSE listen stream, which must not be capped.
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
	client   *client.Client
	clientMu sync.RWMutex
	headers  map[string]string
	init     *mcp.InitializeResult
}

// NewUpstreamMCP creates a new MCPServer instance from the provided configuration.
// It sets up default headers including user-agent and gateway-server-id, and adds
// an Authorization header if credentials are configured.
func NewUpstreamMCP(config *config.MCPServer) *MCPServer {
	up := &MCPServer{
		MCPServer: config,
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
// server. The transport always has handshake and response-header timeouts so a
// hung or unresponsive upstream cannot block the broker indefinitely. When a
// CACert is configured, that CA is appended to the system root pool and used
// for TLS verification.
func (up *MCPServer) buildHTTPClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	transport.ExpectContinueTimeout = defaultExpectContinueTimeout

	if up.CACert != "" {
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}
		if !rootCAs.AppendCertsFromPEM([]byte(up.CACert)) {
			return nil, fmt.Errorf("failed to parse CA certificate PEM for upstream %s", up.Name)
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
		}
	}

	return &http.Client{Transport: transport}, nil
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
// An empty state defaults to enabled for backwards compatibility.
func (up *MCPServer) IsEnabled() bool {
	return up.State == "" || up.State == string(mcpv1alpha1.ServerStateEnabled)
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

// SupportsToolsListChanged validates the mcp server supports tools/list_changed notifications
func (up *MCPServer) SupportsToolsListChanged() bool {
	if up.init == nil {
		return false
	}
	return up.init.Capabilities.Tools.ListChanged
}

// Connect establishes a connection to the upstream MCP server. It creates a
// streamable HTTP client, starts it for continuous listening, and performs
// the MCP initialization handshake. If already connected, this is a no-op.
// The initialization result is stored for later validation of protocol version
// and capabilities.
func (up *MCPServer) Connect(ctx context.Context, onConnection func()) error {
	up.clientMu.RLock()
	if up.client != nil {
		up.clientMu.RUnlock()
		//if we already have a valid connection nothing to do
		return nil
	}
	up.clientMu.RUnlock()

	options := []transport.StreamableHTTPCOption{
		transport.WithContinuousListening(),
		transport.WithHTTPHeaders(up.headers),
	}

	httpC, err := up.buildHTTPClient()
	if err != nil {
		return fmt.Errorf("failed to build HTTP client: %w", err)
	}
	options = append(options, transport.WithHTTPBasicClient(httpC))

	httpClient, err := client.NewStreamableHttpClient(up.URL, options...)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	up.clientMu.Lock()
	up.client = httpClient
	up.clientMu.Unlock()

	// call on connection to register handlers etc
	onConnection()

	// Start the client before initialize to listen for notifications
	err = httpClient.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start streamable client: %w", err)
	}
	initResp, err := httpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities: mcp.ClientCapabilities{
				Roots: &struct {
					ListChanged bool `json:"listChanged,omitempty"`
				}{
					ListChanged: true,
				},
				Elicitation: &mcp.ElicitationCapability{},
			},
			ClientInfo: mcp.Implementation{
				Name:    "mcp-broker",
				Version: "0.0.1",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to initialize client for upstream %s : %w", up.ID(), err)
	}
	// whenever we do an init store the response and session id for validation a future use
	up.init = initResp

	return nil
}

// Disconnect closes the connection to the upstream MCP server. If no client
// connection exists, this is a no-op and returns nil. It will unset the the client if it exists
func (up *MCPServer) Disconnect() error {
	up.clientMu.Lock()
	defer up.clientMu.Unlock()

	if up.client != nil {
		if err := up.client.Close(); err != nil {
			up.client = nil
			return fmt.Errorf("failed to close client %w", err)
		}
	}
	up.client = nil
	return nil
}

// OnNotification allows registering a notification handler func with the client
func (up *MCPServer) OnNotification(handler func(notification mcp.JSONRPCNotification)) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client != nil {
		up.client.OnNotification(handler)
	}
}

// OnConnectionLost allows registering a connection lost handler with the client
func (up *MCPServer) OnConnectionLost(handler func(err error)) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client != nil {
		up.client.OnConnectionLost(handler)
	}
}

// Ping sends a ping request to the upstream MCP server to check connectivity
func (up *MCPServer) Ping(ctx context.Context) error {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client == nil {
		return fmt.Errorf("client not connected")
	}
	return up.client.Ping(ctx)
}

// SupportsPrompts checks if the upstream server declared prompt capabilities
func (up *MCPServer) SupportsPrompts() bool {
	if up.init == nil {
		return false
	}
	return up.init.Capabilities.Prompts != nil
}

// SupportsPromptsListChanged validates the mcp server supports prompts/list_changed notifications
func (up *MCPServer) SupportsPromptsListChanged() bool {
	if up.init == nil {
		return false
	}
	if up.init.Capabilities.Prompts == nil {
		return false
	}
	return up.init.Capabilities.Prompts.ListChanged
}

// ListPrompts retrieves the list of available prompts from the upstream MCP server
func (up *MCPServer) ListPrompts(ctx context.Context, req mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return up.client.ListPrompts(ctx, req)
}

// ListTools retrieves the list of available tools from the upstream MCP server
func (up *MCPServer) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return up.client.ListTools(ctx, req)
}
