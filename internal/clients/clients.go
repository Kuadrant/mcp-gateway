/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
	"fmt"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	mcprouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Initialize will create a new initialize and initialized request and return the associated http client for connection management
// This method makes a request back to the gateway setting the target mcp server to initialize. We hairpin through the gateway to ensure any Auth applied to that host is triggered for the call.
func Initialize(ctx context.Context, gatewayHost, routerKey string, conf *config.MCPServer, passThroughHeaders map[string]string) (*client.Client, error) {
	//mcp-gateway-istio
	// force the initialize to hairpin back through envoy
	passThroughHeaders[mcprouter.RoutingKey] = routerKey
	passThroughHeaders["mcp-init-host"] = conf.Hostname

	mcpPath, err := conf.Path()
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s%s", gatewayHost, mcpPath)

	httpClient, err := client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(passThroughHeaders))
	if err != nil {
		return nil, err
	}
	if err := httpClient.Start(ctx); err != nil {
		return nil, err
	}
	if _, err := httpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "mcp-gateway",
				Version: "0.0.1",
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return httpClient, nil
}
