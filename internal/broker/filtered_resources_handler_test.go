package broker

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const testPublicKeyResources = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7WdMdvC8hviEAL4wcebqaYbLEtVO
VEiyi/nozagw7BaWXmzbOWyy95gZLirTkhUb1P4Z4lgKLU2rD5NCbGPHAA==
-----END PUBLIC KEY-----`

func TestFilterResources(t *testing.T) {
	logger := slog.Default()
	ctx := context.Background()

	testCases := []struct {
		name            string
		jwtClaim        string
		resources       []*mcp.Resource
		serverNames     map[string]string // server name -> prefix
		expectedCount   int
		expectedAuthors []string
		enforceFilter   bool
	}{
		{
			name:     "no JWT claim allows all resources",
			jwtClaim: "",
			resources: []*mcp.Resource{
				{URI: "ui://app_example.com/file.html", Name: "file", Description: "test"},
				{URI: "file://local_localhost/data.txt", Name: "data", Description: "test"},
			},
			serverNames: map[string]string{
				"server1": "app",
				"server2": "local",
			},
			expectedCount:   2,
			expectedAuthors: []string{"app_example.com", "local_localhost"},
		},
		{
			name:     "empty resources claim denies all",
			jwtClaim: createTestResourcesJWT(t, map[string][]string{}),
			resources: []*mcp.Resource{
				{URI: "ui://app_example.com/file.html", Name: "file", Description: "test"},
			},
			serverNames: map[string]string{
				"server1": "app",
			},
			expectedCount: 0,
		},
		{
			name: "JWT filters by authority per server",
			jwtClaim: createTestResourcesJWT(t, map[string][]string{
				"server1": {"example.com"},
				"server2": {"localhost"},
			}),
			resources: []*mcp.Resource{
				{URI: "ui://app_example.com/file.html", Name: "file1", Description: "test"},
				{URI: "ui://app_example.com/other.html", Name: "file2", Description: "test"},
				{URI: "file://local_localhost/data.txt", Name: "data", Description: "test"},
				{URI: "file://local_localhost/backup.txt", Name: "backup", Description: "test"},
			},
			serverNames: map[string]string{
				"server1": "app",
				"server2": "local",
			},
			expectedCount:   4,
			expectedAuthors: []string{"app_example.com", "app_example.com", "local_localhost", "local_localhost"},
		},
		{
			name: "JWT denies unauthorized authority",
			jwtClaim: createTestResourcesJWT(t, map[string][]string{
				"server1": {"example.com"},
			}),
			resources: []*mcp.Resource{
				{URI: "ui://app_example.com/file.html", Name: "file", Description: "test"},
				{URI: "ui://app_other.example.com/file.html", Name: "file2", Description: "test"},
			},
			serverNames: map[string]string{
				"server1": "app",
			},
			expectedCount:   1,
			expectedAuthors: []string{"app_example.com"},
		},
		{
			name:     "enforce filter with no JWT denies all",
			jwtClaim: "",
			resources: []*mcp.Resource{
				{URI: "ui://app.example.com/file.html", Name: "file", Description: "test"},
			},
			serverNames: map[string]string{
				"server1": "app",
			},
			expectedCount: 0,
			enforceFilter: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			upstreams := make(map[config.UpstreamMCPID]upstream.ActiveMCPServer)
			for serverName, prefix := range tc.serverNames {
				upstreams[config.UpstreamMCPID(serverName)] = &mockResourceServer{
					name:   serverName,
					prefix: prefix,
				}
			}

			broker := &mcpBrokerImpl{
				logger:                  logger,
				mcpServers:              upstreams,
				enforceCapabilityFilter: tc.enforceFilter,
				trustedHeadersPublicKey: testPublicKeyResources,
			}

			headers := http.Header{}
			if tc.jwtClaim != "" {
				headers.Set("X-Mcp-Authorized", tc.jwtClaim)
			}

			result := &mcp.ListResourcesResult{
				Resources: tc.resources,
			}

			broker.FilterResources(ctx, headers, result)

			require.Equal(t, tc.expectedCount, len(result.Resources), "resource count mismatch")

			if tc.expectedCount > 0 {
				for i, expected := range tc.expectedAuthors {
					require.Less(t, i, len(result.Resources), "unexpected length")
					actual := routing.ResourceAuthority(result.Resources[i].URI)
					require.Equal(t, expected, actual, "authority mismatch at index %d", i)
				}
			}
		})
	}
}


type mockResourceServer struct {
	name   string
	prefix string
}

func (m *mockResourceServer) Stop()           {}
func (m *mockResourceServer) MCPName() string { return "mock" }
func (m *mockResourceServer) GetStatus() upstream.ServerValidationStatus {
	return upstream.ServerValidationStatus{}
}
func (m *mockResourceServer) GetManagedTools() []mcp.Tool           { return nil }
func (m *mockResourceServer) GetServedManagedTool(string) *mcp.Tool { return nil }
func (m *mockResourceServer) GetToolHints(string) (upstream.ToolHints, bool) {
	return upstream.ToolHints{}, false
}
func (m *mockResourceServer) GetManagedPrompts() []mcp.Prompt           { return nil }
func (m *mockResourceServer) GetServedManagedPrompt(string) *mcp.Prompt { return nil }
func (m *mockResourceServer) Config() config.MCPServer {
	return config.MCPServer{Name: m.name, Prefix: m.prefix}
}
func (m *mockResourceServer) SupportedVersions() []string { return nil }
func (m *mockResourceServer) SupportsVersion(string) bool { return false }
func (m *mockResourceServer) ToolsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *mockResourceServer) PromptsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *mockResourceServer) SupportsResources() bool { return false }
func (m *mockResourceServer) ListResources(context.Context) (*mcp.ListResourcesResult, error) {
	return &mcp.ListResourcesResult{}, nil
}

func createTestResourcesJWT(t *testing.T, allowedResources map[string][]string) string {
	t.Helper()
	return createTestJWTWithCapabilities(t, map[string]map[string][]string{
		"resources": allowedResources,
	})
}
