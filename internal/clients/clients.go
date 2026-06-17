/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
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
	lowerHost := strings.ToLower(gatewayHost)
	if strings.HasPrefix(lowerHost, "http://") || strings.HasPrefix(lowerHost, "https://") {
		return gatewayHost + mcpPath
	}
	return "http://" + gatewayHost + mcpPath
}

// Initialize will create a new initialize and initialized request and return the associated http client for connection management
// This method makes a request back to the gateway setting the target mcp server to initialize. We hairpin through the gateway to ensure any Auth applied to that host is triggered for the call.
// The initToken is a short-lived JWT bound to conf.Hostname that the router will validate when the hairpin request re-enters the gateway.
func Initialize(ctx context.Context, gatewayHost, initToken string, conf *config.MCPServer, passThroughHeaders map[string]string, clientElicitation bool, hairpinHTTPClient *http.Client) (*client.Client, error) {
	// force the initialize to hairpin back through envoy with a token that
	// proves the request originated from the gateway's own router.
	passThroughHeaders[mcprouter.RoutingKey] = initToken
	passThroughHeaders["mcp-init-host"] = conf.Hostname

	mcpPath, err := conf.Path()
	if err != nil {
		return nil, err
	}

	url := buildHairpinURL(gatewayHost, mcpPath)

	httpClient, err := client.NewStreamableHttpClient(url,
		transport.WithHTTPHeaders(passThroughHeaders),
		transport.WithHTTPBasicClient(hairpinHTTPClient),
	)
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

// BuildHairpinHTTPClient returns an *http.Client for hairpin requests. For HTTPS
// private hosts it configures TLS with ServerName set to publicHost so the
// handshake verifies the cert SANs against the public hostname while the TCP
// connection goes to the internal address. For plain HTTP it returns http.DefaultClient.
func BuildHairpinHTTPClient(privateHost, publicHost, caCertPath string) (*http.Client, error) {
	if !strings.HasPrefix(strings.ToLower(privateHost), "https://") {
		return &http.Client{}, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if caCertPath != "" {
		pem, err := os.ReadFile(caCertPath) //nolint:gosec // path comes from a CLI flag, not user input
		if err != nil {
			return nil, fmt.Errorf("failed to read gateway CA cert: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("failed to parse gateway CA cert PEM")
		}
	}

	// TLS ServerName must be a bare hostname, never host:port
	serverName := publicHost
	if h, _, err := net.SplitHostPort(publicHost); err == nil {
		serverName = h
	}

	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverName,
	}
	return &http.Client{Transport: t}, nil
}
