package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	perClientInitialBackoff = time.Second
	perClientMaxBackoff     = 30 * time.Second
)

// PerClientMCPServer manages a single SSE backend connection for one
// (gatewaySessionID × backendServer) pair. It uses the client's captured auth
// headers rather than the shared broker credentials.
type PerClientMCPServer struct {
	cfg              config.MCPServer
	gatewaySessionID string
	headers          map[string]string
	client           *client.Client
	clientMu         sync.Mutex
	logger           *slog.Logger
}

func newPerClientMCPServer(cfg config.MCPServer, gatewaySessionID string, headers map[string]string, logger *slog.Logger) *PerClientMCPServer {
	return &PerClientMCPServer{
		cfg:              cfg,
		gatewaySessionID: gatewaySessionID,
		headers:          headers,
		logger:           logger,
	}
}

// run manages the connection lifecycle with exponential backoff reconnection.
// It blocks until ctx is cancelled. Called in a goroutine from OpenConnections.
func (p *PerClientMCPServer) run(ctx context.Context, onNotification func(mcp.JSONRPCNotification)) {
	backoff := perClientInitialBackoff

	for {
		select {
		case <-ctx.Done():
			p.closeClient()
			return
		default:
		}

		lostCh := make(chan struct{}, 1)
		err := p.connect(ctx, onNotification, func(_ error) {
			select {
			case lostCh <- struct{}{}:
			default:
			}
		})

		if err != nil {
			p.logger.Debug("per-client backend connection failed, will retry",
				"server", p.cfg.Name,
				"gatewaySessionID", p.gatewaySessionID,
				"backoff", backoff,
				"error", err,
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, perClientMaxBackoff)
				continue
			}
		}

		// connected — reset backoff and wait
		backoff = perClientInitialBackoff
		p.logger.Debug("per-client backend connected",
			"server", p.cfg.Name,
			"gatewaySessionID", p.gatewaySessionID,
		)

		select {
		case <-ctx.Done():
			p.closeClient()
			return
		case <-lostCh:
			p.logger.Debug("per-client backend connection lost, reconnecting",
				"server", p.cfg.Name,
				"gatewaySessionID", p.gatewaySessionID,
			)
			p.closeClient()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(backoff*2, perClientMaxBackoff)
			}
		}
	}
}

// transportHeaders returns only the headers safe to forward on a backend connection.
// Transport-level headers (accept, content-type, host, connection, transfer-encoding)
// are excluded because mcp-go sets them itself; forwarding them overrides the correct
// values and breaks the Streamable HTTP handshake.
func transportHeaders(headers map[string]string) map[string]string {
	skip := map[string]struct{}{
		"accept":            {},
		"content-type":      {},
		"host":              {},
		"connection":        {},
		"transfer-encoding": {},
		"user-agent":        {},
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if _, blocked := skip[k]; !blocked {
			out[k] = v
		}
	}
	return out
}

func (p *PerClientMCPServer) connect(ctx context.Context, onNotification func(mcp.JSONRPCNotification), onConnectionLost func(error)) error {
	options := []transport.StreamableHTTPCOption{
		transport.WithContinuousListening(),
		transport.WithHTTPHeaders(transportHeaders(p.headers)),
	}

	httpClient, err := client.NewStreamableHttpClient(p.cfg.URL, options...)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	p.clientMu.Lock()
	p.client = httpClient
	p.clientMu.Unlock()

	// register handlers before Start so they are active for the first notification
	httpClient.OnNotification(onNotification)
	httpClient.OnConnectionLost(onConnectionLost)

	if err := httpClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start client: %w", err)
	}

	_, err = httpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "mcp-broker",
				Version: "0.0.1",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to initialize with %s: %w", p.cfg.Name, err)
	}

	return nil
}

func (p *PerClientMCPServer) closeClient() {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
}
