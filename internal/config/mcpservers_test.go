package config

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPServer_Path(t *testing.T) {
	testCases := []struct {
		name         string
		url          string
		expectedPath string
		expectErr    bool
	}{
		{
			name:         "standard URL with path",
			url:          "http://localhost:8080/mcp",
			expectedPath: "/mcp",
			expectErr:    false,
		},
		{
			name:         "URL with custom path",
			url:          "http://localhost:8080/v1/special/mcp",
			expectedPath: "/v1/special/mcp",
			expectErr:    false,
		},
		{
			name:         "URL without path",
			url:          "http://localhost:8080",
			expectedPath: "",
			expectErr:    false,
		},
		{
			name:         "URL with trailing slash",
			url:          "http://localhost:8080/",
			expectedPath: "/",
			expectErr:    false,
		},
		{
			name:         "HTTPS URL with path",
			url:          "https://api.example.com/mcp",
			expectedPath: "/mcp",
			expectErr:    false,
		},
		{
			name:         "URL with query parameters",
			url:          "http://localhost:8080/mcp?version=1",
			expectedPath: "/mcp",
			expectErr:    false,
		},
		{
			name:         "URL with port and nested path",
			url:          "http://localhost:9000/api/v2/mcp/endpoint",
			expectedPath: "/api/v2/mcp/endpoint",
			expectErr:    false,
		},
		{
			name:         "invalid URL",
			url:          "://invalid",
			expectedPath: "",
			expectErr:    true,
		},
		{
			name:         "empty URL",
			url:          "",
			expectedPath: "",
			expectErr:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := &MCPServer{
				Name: "test",
				URL:  tc.url,
			}

			path, err := server.Path()

			if tc.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedPath, path)
			}
		})
	}
}

func TestMCPServer_ID(t *testing.T) {
	testCases := []struct {
		name       string
		server     *MCPServer
		expectedID UpstreamMCPID
	}{
		{
			name: "standard server",
			server: &MCPServer{
				Name:     "weather",
				Prefix:   "weather_",
				Hostname: "weather.mcp.local",
			},
			expectedID: UpstreamMCPID("weather:weather_:weather.mcp.local"),
		},
		{
			name: "server without prefix",
			server: &MCPServer{
				Name:     "simple",
				Prefix:   "",
				Hostname: "simple.local",
			},
			expectedID: UpstreamMCPID("simple::simple.local"),
		},
		{
			name: "external server",
			server: &MCPServer{
				Name:     "github",
				Prefix:   "gh_",
				Hostname: "api.githubcopilot.com",
			},
			expectedID: UpstreamMCPID("github:gh_:api.githubcopilot.com"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.server.ID()
			require.Equal(t, tc.expectedID, id)
		})
	}
}

func TestMCPServer_ConfigChanged(t *testing.T) {
	testCases := []struct {
		name          string
		current       *MCPServer
		existing      MCPServer
		expectChanged bool
	}{
		{
			name: "no changes",
			current: &MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: false,
		},
		{
			name: "name changed",
			current: &MCPServer{
				Name:       "server2",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "prefix changed",
			current: &MCPServer{
				Name:       "server1",
				Prefix:     "s2_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "hostname changed",
			current: &MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server2.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "credential changed",
			current: &MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "NEW_CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "URL changed does not trigger change",
			current: &MCPServer{
				Name:       "server1",
				URL:        "http://new-url/mcp",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				URL:        "http://old-url/mcp",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: false,
		},
		{
			name: "enabled changed does not trigger change",
			current: &MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
				Enabled:    true,
			},
			existing: MCPServer{
				Name:       "server1",
				Prefix:     "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
				Enabled:    false,
			},
			expectChanged: false,
		},
		{
			name: "tokenURLElicitation added",
			current: &MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{},
			},
			existing: MCPServer{
				Name:     "server1",
				Prefix:   "s1_",
				Hostname: "server1.local",
			},
			expectChanged: true,
		},
		{
			name: "tokenURLElicitation removed",
			current: &MCPServer{
				Name:     "server1",
				Prefix:   "s1_",
				Hostname: "server1.local",
			},
			existing: MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{},
			},
			expectChanged: true,
		},
		{
			name: "tokenURLElicitation URL changed",
			current: &MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{URL: "https://new.example.com"},
			},
			existing: MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{URL: "https://old.example.com"},
			},
			expectChanged: true,
		},
		{
			name: "tokenURLElicitation unchanged",
			current: &MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{},
			},
			existing: MCPServer{
				Name:                "server1",
				Prefix:              "s1_",
				Hostname:            "server1.local",
				TokenURLElicitation: &TokenURLElicitationConfig{},
			},
			expectChanged: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.current.ConfigChanged(tc.existing)
			require.Equal(t, tc.expectChanged, changed)
		})
	}
}

func TestMCPServersConfig_GetServerConfigByName(t *testing.T) {
	servers := []*MCPServer{
		{Name: "server1", URL: "http://server1/mcp"},
		{Name: "server2", URL: "http://server2/mcp"},
		{Name: "server3", URL: "http://server3/mcp"},
	}

	config := &MCPServersConfig{
		Servers: servers,
	}

	testCases := []struct {
		name       string
		serverName string
		expectErr  bool
	}{
		{
			name:       "find first server",
			serverName: "server1",
			expectErr:  false,
		},
		{
			name:       "find middle server",
			serverName: "server2",
			expectErr:  false,
		},
		{
			name:       "find last server",
			serverName: "server3",
			expectErr:  false,
		},
		{
			name:       "server not found",
			serverName: "nonexistent",
			expectErr:  true,
		},
		{
			name:       "empty server name",
			serverName: "",
			expectErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := config.GetServerConfigByName(tc.serverName)

			if tc.expectErr {
				require.Error(t, err)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.serverName, result.Name)
			}
		})
	}
}

