package mcprouter

import (
	"context"
	"encoding/json"
	"fmt"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"k8s.io/utils/ptr"

	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"
)

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
				Method:  "intialise",
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
				Method:  "intialise",
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
	cache, err := session.NewCache(context.Background())
	require.NoError(t, err)

	// Create JWT manager for test
	jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
	require.NoError(t, err)

	// Generate a valid JWT token
	validToken := jwtManager.Generate()

	// Pre-populate the session cache so InitForClient won't be called
	// This simulates the case where the session already exists
	sessionAdded, err := cache.AddSession(context.Background(), validToken, "dummy", "mock-upstream-session-id")
	require.NoError(t, err)
	require.True(t, sessionAdded)

	// Mock InitForClient - should not be called since session exists
	mockInitForClient := func(_ context.Context, _, _ string, _ *config.MCPServer, _ map[string]string, _ bool) (*client.Client, error) {
		// This should not be called in this test since session exists in cache
		return nil, fmt.Errorf("InitForClient should not be called when session exists")
	}

	serverConfigs := []*config.MCPServer{
		{
			Name:       "dummy",
			URL:        "http://localhost:8080/mcp",
			ToolPrefix: "s_",
			Enabled:    true,
			Hostname:   "localhost",
		},
	}

	server := &ExtProcServer{
		RoutingConfig: &config.MCPServersConfig{
			Servers: serverConfigs,
		},
		JWTManager:    jwtManager,
		Logger:        logger,
		SessionCache:  cache,
		InitForClient: mockInitForClient,
		Broker: newMockBroker(serverConfigs, map[string]string{
			"s_mytool": "dummy",
		}),
	}

	data := &MCPRequest{
		ID:      ptr.To(0),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]any{
			"name":  "s_mytool",
			"other": "other",
		},
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(validToken),
				},
			},
		},
	}

	resp := server.RouteMCPRequest(context.Background(), data)
	require.Len(t, resp, 1)
	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, resp[0].Response)
	rb := resp[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.NotNil(t, rb.RequestBody.Response)
	require.Len(t, rb.RequestBody.Response.HeaderMutation.SetHeaders, 7)
	require.Equal(t, "x-mcp-method", rb.RequestBody.Response.HeaderMutation.SetHeaders[0].Header.Key)
	require.Equal(t, []uint8("tools/call"), rb.RequestBody.Response.HeaderMutation.SetHeaders[0].Header.RawValue)
	require.Equal(t, "x-mcp-toolname", rb.RequestBody.Response.HeaderMutation.SetHeaders[1].Header.Key)
	require.Equal(t, []uint8("mytool"), rb.RequestBody.Response.HeaderMutation.SetHeaders[1].Header.RawValue)
	require.Equal(t, "x-mcp-servername", rb.RequestBody.Response.HeaderMutation.SetHeaders[2].Header.Key)
	require.Equal(t, []uint8("dummy"), rb.RequestBody.Response.HeaderMutation.SetHeaders[2].Header.RawValue)
	require.Equal(t, "mcp-session-id", rb.RequestBody.Response.HeaderMutation.SetHeaders[3].Header.Key)
	require.Equal(t, []uint8("mock-upstream-session-id"), rb.RequestBody.Response.HeaderMutation.SetHeaders[3].Header.RawValue)
	require.Equal(t, ":authority", rb.RequestBody.Response.HeaderMutation.SetHeaders[4].Header.Key)
	require.Equal(t, []uint8("localhost"), rb.RequestBody.Response.HeaderMutation.SetHeaders[4].Header.RawValue)
	require.Equal(t, ":path", rb.RequestBody.Response.HeaderMutation.SetHeaders[5].Header.Key)
	require.Equal(t, []uint8("/mcp"), rb.RequestBody.Response.HeaderMutation.SetHeaders[5].Header.RawValue)
	require.Equal(t, "content-length", rb.RequestBody.Response.HeaderMutation.SetHeaders[6].Header.Key)

	require.Equal(t,
		`{"id":0,"jsonrpc":"2.0","method":"tools/call","params":{"name":"mytool","other":"other"}}`,
		string(rb.RequestBody.Response.BodyMutation.GetBody()))
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
			result := req.isNotificationRequest()
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
			result := req.isToolCall()
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
			result := req.isInitializeRequest()
			require.Equal(t, tc.expected, result, "method %q should return %v", tc.method, tc.expected)
		})
	}
}

