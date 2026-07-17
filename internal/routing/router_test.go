package routing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"k8s.io/utils/ptr"

	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/clients"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const testSigningKey = "test-signing-key-must-be-at-least-32-bytes"

// newTestRouter creates a Router202511 for testing
func newTestRouter(t *testing.T, serverConfigs []*config.MCPServer, toolMap map[string]string, promptMap map[string]string) (*Router202511, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	jwtManager, err := session.NewJWTManager(testSigningKey, 0, logger, cache)
	require.NoError(t, err)

	validToken := jwtManager.Generate()

	builder := NewTableBuilder()
	for tool, svrName := range toolMap {
		for _, svr := range serverConfigs {
			if svr.Name == svrName {
				path, _ := svr.Path()
				route := &ServerRoute{
					Name:             svr.Name,
					Host:             svr.Hostname,
					Prefix:           svr.Prefix,
					Path:             path,
					URL:              svr.URL,
					UserSpecificList: svr.UserSpecificList,
				}
				if svr.TokenURLElicitation != nil {
					route.TokenURLElicitation = &TokenURLElicitationRoute{
						URL: svr.TokenURLElicitation.URL,
					}
				}
				builder.AddTool(tool, route)
			}
		}
	}
	for prompt, svrName := range promptMap {
		for _, svr := range serverConfigs {
			if svr.Name == svrName {
				path, _ := svr.Path()
				builder.AddPrompt(prompt, &ServerRoute{
					Name:   svr.Name,
					Host:   svr.Hostname,
					Prefix: svr.Prefix,
					Path:   path,
					URL:    svr.URL,
				})
			}
		}
	}
	table := builder.Build()

	routingConfig := atomic.Pointer[config.MCPServersConfig]{}
	routingConfig.Store(&config.MCPServersConfig{Servers: serverConfigs})

	tokenElicitationMap, err := elicitation.New()
	require.NoError(t, err)

	router := &Router202511{
		RoutingConfig:       &routingConfig,
		Table:               func() RoutingTable { return table },
		SessionCache:        cache,
		JWTManager:          jwtManager,
		ElicitationMap:      mustNewIDMap(t),
		TokenElicitationMap: tokenElicitationMap,
		Logger:              logger,
	}

	return router, validToken
}

func TestMCPRequestValid(t *testing.T) {

	testCases := []struct {
		Name      string
		Input     *MCPRequest
		ExpectErr error
	}{
		{
			Name: "test with valid request",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "initialize",
				Params:  map[string]any{},
				ID:      ptr.To(2),
			},
			ExpectErr: nil,
		},
		{
			Name: "test with valid notification request",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "notifications/initialize",
				Params:  map[string]any{},
			},
			ExpectErr: nil,
		},
		{
			Name: "test with invalid version",
			Input: &MCPRequest{
				JSONRPC: "1.0",
				Method:  "initialize",
				Params:  map[string]any{},
				ID:      ptr.To(2),
			},
			ExpectErr: ErrInvalidRequest,
		},
		{
			Name: "test with invalid method",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "",
				Params:  map[string]any{},
				ID:      ptr.To(2),
			},
			ExpectErr: ErrInvalidRequest,
		},
		{
			Name: "test with missing id  for none notification call",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Params:  map[string]any{},
			},
			ExpectErr: ErrInvalidRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			valid, err := tc.Input.Validate()
			if tc.ExpectErr != nil {
				if err == nil {
					t.Fatalf("expected an error but got none")
				}
				if valid {
					t.Fatalf("mcp request should not have been marked valid")
				}
			} else {
				if !valid {
					t.Fatalf("expected the mcp request to be valid")
				}
			}

		})
	}
}

func TestMCPRequestToolName(t *testing.T) {
	testCases := []struct {
		Name       string
		Input      *MCPRequest
		ExpectTool string
	}{
		{
			Name: "test with valid request",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Params: map[string]any{
					"name": "test_tool",
				},
			},
			ExpectTool: "test_tool",
		},
		{
			Name: "test with no tool",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Params: map[string]any{
					"name": "",
				},
			},
			ExpectTool: "",
		},
		{
			Name: "test with not a tool call",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "initialise",
				Params: map[string]any{
					"name": "test",
				},
			},
			ExpectTool: "",
		},
		{
			Name: "test with not a tool call",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "initialise",
				Params: map[string]any{
					"name": 2,
				},
			},
			ExpectTool: "",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Input.ToolName() != tc.ExpectTool {
				t.Fatalf("expected mcp request tool call to have tool %s but got %s", tc.ExpectTool, tc.Input.ToolName())
			}
		})
	}
}