func TestMCPServersConfig_GetServerConfigByName_EmptyServers(t *testing.T) {
	config := &MCPServersConfig{
		Servers: []*MCPServer{},
	}

	result, err := config.GetServerConfigByName("any")
	require.Nil(t, result)
	require.Error(t, err)
}

func TestMCPServersConfig_GetServerConfigByName_NilServers(t *testing.T) {
	config := &MCPServersConfig{
		Servers: nil,
	}

	result, err := config.GetServerConfigByName("any")
	require.Nil(t, result)
	require.Error(t, err)
}

// mockObserver implements Observer for testing
type mockObserver struct {
	mu           sync.Mutex
	called       bool
	receivedConf *MCPServersConfig
}

func (m *mockObserver) OnConfigChange(_ context.Context, config *MCPServersConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	m.receivedConf = config
}

func (m *mockObserver) wasCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called
}

func TestMCPServersConfig_RegisterObserver(t *testing.T) {
	config := &MCPServersConfig{}

	observer1 := &mockObserver{}
	observer2 := &mockObserver{}

	config.RegisterObserver(observer1)
	require.Len(t, config.observers, 1)

	config.RegisterObserver(observer2)
	require.Len(t, config.observers, 2)
}

func TestMCPServersConfig_Notify(t *testing.T) {
	config := &MCPServersConfig{
		Servers: []*MCPServer{
			{Name: "test"},
		},
	}

	observer := &mockObserver{}
	config.RegisterObserver(observer)

	config.Notify(context.Background())

	// wait briefly for the goroutine to execute
	require.Eventually(t, func() bool {
		return observer.wasCalled()
	}, 1000000000, 10000000) // 1s timeout, 10ms poll interval

	observer.mu.Lock()
	require.NotNil(t, observer.receivedConf)
	require.Equal(t, config, observer.receivedConf)
	observer.mu.Unlock()
}

func TestMultiProtocolConfigModel(t *testing.T) {
	t.Run("MCPServer implements UpstreamServer", func(t *testing.T) {
		mcpServer := &MCPServer{
			Name:     "weather",
			URL:      "http://weather-service/mcp",
			Hostname: "weather.mcp.local",
			Prefix:   "weather_",
			Enabled:  true,
		}

		var upstream UpstreamServer = mcpServer

		require.Equal(t, "weather", upstream.GetName())
		require.Equal(t, ProtocolMCP, upstream.GetProtocol())
		require.Equal(t, "http://weather-service/mcp", upstream.GetURL())
		require.Equal(t, "weather.mcp.local", upstream.GetHostname())
		require.Equal(t, "weather_", upstream.GetPrefix())
		require.True(t, upstream.IsEnabled())
		require.Equal(t, UpstreamID("weather:weather_:weather.mcp.local"), upstream.GetID())
	})

	t.Run("A2AServer implements UpstreamServer", func(t *testing.T) {
		a2aServer := &A2AServer{
			Name:            "agent1",
			URL:             "http://agent-service/a2a",
			Hostname:        "agent.a2a.local",
			Prefix:          "agent_",
			Enabled:         true,
			AgentID:         "agent-xyz-987",
			AgentCardURL:    "http://agent-service/card.json",
			TaskEndpoint:    "http://agent-service/tasks",
			ProtocolBinding: "http-sse",
			Metadata:        map[string]string{"version": "1.0"},
		}

		var upstream UpstreamServer = a2aServer

		require.Equal(t, "agent1", upstream.GetName())
		require.Equal(t, ProtocolA2A, upstream.GetProtocol())
		require.Equal(t, "http://agent-service/a2a", upstream.GetURL())
		require.Equal(t, "agent.a2a.local", upstream.GetHostname())
		require.Equal(t, "agent_", upstream.GetPrefix())
		require.True(t, upstream.IsEnabled())
		require.Equal(t, UpstreamID("agent1:agent_:agent.a2a.local"), upstream.GetID())

		// Verify A2A-specific fields are correctly accessible on concrete struct
		require.Equal(t, "agent-xyz-987", a2aServer.AgentID)
		require.Equal(t, "http://agent-service/card.json", a2aServer.AgentCardURL)
		require.Equal(t, "http://agent-service/tasks", a2aServer.TaskEndpoint)
		require.Equal(t, "http-sse", a2aServer.ProtocolBinding)
		require.Equal(t, "1.0", a2aServer.Metadata["version"])
	})

	t.Run("MCPServersConfig implements UpstreamRegistry", func(t *testing.T) {
		servers := []*MCPServer{
			{Name: "server1", URL: "http://server1/mcp", Enabled: true},
			{Name: "server2", URL: "http://server2/mcp", Enabled: false},
		}

		mcpConfig := &MCPServersConfig{
			Servers:                    servers,
			MCPGatewayExternalHostname: "gateway.public.host",
		}

		var registry UpstreamRegistry = mcpConfig

		require.Equal(t, "gateway.public.host", registry.GetExternalHostname())

		upstreams := registry.ListUpstreams()
		require.Len(t, upstreams, 2)
		require.Equal(t, "server1", upstreams[0].GetName())
		require.Equal(t, "server2", upstreams[1].GetName())

		upstream1, err := registry.GetUpstreamByName("server1")
		require.NoError(t, err)
		require.Equal(t, "server1", upstream1.GetName())
		require.True(t, upstream1.IsEnabled())

		_, err = registry.GetUpstreamByName("nonexistent")
		require.Error(t, err)
	})
}