func TestMCPRequest_GetSessionID(t *testing.T) {
	testCases := []struct {
		name       string
		headers    *corev3.HeaderMap
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
			headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte("header-session-id")},
				},
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
			headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "other-header", RawValue: []byte("value")},
				},
			},
			presetID:   "",
			expectedID: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &MCPRequest{
				Headers:   tc.headers,
				sessionID: tc.presetID,
			}
			result := req.GetSessionID()
			require.Equal(t, tc.expectedID, result)
		})
	}
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
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "content-type", RawValue: []byte("application/json")},
				{Key: "x-custom-header", RawValue: []byte("custom-value")},
			},
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
			result := tc.req.isElicitationResponse()
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("routes elicitation response to correct backend", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		validToken := jwtManager.Generate()

		serverConfigs := []*config.MCPServer{
			{
				Name:       "weather-server",
				URL:        "http://weather.mcp.local:8080/mcp",
				ToolPrefix: "weather_",
				Enabled:    true,
				Hostname:   "weather.mcp.local",
			},
		}

		elicitationMap := idmap.New()
		gatewayID := elicitationMap.Store(float64(42), "weather-server", "backend-session-abc")

		server := &ExtProcServer{
			RoutingConfig: &config.MCPServersConfig{
				Servers: serverConfigs,
			},
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: elicitationMap,
			Broker:         newMockBroker(serverConfigs, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept", "content": map[string]any{"name": "test"}},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte(validToken)},
				},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, resp[0].Response)

		rb := resp[0].Response.(*eppb.ProcessingResponse_RequestBody)
		require.NotNil(t, rb.RequestBody.Response)

		// verify authority is set to backend hostname
		headers := rb.RequestBody.Response.HeaderMutation.SetHeaders
		var authoritySet, sessionSet, pathSet bool
		for _, h := range headers {
			switch h.Header.Key {
			case ":authority":
				require.Equal(t, "weather.mcp.local", string(h.Header.RawValue))
				authoritySet = true
			case "mcp-session-id":
				require.Equal(t, "backend-session-abc", string(h.Header.RawValue))
				sessionSet = true
			case ":path":
				require.Equal(t, "/mcp", string(h.Header.RawValue))
				pathSet = true
			}
		}
		require.True(t, authoritySet, "authority header should be set")
		require.True(t, sessionSet, "mcp-session-id header should be set")
		require.True(t, pathSet, "path header should be set")

		// verify the body has the original backend ID restored
		body := rb.RequestBody.Response.BodyMutation.GetBody()
		var restored MCPRequest
		err = json.Unmarshal(body, &restored)
		require.NoError(t, err)
		require.Equal(t, float64(42), restored.ID)

		// verify the gateway ID was consumed from the idmap (Lookup is destructive)
		_, found := elicitationMap.Lookup(gatewayID)
		require.False(t, found)
	})

	t.Run("rejects missing session ID", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		server := &ExtProcServer{
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: idmap.New(),
			Broker:         newMockBroker(nil, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      "gw-123",
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept"},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, resp[0].Response)
		ir := resp[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
		require.Equal(t, int32(400), int32(ir.ImmediateResponse.Status.Code))
	})

	t.Run("rejects invalid session", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		server := &ExtProcServer{
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: idmap.New(),
			Broker:         newMockBroker(nil, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      "gw-123",
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept"},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte("invalid-jwt-token")},
				},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, resp[0].Response)
		ir := resp[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
		require.Equal(t, int32(404), int32(ir.ImmediateResponse.Status.Code))
	})

	t.Run("rejects unknown gateway ID", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		validToken := jwtManager.Generate()

		server := &ExtProcServer{
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: idmap.New(), // empty - no stored mappings
			Broker:         newMockBroker(nil, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      "nonexistent-gateway-id",
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "accept"},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte(validToken)},
				},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, resp[0].Response)
		ir := resp[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
		require.Equal(t, int32(400), int32(ir.ImmediateResponse.Status.Code))
	})

	t.Run("rejects unknown server name", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		validToken := jwtManager.Generate()

		elicitationMap := idmap.New()
		gatewayID := elicitationMap.Store(float64(1), "nonexistent-server", "session-123")

		server := &ExtProcServer{
			RoutingConfig: &config.MCPServersConfig{
				Servers: []*config.MCPServer{}, // no servers configured
			},
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: elicitationMap,
			Broker:         newMockBroker(nil, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "decline"},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte(validToken)},
				},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, resp[0].Response)
		ir := resp[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
		require.Equal(t, int32(500), int32(ir.ImmediateResponse.Status.Code))
	})

	t.Run("restores string backend ID", func(t *testing.T) {
		cache, err := session.NewCache(context.Background())
		require.NoError(t, err)

		jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
		require.NoError(t, err)

		validToken := jwtManager.Generate()

		serverConfigs := []*config.MCPServer{
			{
				Name:     "test-server",
				URL:      "http://test.mcp.local:8080/mcp",
				Hostname: "test.mcp.local",
			},
		}

		elicitationMap := idmap.New()
		gatewayID := elicitationMap.Store("original-string-id", "test-server", "backend-session-xyz")

		server := &ExtProcServer{
			RoutingConfig: &config.MCPServersConfig{
				Servers: serverConfigs,
			},
			JWTManager:     jwtManager,
			Logger:         logger,
			SessionCache:   cache,
			ElicitationMap: elicitationMap,
			Broker:         newMockBroker(serverConfigs, map[string]string{}),
		}

		data := &MCPRequest{
			ID:      gatewayID,
			JSONRPC: "2.0",
			Result:  map[string]any{"action": "cancel"},
			Headers: &corev3.HeaderMap{
				Headers: []*corev3.HeaderValue{
					{Key: "mcp-session-id", RawValue: []byte(validToken)},
				},
			},
		}

		resp := server.HandleElicitationResponse(context.Background(), data)
		require.Len(t, resp, 1)
		require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, resp[0].Response)

		rb := resp[0].Response.(*eppb.ProcessingResponse_RequestBody)
		body := rb.RequestBody.Response.BodyMutation.GetBody()
		var restored MCPRequest
		err = json.Unmarshal(body, &restored)
		require.NoError(t, err)
		require.Equal(t, "original-string-id", restored.ID)
	})
}

