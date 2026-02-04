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
				Name:       "test-server",
				ToolPrefix: "test_",
				Hostname:   "test.mcp.local",
			},
			passThroughHeaders: map[string]string{},
			expectedError:      true,
		},
		// TODO: Register a mock server to test successful initialization
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := Initialize(context.Background(), tc.gatewayHost, tc.routerKey, tc.conf, tc.passThroughHeaders)
			if tc.expectedError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, client)
		})
	}
}