func TestHandleRequestBody(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Create session cache
	cache, err := session.NewCache()
	require.NoError(t, err)

	// Create JWT manager for test
	jwtManager, err := session.NewJWTManager(testSigningKey, 0, logger, cache)
	require.NoError(t, err)

	// Generate a valid JWT token
	validToken := jwtManager.Generate()

	// Pre-populate the session cache so InitForClient won't be called
	// This simulates the case where the session already exists
	sessionAdded, err := cache.AddSession(context.Background(), validToken, "dummy", "mock-upstream-session-id", 0)
	require.NoError(t, err)
	require.True(t, sessionAdded)

	// Mock InitForClient - should not be called since session exists
	mockInitForClient := func(_ context.Context, _ string, _ *config.MCPServer, _ map[string]string, _ bool, _ *clients.HairpinClientPool) (*mcp.ClientSession, error) {
		// This should not be called in this test since session exists in cache
		return nil, fmt.Errorf("InitForClient should not be called when session exists")
	}

	serverConfigs := []*config.MCPServer{
		{
			Name:     "dummy",
			URL:      "http://localhost:8080/mcp",
			Prefix:   "s_",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	table := NewTableBuilder().
		AddTool("s_mytool", &ServerRoute{
			Name:   "dummy",
			Host:   "localhost",
			Prefix: "s_",
			Path:   "/mcp",
			URL:    "http://localhost:8080/mcp",
		}).
		Build()

	routingConfig := atomic.Pointer[config.MCPServersConfig]{}
	routingConfig.Store(&config.MCPServersConfig{Servers: serverConfigs})

	router := &Router202511{
		RoutingConfig: &routingConfig,
		Table:         func() RoutingTable { return table },
		SessionCache:  cache,
		JWTManager:    jwtManager,
		InitForClient: mockInitForClient,
		Logger:        logger,
	}

	data := &MCPRequest{
		ID:      ptr.To(0),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]any{
			"name":  "s_mytool",
			"other": "other",
		},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}

	decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
	require.Nil(t, decision.Error)
	require.Equal(t, "localhost", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)

	require.Equal(t, "tools/call", decision.SetHeaders["x-mcp-method"])
	require.Equal(t, "mytool", decision.SetHeaders["x-mcp-toolname"])
	require.Equal(t, "dummy", decision.SetHeaders["x-mcp-servername"])
	require.Equal(t, "mock-upstream-session-id", decision.SetHeaders["mcp-session-id"])

	// internal-only headers must be stripped before forwarding to upstream
	require.Contains(t, decision.UnsetHeaders, "x-mcp-authorized")
	require.Contains(t, decision.UnsetHeaders, "x-mcp-virtualserver")

	require.Equal(t,
		`{"id":0,"jsonrpc":"2.0","method":"tools/call","params":{"name":"mytool","other":"other"}}`,
		string(decision.BodyMutation))
}

func TestMCPRequest_isNotificationRequest(t *testing.T) {
	testCases := []struct {
		name     string
		method   string
		expected bool
	}{
		{
			name:     "notifications/initialized is notification",
			method:   "notifications/initialized",
			expected: true,
		},
		{
			name:     "notifications/cancelled is notification",
			method:   "notifications/cancelled",
			expected: true,
		},
		{
			name:     "notifications/progress is notification",
			method:   "notifications/progress",
			expected: true,
		},
		{
			name:     "tools/call is not notification",
			method:   "tools/call",
			expected: false,
		},
		{
			name:     "initialize is not notification",
			method:   "initialize",
			expected: false,
		},
		{
			name:     "tools/list is not notification",
			method:   "tools/list",
			expected: false,
		},
		{
			name:     "empty method is not notification",
			method:   "",
			expected: false,
		},
		{
			name:     "partial notification prefix is not notification",
			method:   "notification",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{Method: tc.method}
			result := req.IsNotificationRequest()
			require.Equal(t, tc.expected, result, "method %q should return %v", tc.method, tc.expected)
		})
	}
}

func TestMCPRequest_isToolCall(t *testing.T) {
	testCases := []struct {
		name     string
		method   string
		expected bool
	}{
		{
			name:     "tools/call is tool call",
			method:   "tools/call",
			expected: true,
		},
		{
			name:     "tools/list is not tool call",
			method:   "tools/list",
			expected: false,
		},
		{
			name:     "initialize is not tool call",
			method:   "initialize",
			expected: false,
		},
		{
			name:     "empty method is not tool call",
			method:   "",
			expected: false,
		},
		{
			name:     "TOOLS/CALL uppercase is not tool call",
			method:   "TOOLS/CALL",
			expected: false,
		},
		{
			name:     "tools/call with extra chars is not tool call",
			method:   "tools/call/extra",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{Method: tc.method}
			result := req.IsToolCall()
			require.Equal(t, tc.expected, result, "method %q should return %v", tc.method, tc.expected)
		})
	}
}

func TestMCPRequest_isInitializeRequest(t *testing.T) {
	testCases := []struct {
		name     string
		method   string
		expected bool
	}{
		{
			name:     "initialize is init request",
			method:   "initialize",
			expected: true,
		},
		{
			name:     "notifications/initialized is init request",
			method:   "notifications/initialized",
			expected: true,
		},
		{
			name:     "tools/call is not init request",
			method:   "tools/call",
			expected: false,
		},
		{
			name:     "tools/list is not init request",
			method:   "tools/list",
			expected: false,
		},
		{
			name:     "empty method is not init request",
			method:   "",
			expected: false,
		},
		{
			name:     "INITIALIZE uppercase is not init request",
			method:   "INITIALIZE",
			expected: false,
		},
		{
			name:     "initialized alone is not init request",
			method:   "initialized",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{Method: tc.method}
			result := req.IsInitializeRequest()
			require.Equal(t, tc.expected, result, "method %q should return %v", tc.method, tc.expected)
		})
	}
}