func TestHandleElicitationResponse_ViaRouteMCPRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cache, err := session.NewCache(context.Background())
	require.NoError(t, err)

	jwtManager, err := session.NewJWTManager("test-signing-key", 0, logger, cache)
	require.NoError(t, err)

	validToken := jwtManager.Generate()

	serverConfigs := []*config.MCPServer{
		{
			Name:     "test-server",
			URL:      "http://test.mcp.local:8080/mcp",
			Hostname: "test.mcp.local",
		},
	}

	elicitationMap := idmap.New()
	gatewayID := elicitationMap.Store(float64(99), "test-server", "backend-session-456")

	server := &ExtProcServer{
		RoutingConfig: &config.MCPServersConfig{
			Servers: serverConfigs,
		},
		JWTManager:     jwtManager,
		Logger:         logger,
		SessionCache:   cache,
		ElicitationMap: elicitationMap,
		Broker:         newMockBroker(serverConfigs, map[string]string{}),
	}

	// elicitation response routed via RouteMCPRequest (the main switch)
	data := &MCPRequest{
		ID:      gatewayID,
		JSONRPC: "2.0",
		Result:  map[string]any{"action": "accept", "content": map[string]any{"value": "yes"}},
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-session-id", RawValue: []byte(validToken)},
			},
		},
	}

	resp := server.RouteMCPRequest(context.Background(), data)
	require.Len(t, resp, 1)
	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, resp[0].Response)

	rb := resp[0].Response.(*eppb.ProcessingResponse_RequestBody)
	body := rb.RequestBody.Response.BodyMutation.GetBody()
	var restored MCPRequest
	err = json.Unmarshal(body, &restored)
	require.NoError(t, err)
	require.Equal(t, float64(99), restored.ID)
}

func TestHandleRequestHeaders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		Name            string
		GatewayHostname string
	}{
		{
			Name:            "sets authority header to gateway hostname",
			GatewayHostname: "mcp.example.com",
		},
		{
			Name:            "handles wildcard gateway hostname",
			GatewayHostname: "*.mcp.local",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			server := &ExtProcServer{
				RoutingConfig: &config.MCPServersConfig{
					MCPGatewayExternalHostname: tc.GatewayHostname,
				},
				Logger: logger,
				Broker: newMockBroker(nil, map[string]string{}),
			}

			headers := &eppb.HttpHeaders{
				Headers: &corev3.HeaderMap{
					Headers: []*corev3.HeaderValue{
						{
							Key:      ":authority",
							RawValue: []byte("original.host.com"),
						},
					},
				},
			}

			responses, err := server.HandleRequestHeaders(headers)

			require.NoError(t, err)
			require.Len(t, responses, 1)

			// should be a request headers response
			require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
			rh := responses[0].Response.(*eppb.ProcessingResponse_RequestHeaders)
			require.NotNil(t, rh.RequestHeaders)

			// verify authority header was set
			headerMutation := rh.RequestHeaders.Response.HeaderMutation
			require.NotNil(t, headerMutation)
			require.Len(t, headerMutation.SetHeaders, 1)
			require.Equal(t, ":authority", headerMutation.SetHeaders[0].Header.Key)
			require.Equal(t, tc.GatewayHostname, string(headerMutation.SetHeaders[0].Header.RawValue))
		})
	}
}
