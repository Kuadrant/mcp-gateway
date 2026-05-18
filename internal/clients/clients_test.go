/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
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
		routerKey          string
		conf               *config.MCPServer
		passThroughHeaders map[string]string
		expectedError      bool
	}{
		{
			name:        "standard initialization",
			gatewayHost: "%invalid",
			routerKey:   "router-key-123",
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
			client, err := Initialize(context.Background(), tc.gatewayHost, tc.routerKey, tc.conf, tc.passThroughHeaders, false)
			if tc.expectedError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)
		})
	}
}