func TestMCPRequest_GetSessionID(t *testing.T) {
	testCases := []struct {
		name       string
		headers    map[string]string
		presetID   string
		expectedID string
	}{
		{
			name:       "returns cached session ID",
			headers:    nil,
			presetID:   "cached-session-id",
			expectedID: "cached-session-id",
		},
		{
			name: "extracts session ID from headers",
			headers: map[string]string{
				"mcp-session-id": "header-session-id",
			},
			presetID:   "",
			expectedID: "header-session-id",
		},
		{
			name:       "returns empty when no headers and no cached ID",
			headers:    nil,
			presetID:   "",
			expectedID: "",
		},
		{
			name: "returns empty when header not present",
			headers: map[string]string{
				"other-header": "value",
			},
			presetID:   "",
			expectedID: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{
				Headers:   tc.headers,
				SessionID: tc.presetID,
			}
			result := req.GetSessionID()
			require.Equal(t, tc.expectedID, result)
		})
	}
}

func TestValidateSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	jwtManager, err := session.NewJWTManager(testSigningKey, 0, logger, cache)
	require.NoError(t, err)

	validToken := jwtManager.Generate()

	router := &Router202511{
		JWTManager: jwtManager,
		Logger:     logger,
	}

	t.Run("valid session", func(t *testing.T) {
		require.Nil(t, router.validateSession(validToken))
	})

	t.Run("empty session ID", func(t *testing.T) {
		routerErr := router.validateSession("")
		require.NotNil(t, routerErr)
		require.Equal(t, int32(400), routerErr.Code())
	})

	t.Run("invalid JWT", func(t *testing.T) {
		routerErr := router.validateSession("invalid-jwt-token")
		require.NotNil(t, routerErr)
		require.Equal(t, int32(401), routerErr.Code())
	})
}

func TestRouteRequest_PrefixFallback(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:             "github",
			URL:              "http://github.mcp:8080/mcp",
			Prefix:           "gh_",
			State:            "Enabled",
			Hostname:         "github.mcp",
			UserSpecificList: true,
		},
	}

	router, validToken := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{})

	// register only a prefix, no direct tool entry
	table := NewTableBuilder().
		AddPrefix("gh_", &ServerRoute{
			Name:             "github",
			Host:             "github.mcp",
			Prefix:           "gh_",
			Path:             "/mcp",
			URL:              "http://github.mcp:8080/mcp",
			UserSpecificList: true,
		}).
		Build()
	router.Table = func() RoutingTable { return table }

	// pre-populate session so InitForClient is skipped
	_, err := router.SessionCache.AddSession(context.Background(), validToken, "github", "upstream-session-123", 0)
	require.NoError(t, err)

	req := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  map[string]any{"name": "gh_user_specific_tool"},
		Headers: map[string]string{"mcp-session-id": validToken},
	}

	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)
	require.Equal(t, "github.mcp", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)
	require.Equal(t, "user_specific_tool", decision.SetHeaders["x-mcp-toolname"])
	require.Equal(t, "github", decision.SetHeaders["x-mcp-servername"])
	require.Equal(t, "upstream-session-123", decision.SetHeaders["mcp-session-id"])
	require.Contains(t, decision.UnsetHeaders, "x-mcp-authorized")
}

func TestMCPRequest_ReWriteToolName(t *testing.T) {
	req := &MCPRequest{
		Params: map[string]any{
			"name":      "prefix_original_tool",
			"arguments": map[string]any{"key": "value"},
		},
	}

	req.ReWriteToolName("original_tool")

	require.Equal(t, "original_tool", req.Params["name"])
	// other params should be unchanged
	require.Equal(t, map[string]any{"key": "value"}, req.Params["arguments"])
}

func TestMCPRequest_ToBytes(t *testing.T) {
	req := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "test_tool",
		},
	}

	bytes, err := req.ToBytes()
	require.NoError(t, err)
	require.Contains(t, string(bytes), `"jsonrpc":"2.0"`)
	require.Contains(t, string(bytes), `"method":"tools/call"`)
	require.Contains(t, string(bytes), `"name":"test_tool"`)
}

func TestMCPRequest_GetSingleHeaderValue(t *testing.T) {
	req := &MCPRequest{
		Headers: map[string]string{
			"content-type":    "application/json",
			"x-custom-header": "custom-value",
		},
	}

	require.Equal(t, "application/json", req.GetSingleHeaderValue("content-type"))
	require.Equal(t, "custom-value", req.GetSingleHeaderValue("x-custom-header"))
	require.Equal(t, "", req.GetSingleHeaderValue("nonexistent"))
}

func TestRouterError(t *testing.T) {
	t.Run("Error returns message", func(t *testing.T) {
		err := NewRouterError(500, fmt.Errorf("internal error"))
		require.Equal(t, "internal error", err.Error())
	})

	t.Run("Error returns status when no error", func(t *testing.T) {
		err := &RouterError{StatusCode: 404, Err: nil}
		require.Equal(t, "router error: status 404", err.Error())
	})

	t.Run("Code returns status code", func(t *testing.T) {
		err := NewRouterError(400, fmt.Errorf("bad request"))
		require.Equal(t, int32(400), err.Code())
	})

	t.Run("Unwrap returns underlying error", func(t *testing.T) {
		underlying := fmt.Errorf("underlying error")
		err := NewRouterError(500, underlying)
		require.Equal(t, underlying, err.Unwrap())
	})

	t.Run("NewRouterErrorf formats message", func(t *testing.T) {
		err := NewRouterErrorf(400, "invalid %s: %d", "value", 42)
		require.Equal(t, "invalid value: 42", err.Error())
		require.Equal(t, int32(400), err.Code())
	})
}

