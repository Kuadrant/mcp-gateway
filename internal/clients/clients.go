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
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HairpinClientPool manages *http.Client instances for hairpin requests,
// keyed by TLS ServerName (SNI). For HTTP or when all servers share the
// same listener, a single default client is used. When a server attaches
// to a different HTTPS listener, a client with that server's SNI is
// created on demand and cached.
type HairpinClientPool struct {
	defaultClient *http.Client
	baseTLSConfig *tls.Config // nil for HTTP
	mu            sync.RWMutex
	clients       map[string]*http.Client
}

// Get returns an *http.Client with the appropriate TLS ServerName.
// If sniOverride is empty, the default client (gateway hostname SNI) is returned.
func (p *HairpinClientPool) Get(sniOverride string) *http.Client {
	if sniOverride == "" || p.baseTLSConfig == nil {
		return p.defaultClient
	}

	sni := sniOverride
	if h, _, err := net.SplitHostPort(sniOverride); err == nil {
		sni = h
	}

	// double-checked locking: read lock for the fast path (concurrent readers),
	// then write lock with a re-check to avoid duplicate creation when multiple
	// goroutines race past the read lock simultaneously
	p.mu.RLock()
	c, ok := p.clients[sni]
	p.mu.RUnlock()
	if ok {
		return c
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok = p.clients[sni]; ok {
		return c
	}

	cfg := p.baseTLSConfig.Clone()
	cfg.ServerName = sni
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = cfg
	c = &http.Client{Transport: t}
	p.clients[sni] = c
	return c
}

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

// Initialize will create a new initialize and initialized request and return the associated client session.
// This method makes a request back through the gateway to ensure any AuthPolicy is triggered.
// The caller must set the routing key and mcp-init-host headers in passThroughHeaders before calling.
func Initialize(ctx context.Context, gatewayHost string, conf *config.MCPServer, passThroughHeaders map[string]string, clientElicitation bool, hairpinClientPool *HairpinClientPool) (*mcp.ClientSession, error) {
	mcpPath, err := conf.Path()
	if err != nil {
		return nil, err
	}

	url := buildHairpinURL(gatewayHost, mcpPath)
	hairpinHTTPClient := hairpinClientPool.Get(conf.Hostname)
	passThroughHeaders["x-client-id"] = "lazyinit"

	base := hairpinHTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient := &http.Client{
		// the discover short circuit answers the SDK's server/discover
		// probe in-process; the hairpin costs exactly the requests it did
		// on mark3labs (initialize + notifications/initialized)
		Transport: &transport.DiscoverShortCircuit{
			Base: &transport.HeaderRoundTripper{
				Base:    base,
				Headers: passThroughHeaders,
			},
		},
	}

	streamTransport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: httpClient,
		// the router only reads ID() and Close() from this session; nothing
		// consumes server-initiated messages, so skip the standalone SSE GET
		// the SDK otherwise performs synchronously inside Connect. saves a
		// full gateway round trip on every lazy backend init (hot path).
		DisableStandaloneSSE: true,
	}

	caps := &mcp.ClientCapabilities{}
	if clientElicitation {
		caps.Elicitation = &mcp.ElicitationCapabilities{}
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "mcp-gateway",
		Version: "0.0.1",
	}, &mcp.ClientOptions{
		Capabilities: caps,
	})

	session, err := client.Connect(ctx, streamTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return session, nil
}

// BuildHairpinHTTPClientPool returns a HairpinClientPool for hairpin requests.
// For HTTPS private hosts it configures TLS with the publicHost as the default
// ServerName (SNI). Servers on a different HTTPS listener can obtain a client
// with a different SNI via pool.Get(serverHostname).
// For plain HTTP it returns a pool whose default client has no TLS.
func BuildHairpinHTTPClientPool(privateHost, publicHost, caCertPath string) (*HairpinClientPool, error) {
	if !strings.HasPrefix(strings.ToLower(privateHost), "https://") {
		return &HairpinClientPool{
			defaultClient: &http.Client{},
			clients:       make(map[string]*http.Client),
		}, nil
	}

	certPool, err := x509.SystemCertPool()
	if err != nil {
		certPool = x509.NewCertPool()
	}

	if caCertPath != "" {
		pem, err := os.ReadFile(caCertPath) //nolint:gosec // path comes from a CLI flag, not user input
		if err != nil {
			return nil, fmt.Errorf("failed to read gateway CA cert: %w", err)
		}
		if !certPool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("failed to parse gateway CA cert PEM")
		}
	}

	defaultSNI := publicHost
	if h, _, err := net.SplitHostPort(publicHost); err == nil {
		defaultSNI = h
	}

	baseCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    certPool,
		ServerName: defaultSNI,
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = baseCfg
	return &HairpinClientPool{
		defaultClient: &http.Client{Transport: t},
		baseTLSConfig: baseCfg,
		clients:       make(map[string]*http.Client),
	}, nil
}
