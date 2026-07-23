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

// buildGatewayTransport creates a streamable transport with e2e headers.
// When block2026 is true, wraps with blockDiscoverTransport to force
// 2025-11-25 negotiation. When false, allows 2026-07-28 via server/discover.
func buildGatewayTransport(gatewayHost string, headers map[string]string, block2026 bool) *mcp.StreamableClientTransport {
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
	//The SDK has no public API to force 2025 negotiation so we have to intercept with the round tripper.
	var rt http.RoundTripper = &transport.HeaderRoundTripper{Base: base, Headers: allHeaders}
	if block2026 {
		rt = &blockDiscoverTransport{base: rt}
	}
	httpClient.Transport = rt
	return &mcp.StreamableClientTransport{
		Endpoint:   gatewayHost,
		HTTPClient: httpClient,
	}
}

// blockDiscoverTransport wraps an http.RoundTripper and strips the
// Mcp-Protocol-Version header, forcing the SDK to fall back to the
// legacy initialize handshake (2025-11-25).
type blockDiscoverTransport struct {
	base http.RoundTripper
}

func (t *blockDiscoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	r2.Header.Del("Mcp-Protocol-Version")
	return t.base.RoundTrip(r2)
}

// NewStatelessClient creates an MCP client that negotiates 2026-07-28
// via server/discover. Use for tests that explicitly need stateless protocol.
func NewStatelessClient(ctx context.Context, gatewayHost string) (*mcp.ClientSession, error) {
	return NewStatelessClientWithHeaders(ctx, gatewayHost, nil)
}

// NewStatelessClientWithHeaders creates a 2026-07-28 MCP client with custom headers.
func NewStatelessClientWithHeaders(ctx context.Context, gatewayHost string, headers map[string]string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-2026", Version: "0.0.1"}, nil)
	return client.Connect(ctx, buildGatewayTransport(gatewayHost, headers, false), nil)
}

// NotifyingMCPClient wraps an MCP client session with notification handling
type NotifyingMCPClient struct {
	*mcp.ClientSession
}

// NewStatefulClient creates a new MCP client connected to the gateway
func NewStatefulClient(ctx context.Context, gatewayHost string) (*mcp.ClientSession, error) {
	return NewStatefulClientWithHeaders(ctx, gatewayHost, nil)
}

// NewStatefulClientWithHeaders creates a new MCP client with custom headers
func NewStatefulClientWithHeaders(ctx context.Context, gatewayHost string, headers map[string]string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.1"}, nil)
	return client.Connect(ctx, buildGatewayTransport(gatewayHost, headers, true), nil)
}

// NewStatefulClientWithNotifications creates an MCP client that reports tools
// and prompts list_changed notifications to notificationFunc by method name.
func NewStatefulClientWithNotifications(ctx context.Context, gatewayHost string, notificationFunc func(string)) (*NotifyingMCPClient, error) {
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

	session, err := client.Connect(ctx, buildGatewayTransport(gatewayHost, nil, true), nil)
	if err != nil {
		return nil, err
	}

	return &NotifyingMCPClient{
		ClientSession: session,
	}, nil
}

// NewStatefulClientWithElicitation creates an MCP client with an elicitation handler.
func NewStatefulClientWithElicitation(ctx context.Context, gatewayHost string, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-elicitation", Version: "0.0.1"}, &mcp.ClientOptions{
		ElicitationHandler: handler,
	})
	return client.Connect(ctx, buildGatewayTransport(gatewayHost, nil, true), nil)
}
