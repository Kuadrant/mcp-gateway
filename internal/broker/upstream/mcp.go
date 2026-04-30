package upstream

import (
	"context"
	"fmt"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPServer represents a connection to an upstream MCP server.
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
	}
	if up.Credential != "" {
		up.headers["Authorization"] = up.Credential
	}
	return up
}

func (up *MCPServer) GetConfig() config.MCPServer {
	return config.MCPServer{
		Name:       up.Name,
		URL:        up.URL,
		ToolPrefix: up.ToolPrefix,
		Enabled:    up.Enabled,
		Hostname:   up.Hostname,
		Credential: up.Credential,
	}
}

func (up *MCPServer) ProtocolInfo() *mcp.InitializeResult {
	return up.init
}

func (up *MCPServer) GetPrefix() string {
	return up.ToolPrefix
}

func (up *MCPServer) GetName() string {
	return up.Name
}

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
// NOTE: includes reconnection and stale session handling logic
func (up *MCPServer) Connect(ctx context.Context, onConnection func()) error {
	//  Check if existing client is still valid
	up.clientMu.RLock()
	existingClient := up.client
	if existingClient != nil {
		if err := existingClient.Ping(ctx); err == nil {
			up.clientMu.RUnlock()
			return nil
		}
	}
	up.clientMu.RUnlock()

	//  Cleanup stale client
	up.clientMu.Lock()
	if up.client == existingClient {
		if up.client != nil {
			_ = up.client.Close()
			up.client = nil
			up.init = nil
		}
	}
	up.clientMu.Unlock()

	options := []transport.StreamableHTTPCOption{
		transport.WithContinuousListening(),
		transport.WithHTTPHeaders(up.headers),
	}

	httpClient, err := client.NewStreamableHttpClient(up.URL, options...)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// set new client
	up.clientMu.Lock()
	up.client = httpClient
	up.clientMu.Unlock()

	currentClient := httpClient

	//  Safe connection lost handler (no stale callback bug)
	httpClient.OnConnectionLost(func(err error) {
		up.clientMu.Lock()
		defer up.clientMu.Unlock()

		if up.client == currentClient {
			_ = up.client.Close()
			up.client = nil
			up.init = nil
		}
	})

	// register handlers etc
	onConnection()

	// start listening (SSE etc)
	if err := httpClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start streamable client: %w", err)
	}

	// initialize session
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
		//  Cleanup on init failure
		up.clientMu.Lock()
		if up.client == currentClient {
			_ = up.client.Close()
			up.client = nil
			up.init = nil
		}
		up.clientMu.Unlock()

		return fmt.Errorf("failed to initialize client for upstream %s : %w", up.ID(), err)
	}

	up.init = initResp
	return nil
}

// Disconnect closes connection
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
	up.init = nil
	return nil
}

func (up *MCPServer) OnNotification(handler func(notification mcp.JSONRPCNotification)) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client != nil {
		up.client.OnNotification(handler)
	}
}

func (up *MCPServer) OnConnectionLost(handler func(err error)) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client != nil {
		up.client.OnConnectionLost(handler)
	}
}

func (up *MCPServer) Ping(ctx context.Context) error {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client == nil {
		return fmt.Errorf("client not connected")
	}
	return up.client.Ping(ctx)
}

func (up *MCPServer) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()

	if up.client == nil {
		return nil, fmt.Errorf("client not connected")
	}
	return up.client.ListTools(ctx, req)
}
