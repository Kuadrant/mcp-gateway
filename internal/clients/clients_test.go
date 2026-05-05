/*
Package clients provides a set of clients for use with the gateway code
*/
package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestInitialize_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch req["method"] {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "test-server",
						"version": "1.0.0",
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	gatewayHost := strings.TrimPrefix(server.URL, "http://")
	conf := &config.MCPServer{
		Name:       "test-server",
		ToolPrefix: "test_",
		Hostname:   "test.mcp.local",
		URL:        "http://example.com/mcp",
	}

	c, err := Initialize(context.Background(), gatewayHost, "router-key-123", conf, map[string]string{}, false)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.True(t, c.IsInitialized())
	defer c.Close()
}
