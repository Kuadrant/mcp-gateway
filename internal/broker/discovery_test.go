package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func TestIsBrokerTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"discover_tools is broker tool", "discover_tools", true},
		{"select_tools is broker tool", "select_tools", true},
		{"regular tool is not broker tool", "test1_greet", false},
		{"empty string is not broker tool", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBrokerTool(tt.toolName); got != tt.want {
				t.Errorf("IsBrokerTool(%q) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestSessionScope(t *testing.T) {
	store := newSessionScopeStore()

	t.Run("no scope returns nil and false", func(t *testing.T) {
		tools, exists := store.getScope("session-1")
		if tools != nil {
			t.Errorf("expected nil, got %v", tools)
		}
		if exists {
			t.Error("expected exists=false for unset scope")
		}
	})

	t.Run("set and get scope", func(t *testing.T) {
		store.setScope("session-1", []string{"tool_a", "tool_b"})
		tools, exists := store.getScope("session-1")
		if !exists {
			t.Fatal("expected exists=true")
		}
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(tools))
		}
		if tools["tool_a"] != true || tools["tool_b"] != true {
			t.Errorf("unexpected scope: %v", tools)
		}
	})

	t.Run("empty list sets scope to show all", func(t *testing.T) {
		store.setScope("session-1", []string{})
		tools, exists := store.getScope("session-1")
		if !exists {
			t.Error("expected exists=true after empty set")
		}
		if len(tools) != 0 {
			t.Errorf("expected empty scope, got %v", tools)
		}
	})

	t.Run("remove session", func(t *testing.T) {
		store.setScope("session-2", []string{"tool_c"})
		store.removeSession("session-2")
		tools, exists := store.getScope("session-2")
		if exists {
			t.Error("expected exists=false after remove")
		}
		if tools != nil {
			t.Errorf("expected nil after remove, got %v", tools)
		}
	})
}

func TestApplySessionScopeFilter(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "discover_tools"},
		{Name: "select_tools"},
		{Name: "test1_greet"},
		{Name: "test1_time"},
		{Name: "test2_hello"},
	}

	t.Run("no scope below threshold shows all tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 10, // 3 non-meta tools, under threshold
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("no scope above threshold shows only meta-tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 2, // 3 non-meta tools, above threshold
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 2 {
			t.Errorf("expected 2 meta-tools, got %d: %v", len(result), toolNames(result))
		}
		for _, name := range toolNames(result) {
			if !IsBrokerTool(name) {
				t.Errorf("unexpected non-meta tool %q in default hidden result", name)
			}
		}
	})

	t.Run("no scope at exact threshold shows all tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 3, // exactly 3 non-meta tools
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools at threshold, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("threshold 0 always hides", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 0,
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 2 {
			t.Errorf("expected 2 meta-tools with threshold 0, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("scope filters to selected tools plus meta-tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 10,
		}
		store.setScope("scoped-session", []string{"test1_greet", "test1_time"})
		result := b.applySessionScopeFilter("scoped-session", tools)
		if len(result) != 4 { // 2 selected + 2 meta-tools
			t.Errorf("expected 4 tools, got %d: %v", len(result), toolNames(result))
		}
		names := toolNames(result)
		for _, expected := range []string{"discover_tools", "select_tools", "test1_greet", "test1_time"} {
			if !containsName(names, expected) {
				t.Errorf("expected %q in result, got %v", expected, names)
			}
		}
	})

	t.Run("empty scope shows all tools regardless of threshold", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 0,
		}
		store.setScope("reset-session", []string{})
		result := b.applySessionScopeFilter("reset-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools after reset, got %d: %v", len(result), toolNames(result))
		}
	})
}

func createTestManagerWithMeta(t *testing.T, serverName, toolPrefix, category, hint string, tools []mcp.Tool) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:       serverName,
		ToolPrefix: toolPrefix,
		URL:        "http://test.local/mcp",
		Category:   category,
		Hint:       hint,
	})
	manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	manager.SetToolsForTesting(tools)
	return manager
}

