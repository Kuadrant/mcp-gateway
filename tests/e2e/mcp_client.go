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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
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

// newGatewayTransport builds the streamable transport shared by all e2e
// clients: e2e marker plus custom headers injected via
// transport.HeaderRoundTripper.
func newGatewayTransport(gatewayHost string, headers map[string]string) *mcp.StreamableClientTransport {
	allHeaders := map[string]string{"e2e": "client"}
	maps.Copy(allHeaders, headers)

	httpClient := e2eHTTPClient(gatewayHost)
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &transport.HeaderRoundTripper{Base: base, Headers: allHeaders}
	return &mcp.StreamableClientTransport{
		Endpoint:   gatewayHost,
		HTTPClient: httpClient,
	}
}

// NotifyingMCPClient wraps an MCP client session with notification handling
type NotifyingMCPClient struct {
	*mcp.ClientSession
	sessionID string
}

// NewMCPGatewayClient creates a new MCP client connected to the gateway
func NewMCPGatewayClient(ctx context.Context, gatewayHost string) (*mcp.ClientSession, error) {
	return NewMCPGatewayClientWithHeaders(ctx, gatewayHost, nil)
}

// NewMCPGatewayClientWithHeaders creates a new MCP client with custom headers
func NewMCPGatewayClientWithHeaders(ctx context.Context, gatewayHost string, headers map[string]string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.1"}, nil)
	return client.Connect(ctx, newGatewayTransport(gatewayHost, headers), nil)
}

// NewMCPGatewayClientWithNotifications creates an MCP client that reports tools
// and prompts list_changed notifications to notificationFunc by method name.
func NewMCPGatewayClientWithNotifications(ctx context.Context, gatewayHost string, notificationFunc func(string)) (*NotifyingMCPClient, error) {
	notify := func(method string) {
		if notificationFunc != nil {
			notificationFunc(method)
			return
		}
		GinkgoWriter.Println("default notification handler:", method)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, _ *mcp.ToolListChangedRequest) {
			notify("notifications/tools/list_changed")
		},
		PromptListChangedHandler: func(_ context.Context, _ *mcp.PromptListChangedRequest) {
			notify("notifications/prompts/list_changed")
		},
	})

	session, err := client.Connect(ctx, newGatewayTransport(gatewayHost, nil), nil)
	if err != nil {
		return nil, err
	}

	return &NotifyingMCPClient{
		ClientSession: session,
		sessionID:     session.ID(),
	}, nil
}

// NewMCPGatewayClientWithElicitation creates an MCP client with an elicitation handler.
func NewMCPGatewayClientWithElicitation(ctx context.Context, gatewayHost string, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-elicitation", Version: "0.0.1"}, &mcp.ClientOptions{
		ElicitationHandler: handler,
	})
	return client.Connect(ctx, newGatewayTransport(gatewayHost, nil), nil)
}