func TestMCPRequest_isElicitationResponse(t *testing.T) {
	testCases := []struct {
		name     string
		req      *MCPRequest
		expected bool
	}{
		{
			name: "accept action is elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
				Result:  map[string]any{"action": "accept", "content": map[string]any{"name": "value"}},
			},
			expected: true,
		},
		{
			name: "decline action is elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-456",
				Result:  map[string]any{"action": "decline"},
			},
			expected: true,
		},
		{
			name: "cancel action is elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-789",
				Result:  map[string]any{"action": "cancel"},
			},
			expected: true,
		},
		{
			name: "method set means not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Result:  map[string]any{"action": "accept"},
			},
			expected: false,
		},
		{
			name: "nil result is not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
			},
			expected: false,
		},
		{
			name: "empty result is not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
				Result:  map[string]any{},
			},
			expected: false,
		},
		{
			name: "missing action key is not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
				Result:  map[string]any{"content": map[string]any{"name": "value"}},
			},
			expected: false,
		},
		{
			name: "unknown action value is not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
				Result:  map[string]any{"action": "unknown"},
			},
			expected: false,
		},
		{
			name: "non-string action is not elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-123",
				Result:  map[string]any{"action": 42},
			},
			expected: false,
		},
		{
			name: "empty method with valid action is elicitation response",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "",
				ID:      "gw-123",
				Result:  map[string]any{"action": "accept"},
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.req.IsElicitationResponse()
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestMCPRequestValid_ElicitationResponse(t *testing.T) {
	testCases := []struct {
		name      string
		req       *MCPRequest
		expectErr error
	}{
		{
			name: "valid elicitation response with accept",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-uuid-123",
				Result:  map[string]any{"action": "accept"},
			},
			expectErr: nil,
		},
		{
			name: "valid elicitation response with decline",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-uuid-456",
				Result:  map[string]any{"action": "decline"},
			},
			expectErr: nil,
		},
		{
			name: "valid elicitation response with cancel",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-uuid-789",
				Result:  map[string]any{"action": "cancel"},
			},
			expectErr: nil,
		},
		{
			name: "empty method without result is invalid",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-uuid-123",
			},
			expectErr: ErrInvalidRequest,
		},
		{
			name: "empty method with unknown action is invalid",
			req: &MCPRequest{
				JSONRPC: "2.0",
				ID:      "gw-uuid-123",
				Result:  map[string]any{"action": "unknown"},
			},
			expectErr: ErrInvalidRequest,
		},
		{
			name: "elicitation response without ID is invalid",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Result:  map[string]any{"action": "accept"},
			},
			expectErr: ErrInvalidRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			valid, err := tc.req.Validate()
			if tc.expectErr != nil {
				require.Error(t, err)
				require.False(t, valid)
			} else {
				require.NoError(t, err)
				require.True(t, valid)
			}
		})
	}
}

func TestHandleElicitationResponse(t *testing.T) {
	t.Run("routes elicitation response to correct backend", func(t *testing.T) {
		serverConfigs := []*config.MCPServer{
			{
				Name:     "weather-server",
				URL:      "http://weather.mcp.local:8080/mcp",
				Prefix:   "weather_",
				State:    "Enabled",
				Hostname: "weather.mcp.local",
			},
		}

		router, validToken := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{})

		gatewayID := mustStoreIDMap(t, router.ElicitationMap, float64(42), "weather-server", "backend-session-abc", validToken)

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept", "content": map[string]any{"name": "test"}},
			Headers: map[string]string{
				"mcp-session-id": validToken,
			},
		}

		decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
		require.Nil(t, decision.Error)

		// verify authority is set to backend hostname
		require.Equal(t, "weather.mcp.local", decision.Authority)
		require.Equal(t, "/mcp", decision.Path)

		// verify session ID is set
		require.Equal(t, "backend-session-abc", decision.SetHeaders["mcp-session-id"])

		// verify the body has the original backend ID restored
		var restored MCPRequest
		err := json.Unmarshal(decision.BodyMutation, &restored)
		require.NoError(t, err)
		require.Equal(t, float64(42), restored.ID)

		// verify the gateway ID was removed from the idmap after successful forwarding
		_, found, lookupErr := router.ElicitationMap.Lookup(context.Background(), gatewayID)
		require.NoError(t, lookupErr)
		require.False(t, found)
	})

	t.Run("rejects unknown gateway ID", func(t *testing.T) {
		router, validToken := newTestRouter(t, nil, map[string]string{}, map[string]string{})

		data := &MCPRequest{
			ID:      "nonexistent-gateway-id",
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept"},
			Headers: map[string]string{
				"mcp-session-id": validToken,
			},
		}

		decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
		require.NotNil(t, decision.Error)
		require.Equal(t, 400, decision.Error.StatusCode)
	})

	t.Run("rejects unknown server name", func(t *testing.T) {
		router, validToken := newTestRouter(t, []*config.MCPServer{}, map[string]string{}, map[string]string{})

		gatewayID := mustStoreIDMap(t, router.ElicitationMap, float64(1), "nonexistent-server", "session-123", validToken)

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "decline"},
			Headers: map[string]string{
				"mcp-session-id": validToken,
			},
		}

		decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
		require.NotNil(t, decision.Error)
		require.Equal(t, 500, decision.Error.StatusCode)
	})

	t.Run("restores string backend ID", func(t *testing.T) {
		serverConfigs := []*config.MCPServer{
			{
				Name:     "test-server",
				URL:      "http://test.mcp.local:8080/mcp",
				Hostname: "test.mcp.local",
			},
		}

		router, validToken := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{})

		gatewayID := mustStoreIDMap(t, router.ElicitationMap, "original-string-id", "test-server", "backend-session-xyz", validToken)

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "cancel"},
			Headers: map[string]string{
				"mcp-session-id": validToken,
			},
		}

		decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
		require.Nil(t, decision.Error)

		var restored MCPRequest
		err := json.Unmarshal(decision.BodyMutation, &restored)
		require.NoError(t, err)
		require.Equal(t, "original-string-id", restored.ID)
	})
}