func TestHandleDiscoverTools_AuthFiltering(t *testing.T) {
	tests := []struct {
		name            string
		enforceFilter   bool
		allowedTools    map[string][]string // nil = no header
		expectedServers []string
		expectedTools   map[string][]string // server -> prefixed tool names
	}{
		{
			name:          "no auth header, enforcement off — returns all",
			enforceFilter: false,
			allowedTools:  nil,
			expectedServers: []string{"server1", "server2"},
			expectedTools: map[string][]string{
				"server1": {"s1_tool_a", "s1_tool_b"},
				"server2": {"s2_tool_c"},
			},
		},
		{
			name:            "no auth header, enforcement on — returns empty",
			enforceFilter:   true,
			allowedTools:    nil,
			expectedServers: []string{},
		},
		{
			name:          "auth header allows subset",
			enforceFilter: false,
			allowedTools: map[string][]string{
				"server1": {"tool_a"},
			},
			expectedServers: []string{"server1"},
			expectedTools: map[string][]string{
				"server1": {"s1_tool_a"},
			},
		},
		{
			name:          "auth header allows tools from both servers",
			enforceFilter: false,
			allowedTools: map[string][]string{
				"server1": {"tool_b"},
				"server2": {"tool_c"},
			},
			expectedServers: []string{"server1", "server2"},
			expectedTools: map[string][]string{
				"server1": {"s1_tool_b"},
				"server2": {"s2_tool_c"},
			},
		},
		{
			name:          "auth header for unknown server — filters it out",
			enforceFilter: false,
			allowedTools: map[string][]string{
				"unknown_server": {"tool_x"},
			},
			expectedServers: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &mcpBrokerImpl{
				logger:                  slog.Default(),
				sessionScopes:           newSessionScopeStore(),
				enforceToolFilter:       tc.enforceFilter,
				trustedHeadersPublicKey: testPublicKey,
				mcpServers: map[config.UpstreamMCPID]*upstream.MCPManager{
					"server1": createTestManagerWithMeta(t, "server1", "s1_", "cat1", "hint1", []mcp.Tool{
						{Name: "tool_a"}, {Name: "tool_b"},
					}),
					"server2": createTestManagerWithMeta(t, "server2", "s2_", "cat2", "hint2", []mcp.Tool{
						{Name: "tool_c"},
					}),
				},
			}

			headers := http.Header{}
			if tc.allowedTools != nil {
				headers[authorizedToolsHeader] = []string{createTestJWT(t, tc.allowedTools)}
			}

			req := mcp.CallToolRequest{Header: headers}
			result, err := b.handleDiscoverTools(context.Background(), req)
			require.NoError(t, err)
			require.Len(t, result.Content, 1)

			var resp discoverToolsResponse
			text := result.Content[0].(mcp.TextContent)
			require.NoError(t, json.Unmarshal([]byte(text.Text), &resp))

			var serverNames []string
			for _, s := range resp.Servers {
				serverNames = append(serverNames, s.Name)
			}

			if len(tc.expectedServers) == 0 {
				require.Empty(t, resp.Servers)
				return
			}

			require.ElementsMatch(t, tc.expectedServers, serverNames)
			for _, s := range resp.Servers {
				require.ElementsMatch(t, tc.expectedTools[s.Name], s.Tools)
			}
		})
	}
}

func TestHandleSelectTools_AuthFiltering(t *testing.T) {
	tests := []struct {
		name         string
		allowedTools map[string][]string
		selectTools  []string
		expectError  bool
	}{
		{
			name:         "selecting authorized tool succeeds",
			allowedTools: map[string][]string{"server1": {"tool_a"}},
			selectTools:  []string{"s1_tool_a"},
			expectError:  false,
		},
		{
			name:         "selecting unauthorized tool fails",
			allowedTools: map[string][]string{"server1": {"tool_a"}},
			selectTools:  []string{"s1_tool_b"},
			expectError:  true,
		},
		{
			name:         "selecting mix of authorized and unauthorized fails",
			allowedTools: map[string][]string{"server1": {"tool_a"}},
			selectTools:  []string{"s1_tool_a", "s1_tool_b"},
			expectError:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mcpSrv := server.NewMCPServer("test", "1.0")
			mcpSrv.AddTools(
				server.ServerTool{Tool: mcp.Tool{Name: "s1_tool_a"}},
				server.ServerTool{Tool: mcp.Tool{Name: "s1_tool_b"}},
			)

			b := &mcpBrokerImpl{
				logger:                  slog.Default(),
				sessionScopes:           newSessionScopeStore(),
				trustedHeadersPublicKey: testPublicKey,
				listeningMCPServer:      mcpSrv,
				mcpServers: map[config.UpstreamMCPID]*upstream.MCPManager{
					"server1": createTestManagerWithMeta(t, "server1", "s1_", "cat1", "hint1", []mcp.Tool{
						{Name: "tool_a"}, {Name: "tool_b"},
					}),
				},
			}

			headers := http.Header{}
			headers[authorizedToolsHeader] = []string{createTestJWT(t, tc.allowedTools)}

			toolsArg := make([]any, len(tc.selectTools))
			for i, name := range tc.selectTools {
				toolsArg[i] = name
			}
			req := mcp.CallToolRequest{
				Header: headers,
				Params: mcp.CallToolParams{
					Arguments: map[string]any{"tools": toolsArg},
				},
			}

			// create a context with a session
			sess := &mockSession{id: "test-session"}
			require.NoError(t, mcpSrv.RegisterSession(context.Background(), sess))
			ctx := mcpSrv.WithContext(context.Background(), sess)

			_, err := b.handleSelectTools(ctx, req)
			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "not authorized")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// mockSession implements server.ClientSession for testing
type mockSession struct {
	id string
	ch chan mcp.JSONRPCNotification
}

func (m *mockSession) Initialize()         {}
func (m *mockSession) Initialized() bool   { return true }
func (m *mockSession) SessionID() string   { return m.id }
func (m *mockSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if m.ch == nil {
		m.ch = make(chan mcp.JSONRPCNotification, 10)
	}
	return m.ch
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
