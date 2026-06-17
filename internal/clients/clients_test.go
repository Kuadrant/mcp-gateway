/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildHairpinURL(t *testing.T) {
	tests := []struct {
		name        string
		gatewayHost string
		mcpPath     string
		want        string
	}{
		{
			name:        "bare host gets http:// for backwards compatibility",
			gatewayHost: "mcp-gateway-istio.gateway-system.svc.cluster.local:8080",
			mcpPath:     "/mcp",
			want:        "http://mcp-gateway-istio.gateway-system.svc.cluster.local:8080/mcp",
		},
		{
			name:        "https scheme prefix is preserved (HTTPS listener case, issue #917)",
			gatewayHost: "https://mcp-gateway-istio.gateway-system.svc.cluster.local:443",
			mcpPath:     "/mcp",
			want:        "https://mcp-gateway-istio.gateway-system.svc.cluster.local:443/mcp",
		},
		{
			name:        "explicit http:// scheme prefix is preserved",
			gatewayHost: "http://my-internal-host:8081",
			mcpPath:     "/mcp",
			want:        "http://my-internal-host:8081/mcp",
		},
		{
			name:        "custom path is appended",
			gatewayHost: "https://mcp-gw.example.com:443",
			mcpPath:     "/v1/special/mcp",
			want:        "https://mcp-gw.example.com:443/v1/special/mcp",
		},
		{
			name:        "uppercase scheme is recognized and not double-prefixed",
			gatewayHost: "HTTPS://mcp-gateway-istio.gateway-system.svc.cluster.local:443",
			mcpPath:     "/mcp",
			want:        "HTTPS://mcp-gateway-istio.gateway-system.svc.cluster.local:443/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildHairpinURL(tt.gatewayHost, tt.mcpPath)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInitialize(t *testing.T) {
	testCases := []struct {
		name               string
		gatewayHost        string
		conf               *config.MCPServer
		passThroughHeaders map[string]string
		expectedError      bool
	}{
		{
			name:        "standard initialization",
			gatewayHost: "%invalid",
			conf: &config.MCPServer{
				Name:     "test-server",
				Prefix:   "test_",
				Hostname: "test.mcp.local",
			},
			passThroughHeaders: map[string]string{},
			expectedError:      true,
		},
		// TODO: Register a mock server to test successful initialization
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pool := &HairpinClientPool{
				defaultClient: &http.Client{},
				clients:       make(map[string]*http.Client),
			}
			client, err := Initialize(context.Background(), tc.gatewayHost, tc.conf, tc.passThroughHeaders, false, pool)
			if tc.expectedError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)
		})
	}
}

func TestBuildHairpinHTTPClientPool(t *testing.T) {
	t.Run("returns plain pool for HTTP private host", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("http://gw.svc:8080", "mcp.example.com", "")
		require.NoError(t, err)
		require.NotNil(t, pool)
		require.Nil(t, pool.baseTLSConfig)
		require.NotNil(t, pool.Get(""))
	})

	t.Run("returns plain pool for bare host without scheme", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("gw.svc:8080", "mcp.example.com", "")
		require.NoError(t, err)
		require.NotNil(t, pool)
		require.Nil(t, pool.baseTLSConfig)
	})

	t.Run("HTTPS sets default ServerName and TLS minimum version", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", "")
		require.NoError(t, err)
		require.NotNil(t, pool)

		c := pool.Get("")
		tr, ok := c.Transport.(*http.Transport)
		require.True(t, ok)
		require.Equal(t, "mcp.example.com", tr.TLSClientConfig.ServerName)
		require.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
	})

	t.Run("HTTPS strips port from publicHost for ServerName", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com:8443", "")
		require.NoError(t, err)

		c := pool.Get("")
		tr, ok := c.Transport.(*http.Transport)
		require.True(t, ok)
		require.Equal(t, "mcp.example.com", tr.TLSClientConfig.ServerName)
	})

	t.Run("Get with override returns client with different SNI", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", "")
		require.NoError(t, err)

		defaultClient := pool.Get("")
		overrideClient := pool.Get("server.mcp-alt.local")
		require.NotEqual(t, defaultClient, overrideClient)

		tr, ok := overrideClient.Transport.(*http.Transport)
		require.True(t, ok)
		require.Equal(t, "server.mcp-alt.local", tr.TLSClientConfig.ServerName)
	})

	t.Run("Get with override strips port", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", "")
		require.NoError(t, err)

		c := pool.Get("server.mcp-alt.local:8443")
		tr, ok := c.Transport.(*http.Transport)
		require.True(t, ok)
		require.Equal(t, "server.mcp-alt.local", tr.TLSClientConfig.ServerName)
	})

	t.Run("Get with override is cached", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", "")
		require.NoError(t, err)

		c1 := pool.Get("server.mcp-alt.local")
		c2 := pool.Get("server.mcp-alt.local")
		require.Same(t, c1, c2)
	})

	t.Run("Get with override on HTTP pool returns default", func(t *testing.T) {
		pool, err := BuildHairpinHTTPClientPool("http://gw.svc:8080", "mcp.example.com", "")
		require.NoError(t, err)

		defaultClient := pool.Get("")
		overrideClient := pool.Get("server.mcp-alt.local")
		require.Same(t, defaultClient, overrideClient)
	})

	t.Run("errors on non-existent CA cert path", func(t *testing.T) {
		_, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", "/nonexistent/ca.crt")
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read gateway CA cert")
	})

	t.Run("errors on invalid PEM content", func(t *testing.T) {
		tmpDir := t.TempDir()
		badCert := filepath.Join(tmpDir, "bad.crt")
		require.NoError(t, os.WriteFile(badCert, []byte("not a certificate"), 0600))

		_, err := BuildHairpinHTTPClientPool("https://gw.svc:443", "mcp.example.com", badCert)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse gateway CA cert PEM")
	})
}