func TestHandleElicitationResponse_ViaRouteRequest(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "test-server",
			URL:      "http://test.mcp.local:8080/mcp",
			Hostname: "test.mcp.local",
		},
	}

	router, validToken := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{})

	gatewayID := mustStoreIDMap(t, router.ElicitationMap, float64(99), "test-server", "backend-session-456", validToken)

	// elicitation response routed via RouteRequest (the main entry point)
	data := &MCPRequest{
		ID:      gatewayID,
		JSONRPC: "2.0",
		Result:  map[string]any{"action": "accept", "content": map[string]any{"value": "yes"}},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}

	decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
	require.Nil(t, decision.Error)

	var restored MCPRequest
	err := json.Unmarshal(decision.BodyMutation, &restored)
	require.NoError(t, err)
	require.Equal(t, float64(99), restored.ID)
}

func mustNewIDMap(t *testing.T) idmap.Map {
	t.Helper()
	m, err := idmap.New()
	require.NoError(t, err)
	return m
}

func mustStoreIDMap(t *testing.T, m idmap.Map, backendID any, serverName, sessionID, gatewaySessionID string) string {
	t.Helper()
	id, err := m.Store(context.Background(), backendID, serverName, sessionID, gatewaySessionID)
	require.NoError(t, err)
	return id
}

// TestHandleNoneToolCall_HairpinJWTValidation covers the GHSA-g53w-w6mj-hrpp
// fix: the router only honours an `mcp-init-host` rewrite when accompanied by
// a valid short-lived JWT bound to that host. Static keys, missing tokens, or
// host mismatches are rejected.
func TestHandleNoneToolCall_HairpinJWTValidation(t *testing.T) {
	newRouter := func(t *testing.T) *Router202511 {
		t.Helper()
		router, _ := newTestRouter(t, []*config.MCPServer{}, map[string]string{}, map[string]string{})
		return router
	}

	mustError := func(t *testing.T, decision *Decision, expectedCode int) {
		t.Helper()
		require.NotNil(t, decision.Error)
		require.Equal(t, expectedCode, decision.Error.StatusCode)
	}

	t.Run("rejects request with no token", func(t *testing.T) {
		router := newRouter(t)
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{
				"mcp-init-host": "backend.example.com",
			},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		mustError(t, decision, 400)
	})

	t.Run("rejects request with static key (legacy attack)", func(t *testing.T) {
		router := newRouter(t)
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{
				"mcp-init-host": "backend.example.com",
				RoutingKey:      "secret-api-key",
			},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		mustError(t, decision, 400)
	})

	t.Run("rejects token bound to different host", func(t *testing.T) {
		router := newRouter(t)
		token, err := router.JWTManager.GenerateBackendInitToken("good.example.com")
		require.NoError(t, err)
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{
				"mcp-init-host": "attacker.example.com",
				RoutingKey:      token,
			},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		mustError(t, decision, 400)
	})

	t.Run("accepts valid token bound to same host and strips router headers", func(t *testing.T) {
		router := newRouter(t)
		const targetHost = "backend.example.com"
		token, err := router.JWTManager.GenerateBackendInitToken(targetHost)
		require.NoError(t, err)

		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{
				"mcp-init-host": targetHost,
				RoutingKey:      token,
			},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		require.Nil(t, decision.Error)

		// authority should be rewritten to the target host
		require.Equal(t, targetHost, decision.Authority)

		// router-internal headers must be unset before forwarding to backend
		require.Contains(t, decision.UnsetHeaders, "mcp-init-host")
		require.Contains(t, decision.UnsetHeaders, RoutingKey)
		require.Contains(t, decision.UnsetHeaders, "x-mcp-authorized")
		require.Contains(t, decision.UnsetHeaders, "x-mcp-virtualserver")
	})

	t.Run("rejects token signed by a different gateway", func(t *testing.T) {
		router := newRouter(t)
		const targetHost = "backend.example.com"
		other, err := session.NewJWTManager("other-key-must-be-at-least-32-bytes", 0, slog.Default(), nil)
		require.NoError(t, err)
		token, err := other.GenerateBackendInitToken(targetHost)
		require.NoError(t, err)
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{
				"mcp-init-host": targetHost,
				RoutingKey:      token,
			},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		mustError(t, decision, 400)
	})

	t.Run("no init host header is a regular broker passthrough", func(t *testing.T) {
		router := newRouter(t)
		req := &MCPRequest{
			JSONRPC: "2.0",
			Method:  MethodInitialize,
			ID:      "1",
			Headers: map[string]string{},
		}
		decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
		require.Nil(t, decision.Error)
		require.True(t, decision.BrokerPass)
	})
}

