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
				Name:       "weather",
				ToolPrefix: "weather_",
				Hostname:   "weather.mcp.local",
			},
			expectedID: UpstreamMCPID("weather:weather_:weather.mcp.local"),
		},
		{
			name: "server without prefix",
			server: &MCPServer{
				Name:       "simple",
				ToolPrefix: "",
				Hostname:   "simple.local",
			},
			expectedID: UpstreamMCPID("simple::simple.local"),
		},
		{
			name: "external server",
			server: &MCPServer{
				Name:       "github",
				ToolPrefix: "gh_",
				Hostname:   "api.githubcopilot.com",
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
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: false,
		},
		{
			name: "name changed",
			current: &MCPServer{
				Name:       "server2",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "tool prefix changed",
			current: &MCPServer{
				Name:       "server1",
				ToolPrefix: "s2_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "hostname changed",
			current: &MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server2.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: true,
		},
		{
			name: "credential changed",
			current: &MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "NEW_CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
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
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			existing: MCPServer{
				Name:       "server1",
				URL:        "http://old-url/mcp",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
			},
			expectChanged: false,
		},
		{
			name: "enabled changed does not trigger change",
			current: &MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
				Enabled:    true,
			},
			existing: MCPServer{
				Name:       "server1",
				ToolPrefix: "s1_",
				Hostname:   "server1.local",
				Credential: "CRED_VAR",
				Enabled:    false,
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
