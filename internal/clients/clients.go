/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
	"fmt"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	mcprouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// buildHairpinURL composes the hairpin URL the broker uses to send the internal
// initialize request back through the gateway. gatewayHost may be either a
// bare host[:port] (in which case http:// is assumed for backwards
// compatibility) or a full URL prefix that already carries an http:// or
// https:// scheme. This is what lets HTTPS-listener hairpins work without
// silently sending plain HTTP to a TLS-only port (issue #917).
func buildHairpinURL(gatewayHost, mcpPath string) string {
	if strings.HasPrefix(gatewayHost, "http://") || strings.HasPrefix(gatewayHost, "https://") {
		return gatewayHost + mcpPath
	}
	return "http://" + gatewayHost + mcpPath
}

// Initialize will create a new initialize and initialized request and return the associated http client for connection management
// This method makes a request back to the gateway setting the target mcp server to initialize. We hairpin through the gateway to ensure any Auth applied to that host is triggered for the call.
func Initialize(ctx context.Context, gatewayHost, routerKey string, conf *config.MCPServer, passThroughHeaders map[string]string, clientElicitation bool) (*client.Client, error) {
	//mcp-gateway-istio
	// force the initialize to hairpin back through envoy
	passThroughHeaders[mcprouter.RoutingKey] = routerKey
	passThroughHeaders["mcp-init-host"] = conf.Hostname

	mcpPath, err := conf.Path()
	if err != nil {
		return nil, err
	}

	url := buildHairpinURL(gatewayHost, mcpPath)

	httpClient, err := client.NewStreamableHttpClient(url, transport.WithHTTPHeaders(passThroughHeaders))
	if err != nil {
		return nil, err
	}
	if err := httpClient.Start(ctx); err != nil {
		return nil, err
	}
	caps := mcp.ClientCapabilities{}
	if clientElicitation {
		caps.Elicitation = &mcp.ElicitationCapability{}
	}
	if _, err := httpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    caps,
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