// TestInitializeMCPServerSession_PassThroughHeaders verifies that headers
// forwarded to the upstream initialize call drop the router-internal headers
// (mcp-init-host, router-key) and the gateway-bound mcp-session-id even when
// supplied by a client. Anything else is preserved so custom headers still
// flow through. This is defense-in-depth on top of the explicit override in
// clients.Initialize.
func TestInitializeMCPServerSession_PassThroughHeaders(t *testing.T) {
	var captured map[string]string
	mockInitForClient := func(_ context.Context, _ string, _ *config.MCPServer, headers map[string]string, _ bool, _ *clients.HairpinClientPool) (*mcp.ClientSession, error) {
		captured = make(map[string]string, len(headers))
		for k, v := range headers {
			captured[k] = v
		}
		// short-circuit: returning an error skips the rest of the init flow,
		// which is fine since we only care about what was passed in.
		return nil, fmt.Errorf("mock init: skip further work")
	}

	serverConfigs := []*config.MCPServer{
		{
			Name:     "dummy",
			URL:      "http://localhost:8080/mcp",
			Prefix:   "s_",
			State:    "Enabled",
			Hostname: "backend.example.com",
		},
	}
	router, _ := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{})
	router.InitForClient = mockInitForClient
	router.RoutingConfig.Store(&config.MCPServersConfig{
		Servers:                    serverConfigs,
		MCPGatewayInternalHostname: "mcp-gateway.local",
	})

	validToken := router.JWTManager.Generate()
	table := NewTableBuilder().
		AddTool("s_mytool", &ServerRoute{
			Name:   "dummy",
			Host:   "backend.example.com",
			Prefix: "s_",
			Path:   "/mcp",
			URL:    "http://localhost:8080/mcp",
		}).
		Build()
	router.Table = func() RoutingTable { return table }

	req := &MCPRequest{
		ID:      ptr.To(0),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  map[string]any{"name": "s_mytool"},
		Headers: map[string]string{
			"mcp-session-id":      validToken,
			"mcp-init-host":       "attacker.example.com",
			RoutingKey:            "attacker-supplied-key",
			"x-custom-header":     "custom-value",
			"authorization":       "Bearer client-token",
			"x-mcp-authorized":    "signed-jwt-value",
			"x-mcp-virtualserver": "test/vs",
		},
	}

	_ = router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.NotNil(t, captured, "InitForClient must have been called")

	// router-internal and pseudo-headers are dropped
	require.NotContains(t, captured, ":authority", "pseudo-header must not be forwarded")
	require.NotContains(t, captured, ":path", "pseudo-header must not be forwarded")
	require.NotContains(t, captured, "mcp-session-id", "gateway session id must not be forwarded")
	require.NotContains(t, captured, "x-mcp-authorized", "broker-only filtering header must not reach upstream")
	require.NotContains(t, captured, "x-mcp-virtualserver", "broker-only filtering header must not reach upstream")

	// router-key and mcp-init-host must be set by the router (not from client input)
	require.Contains(t, captured, RoutingKey, "router must set the routing key")
	require.NotEqual(t, "attacker-supplied-key", captured[RoutingKey], "client-supplied router-key must be overwritten")
	require.Equal(t, "backend.example.com", captured["mcp-init-host"], "mcp-init-host must be set to the server hostname, not client-supplied value")

	// custom headers and authorization are passed through
	require.Equal(t, "custom-value", captured["x-custom-header"])
	require.Equal(t, "Bearer client-token", captured["authorization"])

	// gateway-set headers are present
	require.Equal(t, "tools/call", captured["x-mcp-method"])
	require.Equal(t, "dummy", captured["x-mcp-servername"])
	require.Equal(t, "mcp-router", captured["user-agent"])
}

func TestMCPRequest_PromptName(t *testing.T) {
	testCases := []struct {
		Name         string
		Input        *MCPRequest
		ExpectPrompt string
	}{
		{
			Name: "extracts prompt name from prompts/get",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "prompts/get",
				Params: map[string]any{
					"name": "test_prompt",
				},
			},
			ExpectPrompt: "test_prompt",
		},
		{
			Name: "returns empty for non prompts/get method",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Params: map[string]any{
					"name": "test",
				},
			},
			ExpectPrompt: "",
		},
		{
			Name: "returns empty when no name param",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "prompts/get",
				Params:  map[string]any{},
			},
			ExpectPrompt: "",
		},
		{
			Name: "returns empty for non-string name",
			Input: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "prompts/get",
				Params: map[string]any{
					"name": 42,
				},
			},
			ExpectPrompt: "",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			require.Equal(t, tc.ExpectPrompt, tc.Input.PromptName())
		})
	}
}

func TestMCPRequest_isPromptGet(t *testing.T) {
	testCases := []struct {
		name     string
		method   string
		expected bool
	}{
		{name: "prompts/get is prompt get", method: "prompts/get", expected: true},
		{name: "prompts/list is not prompt get", method: "prompts/list", expected: false},
		{name: "tools/call is not prompt get", method: "tools/call", expected: false},
		{name: "empty is not prompt get", method: "", expected: false},
		{name: "PROMPTS/GET uppercase is not prompt get", method: "PROMPTS/GET", expected: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{Method: tc.method}
			require.Equal(t, tc.expected, req.IsPromptGet())
		})
	}
}

