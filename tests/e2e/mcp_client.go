//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"maps"
	"net"
	"net/http"
	"strings"

	goenv "github.com/caitlinelfring/go-env-default"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
)

var useInsecureClient = goenv.GetDefault("INSECURE_CLIENT", "false")

// e2eHTTPClient returns an *http.Client configured for e2e tests.
// For HTTPS URLs it sets InsecureSkipVerify and adds a custom dialer
// that resolves non-routable hostnames (e.g. *.mcp-gateway.local) to localhost.
func e2eHTTPClient(url string) *http.Client {
	if !strings.HasPrefix(url, "https://") && strings.ToLower(useInsecureClient) != "true" {
		return nil
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(addr)
				if strings.HasSuffix(host, ".local") {
					addr = net.JoinHostPort("127.0.0.1", port)
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
	}
}

// NotifyingMCPClient wraps an MCP client with notification handling
type NotifyingMCPClient struct {
	*mcpclient.Client
	sessionID string
}

// NewMCPGatewayClient creates a new MCP client connected to the gateway
func NewMCPGatewayClient(ctx context.Context, gatewayHost string) (*mcpclient.Client, error) {
	return NewMCPGatewayClientWithHeaders(ctx, gatewayHost, nil)
}

// NewMCPGatewayClientWithHeaders creates a new MCP client with custom headers
func NewMCPGatewayClientWithHeaders(ctx context.Context, gatewayHost string, headers map[string]string) (*mcpclient.Client, error) {
	allHeaders := map[string]string{"e2e": "client"}
	maps.Copy(allHeaders, headers)
	options := []transport.StreamableHTTPCOption{transport.
		WithHTTPHeaders(allHeaders), transport.WithContinuousListening()}
	if hc := e2eHTTPClient(gatewayHost); hc != nil {
		options = append(options, transport.WithHTTPBasicClient(hc))
	}

	gatewayClient, err := mcpclient.NewStreamableHttpClient(gatewayHost, options...)
	if err != nil {
		return nil, err
	}
	err = gatewayClient.Start(ctx)
	if err != nil {
		return nil, err
	}
	_, err = gatewayClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "e2e",
				Version: "0.0.1",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return gatewayClient, nil
}

// NewMCPGatewayClientWithNotifications creates an MCP client that captures notifications
func NewMCPGatewayClientWithNotifications(ctx context.Context, gatewayHost string, notificationFunc func(mcp.JSONRPCNotification)) (*NotifyingMCPClient, error) {
	client, err := NewMCPGatewayClientWithHeaders(ctx, gatewayHost, nil)
	if err != nil {
		return nil, err
	}

	client.OnNotification(func(notification mcp.JSONRPCNotification) {
		if notificationFunc != nil {
			notificationFunc(notification)
			return
		}
	})

	client.OnConnectionLost(func(err error) {
		GinkgoWriter.Println("connection lost", err)
	})

	return &NotifyingMCPClient{
		Client:    client,
		sessionID: client.GetSessionId(),
	}, nil
}

// NewMCPGatewayClientWithElicitation creates an MCP client with an elicitation handler.
// Uses manual transport construction since NewStreamableHttpClient doesn't accept ClientOptions.
func NewMCPGatewayClientWithElicitation(ctx context.Context, gatewayHost string, handler mcpclient.ElicitationHandler) (*mcpclient.Client, error) {
	allHeaders := map[string]string{"e2e": "client"}
	options := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(allHeaders),
		transport.WithContinuousListening(),
	}
	if hc := e2eHTTPClient(gatewayHost); hc != nil {
		options = append(options, transport.WithHTTPBasicClient(hc))
	}

	trans, err := transport.NewStreamableHTTP(gatewayHost, options...)
	if err != nil {
		return nil, err
	}

	clientOpts := []mcpclient.ClientOption{
		mcpclient.WithElicitationHandler(handler),
	}
	gatewayClient := mcpclient.NewClient(trans, clientOpts...)

	if err := gatewayClient.Start(ctx); err != nil {
		return nil, err
	}
	_, err = gatewayClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{}},
			ClientInfo: mcp.Implementation{
				Name:    "e2e-elicitation",
				Version: "0.0.1",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return gatewayClient, nil
}