func TestHandlePromptGet(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "dummy",
			URL:      "http://localhost:8080/mcp",
			Prefix:   "s_",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router, validToken := newTestRouter(t, serverConfigs, map[string]string{}, map[string]string{"s_myprompt": "dummy"})

	sessionAdded, err := router.SessionCache.AddSession(context.Background(), validToken, "dummy", "mock-upstream-session-id", 0)
	require.NoError(t, err)
	require.True(t, sessionAdded)

	mockInitForClient := func(_ context.Context, _ string, _ *config.MCPServer, _ map[string]string, _ bool, _ *clients.HairpinClientPool) (*mcp.ClientSession, error) {
		return nil, fmt.Errorf("InitForClient should not be called when session exists")
	}
	router.InitForClient = mockInitForClient

	data := &MCPRequest{
		ID:      ptr.To(0),
		JSONRPC: "2.0",
		Method:  "prompts/get",
		Params: map[string]any{
			"name": "s_myprompt",
		},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}

	decision := router.RouteRequest(context.Background(), &Request{Parsed: data})
	require.Nil(t, decision.Error)

	require.Equal(t, "prompts/get", decision.SetHeaders["x-mcp-method"])
	require.Equal(t, "myprompt", decision.SetHeaders["x-mcp-promptname"])
	require.Equal(t, "dummy", decision.SetHeaders["x-mcp-servername"])

	require.Contains(t, string(decision.BodyMutation), `"name":"myprompt"`)
	require.NotContains(t, string(decision.BodyMutation), `"name":"s_myprompt"`)
}

func testBearerJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"%s"}`, sub)))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("Bearer %s.%s.%s", header, payload, sig)
}

func testBearerJWTNoSub() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"gateway"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("Bearer %s.%s.%s", header, payload, sig)
}

func setupTokenResolutionTestRouter(t *testing.T, serverConfigs []*config.MCPServer, toolMap map[string]string, tokenElicitationMap elicitation.Map) (*Router202511, string) {
	t.Helper()
	router, validToken := newTestRouter(t, serverConfigs, toolMap, map[string]string{})

	// pre-populate session so we skip InitForClient
	for _, svr := range serverConfigs {
		_, err := router.SessionCache.AddSession(context.Background(), validToken, svr.Name, "mock-upstream-session", 0)
		require.NoError(t, err)
	}

	router.InitForClient = func(_ context.Context, _ string, _ *config.MCPServer, _ map[string]string, _ bool, _ *clients.HairpinClientPool) (*mcp.ClientSession, error) {
		return nil, fmt.Errorf("should not be called")
	}
	router.TokenElicitationMap = tokenElicitationMap
	router.ElicitationEnabled = true
	router.RoutingConfig.Store(&config.MCPServersConfig{
		Servers:                    serverConfigs,
		MCPGatewayExternalHostname: "gateway.example.com",
	})
	return router, validToken
}

func TestResolveUpstreamToken_NoElicitationConfig(t *testing.T) {
	// server without TokenURLElicitation → existing behavior unchanged, no auth header injected
	serverConfigs := []*config.MCPServer{{
		Name: "plain-server", URL: "http://localhost:8080/mcp", Prefix: "p_", State: "Enabled", Hostname: "localhost",
	}}
	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"p_tool": "plain-server"}, nil)

	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "p_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)

	// no authorization header should be injected
	_, hasAuth := decision.SetHeaders["authorization"]
	require.False(t, hasAuth, "should not inject authorization header")
}

func TestResolveUpstreamToken_CachedTokenInjected(t *testing.T) {
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)

	// pre-populate the user token in the session cache
	require.NoError(t, router.SessionCache.SetUserToken(context.Background(), validToken, "github", "ghp_cached_token", 0))

	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)

	// token is injected as-is (no "Bearer " prefix) — upstream servers handle both PATs and Bearer tokens
	require.Equal(t, "ghp_cached_token", decision.SetHeaders["authorization"], "authorization header should be injected with cached token")
}

func TestResolveUpstreamToken_CacheMiss_ElicitationTriggered(t *testing.T) {
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)

	// mark client as supporting elicitation
	require.NoError(t, router.SessionCache.SetClientElicitation(context.Background(), validToken, 0))

	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
			"authorization":  testBearerJWT("user456"),
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)

	// should route to broker with elicitation headers
	elicitationID, ok := decision.SetHeaders["x-mcp-elicitation-id"]
	require.True(t, ok, "x-mcp-elicitation-id header should be present")
	require.NotEmpty(t, elicitationID)

	requestID, ok := decision.SetHeaders["x-mcp-request-id"]
	require.True(t, ok, "x-mcp-request-id header should be present")
	require.NotEmpty(t, requestID)

	require.Equal(t, "mcpBroker", decision.SetHeaders["x-mcp-servername"])
	require.Equal(t, "/mcp/elicitation", decision.Path)
}

func TestResolveUpstreamToken_CacheMiss_NoElicitationSupport(t *testing.T) {
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)

	// client does NOT support elicitation (default)
	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.NotNil(t, decision.Error)
	require.Equal(t, 200, decision.Error.StatusCode)
	require.Contains(t, decision.Error.JSONRPCErr, "does not support elicitation")
	require.Contains(t, decision.Error.JSONRPCErr, "isError")
}

func TestResolveUpstreamToken_JWTWithoutSub(t *testing.T) {
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)

	// JWT present but no sub claim → misconfigured OIDC → error
	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
			"authorization":  testBearerJWTNoSub(),
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.NotNil(t, decision.Error)
	require.Equal(t, 200, decision.Error.StatusCode)
	require.Contains(t, decision.Error.JSONRPCErr, "missing sub claim")
	require.Contains(t, decision.Error.JSONRPCErr, "isError")
}

func TestResolveUpstreamToken_ExternalURL(t *testing.T) {
	externalURL := "https://auth.example.com/tokens"
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{URL: externalURL},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)
	require.NoError(t, router.SessionCache.SetClientElicitation(context.Background(), validToken, 0))

	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)

	elicitationID, ok := decision.SetHeaders["x-mcp-elicitation-id"]
	require.True(t, ok, "x-mcp-elicitation-id header should be present")
	require.NotEmpty(t, elicitationID)
}

func TestResolveUpstreamToken_SubExtractedAndStored(t *testing.T) {
	serverConfigs := []*config.MCPServer{{
		Name: "github", URL: "http://github.mcp:8080/mcp", Prefix: "gh_", State: "Enabled", Hostname: "github.mcp",
		TokenURLElicitation: &config.TokenURLElicitationConfig{},
	}}
	tokenMap, err := elicitation.New()
	require.NoError(t, err)

	router, validToken := setupTokenResolutionTestRouter(t, serverConfigs, map[string]string{"gh_tool": "github"}, tokenMap)
	require.NoError(t, router.SessionCache.SetClientElicitation(context.Background(), validToken, 0))

	req := &MCPRequest{
		ID: ptr.To(1), JSONRPC: "2.0", Method: "tools/call",
		Params: map[string]any{"name": "gh_tool"},
		Headers: map[string]string{
			"mcp-session-id": validToken,
			"authorization":  testBearerJWT("user123"),
		},
	}
	decision := router.RouteRequest(context.Background(), &Request{Parsed: req})
	require.Nil(t, decision.Error)

	elicitationID, ok := decision.SetHeaders["x-mcp-elicitation-id"]
	require.True(t, ok, "x-mcp-elicitation-id header should be present")
	require.NotEmpty(t, elicitationID)

	entry, lookupOk, lookupErr := tokenMap.Lookup(context.Background(), elicitationID)
	require.NoError(t, lookupErr)
	require.True(t, lookupOk, "elicitation entry should exist")
	require.Equal(t, "user123", entry.Sub)
	require.Equal(t, "github", entry.ServerName)
	require.Equal(t, validToken, entry.SessionID)
}

func TestBuildSSEToolError(t *testing.T) {
	result := BuildSSEToolError("req-1", "something went wrong")
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Result  struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	data := extractSSEData(t, result)
	require.NoError(t, json.Unmarshal([]byte(data), &envelope))
	require.Equal(t, "2.0", envelope.JSONRPC)
	require.Equal(t, "req-1", envelope.ID)
	require.True(t, envelope.Result.IsError)
	require.Len(t, envelope.Result.Content, 1)
	require.Equal(t, "text", envelope.Result.Content[0].Type)
	require.Equal(t, "something went wrong", envelope.Result.Content[0].Text)
}

func extractSSEData(t *testing.T, sse string) string {
	t.Helper()
	const prefix = "data: "
	idx := strings.Index(sse, prefix)
	require.Greater(t, idx, -1, "SSE should contain data: prefix")
	rest := sse[idx+len(prefix):]
	end := strings.Index(rest, "\n")
	if end == -1 {
		return rest
	}
	return rest[:end]
}

// x-mcp-annotation-hints must render mark3labs semantics: nil pointers as
// unspecified, explicit true/false preserved, all four keys always present.
func TestAnnotationHintsHeader(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	// local rendering replica mirrors the inline code in HandleToolCall
	renderHints := func(h upstream.ToolHints) string {
		var parts []string
		push := func(key string, val *bool) {
			if val == nil {
				parts = append(parts, fmt.Sprintf("%s=unspecified", key))
			} else if *val {
				parts = append(parts, fmt.Sprintf("%s=true", key))
			} else {
				parts = append(parts, fmt.Sprintf("%s=false", key))
			}
		}
		push("readOnly", h.ReadOnlyHint)
		push("destructive", h.DestructiveHint)
		push("idempotent", h.IdempotentHint)
		push("openWorld", h.OpenWorldHint)
		return strings.Join(parts, ",")
	}
	cases := []struct {
		name  string
		hints upstream.ToolHints
		want  string
	}{
		{
			name:  "all unspecified",
			hints: upstream.ToolHints{},
			want:  "readOnly=unspecified,destructive=unspecified,idempotent=unspecified,openWorld=unspecified",
		},
		{
			name: "explicit values",
			hints: upstream.ToolHints{
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
			want: "readOnly=true,destructive=false,idempotent=true,openWorld=false",
		},
		{
			name: "mixed explicit false and unspecified",
			hints: upstream.ToolHints{
				ReadOnlyHint:   boolPtr(false),
				IdempotentHint: boolPtr(false),
			},
			want: "readOnly=false,destructive=unspecified,idempotent=false,openWorld=unspecified",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, renderHints(tc.hints))
		})
	}
}
