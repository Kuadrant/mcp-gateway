package routing

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/stretchr/testify/require"
)

func TestResponseHandler_ReturnsGatewaySessionID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	gatewaySessionID := "gateway-session-123"
	upstreamSessionID := "upstream-session-456"

	input := &ResponseInput{
		GatewaySessionID:  gatewaySessionID,
		ResponseSessionID: upstreamSessionID,
		StatusCode:        "200",
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
	require.Equal(t, gatewaySessionID, decision.SetHeaders[SessionHeader])
}

func TestResponseHandler_NoGatewaySessionID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	input := &ResponseInput{
		GatewaySessionID:  "",
		ResponseSessionID: "",
		StatusCode:        "200",
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
	_, hasSessionHeader := decision.SetHeaders[SessionHeader]
	require.False(t, hasSessionHeader)
}

func TestResponseHandler_404RemovesServerSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	gatewaySessionID := "gateway-session-123"
	serverName := "test-server"

	// add a session to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName, "upstream-session-456", 0)
	require.NoError(t, err)

	// verify session exists
	sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-456", sessions[serverName])

	mcpReq := &MCPRequest{
		SessionID:  gatewaySessionID,
		ServerName: serverName,
		Method:     "tools/call",
	}

	input := &ResponseInput{
		GatewaySessionID: gatewaySessionID,
		StatusCode:       "404",
		Request:          mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	// verify the server session was removed from cache
	sessions, err = cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Empty(t, sessions)
}

func TestResponseHandler_404WithoutMCPRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	input := &ResponseInput{
		GatewaySessionID: "gateway-session-123",
		StatusCode:       "404",
		Request:          nil,
	}

	// should not panic or error
	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
}

func TestResponseHandler_404WithMultipleServerSessions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	gatewaySessionID := "gateway-session-123"
	serverName1 := "server1"
	serverName2 := "server2"

	// add multiple server sessions to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName1, "upstream-session-1", 0)
	require.NoError(t, err)
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName2, "upstream-session-2", 0)
	require.NoError(t, err)

	// verify both sessions exist
	sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	mcpReq := &MCPRequest{
		SessionID:  gatewaySessionID,
		ServerName: serverName1,
		Method:     "tools/call",
	}

	input := &ResponseInput{
		GatewaySessionID: gatewaySessionID,
		StatusCode:       "404",
		Request:          mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	// verify only server1 session was removed, server2 session remains
	sessions, err = cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-2", sessions[serverName2])
	_, exists := sessions[serverName1]
	require.False(t, exists)
}

func TestResponseHandler_SuccessStatusDoesNotRemoveSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	gatewaySessionID := "gateway-session-123"
	serverName := "test-server"

	// add a session to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName, "upstream-session-456", 0)
	require.NoError(t, err)

	// test various success status codes
	successCodes := []string{"200", "201", "204"}

	for _, statusCode := range successCodes {
		t.Run("status_"+statusCode, func(t *testing.T) {
			mcpReq := &MCPRequest{
				SessionID:  gatewaySessionID,
				ServerName: serverName,
				Method:     "tools/call",
			}

			input := &ResponseInput{
				GatewaySessionID: gatewaySessionID,
				StatusCode:       statusCode,
				Request:          mcpReq,
			}

			decision := handler.HandleResponse(context.Background(), input)

			require.NotNil(t, decision)

			// verify the session was NOT removed
			sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.Equal(t, "upstream-session-456", sessions[serverName])
		})
	}
}

func TestResponseHandler_StoresElicitationForDirectInit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	jwtManager, err := session.NewJWTManager("test-signing-key-must-be-at-least-32-bytes", 0, logger, cache)
	require.NoError(t, err)
	brokerSessionID := jwtManager.Generate()

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
		JWTManager:   jwtManager,
	}

	mcpReq := &MCPRequest{
		Method: "initialize",
		Params: map[string]any{
			"capabilities": map[string]any{
				"elicitation": map[string]any{},
			},
		},
	}

	input := &ResponseInput{
		GatewaySessionID:  "",
		ResponseSessionID: brokerSessionID,
		StatusCode:        "200",
		InitHost:          "",
		Request:           mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	val, err := cache.GetClientElicitation(context.Background(), brokerSessionID)
	require.NoError(t, err)
	require.True(t, val)
}

func TestResponseHandler_SkipsElicitationForHairpinInit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	backendSessionID := "backend-session-456"

	mcpReq := &MCPRequest{
		Method: "initialize",
		Params: map[string]any{
			"capabilities": map[string]any{
				"elicitation": map[string]any{},
			},
		},
	}

	input := &ResponseInput{
		GatewaySessionID:  "",
		ResponseSessionID: backendSessionID,
		StatusCode:        "200",
		InitHost:          "backend.example.com",
		Request:           mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	val, err := cache.GetClientElicitation(context.Background(), backendSessionID)
	require.NoError(t, err)
	require.False(t, val)
}

func TestResponseHandler_401DeletesUserToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	serverName := "elicit-server"
	gatewaySessionID := "gateway-session-123"

	routingConfig := &config.MCPServersConfig{
		Servers: []*config.MCPServer{
			{
				Name:                serverName,
				TokenURLElicitation: &config.TokenURLElicitationConfig{},
			},
		},
	}

	handler := &ResponseHandler202511{
		Logger:             logger,
		SessionCache:       cache,
		ElicitationEnabled: true,
		RoutingConfig:      storeConfig(routingConfig),
	}

	// store a user token in the cache
	require.NoError(t, cache.SetUserToken(context.Background(), gatewaySessionID, serverName, "expired-token", 0))
	tok, ok, err := cache.GetUserToken(context.Background(), gatewaySessionID, serverName)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "expired-token", tok)

	mcpReq := &MCPRequest{
		SessionID:  gatewaySessionID,
		ServerName: serverName,
		Method:     "tools/call",
	}

	input := &ResponseInput{
		GatewaySessionID: gatewaySessionID,
		StatusCode:       "401",
		Request:          mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	// token should be deleted
	_, ok, err = cache.GetUserToken(context.Background(), gatewaySessionID, serverName)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestResponseHandler_401SkipsTokenDeleteWhenNotApplicable(t *testing.T) {
	tests := []struct {
		name               string
		serverName         string
		elicitationConfig  *config.TokenURLElicitationConfig
		elicitationEnabled bool
	}{
		{
			name:               "no elicitation config on server",
			serverName:         "plain-server",
			elicitationConfig:  nil,
			elicitationEnabled: true,
		},
		{
			name:               "elicitation feature disabled",
			serverName:         "elicit-server",
			elicitationConfig:  &config.TokenURLElicitationConfig{},
			elicitationEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
			cache, err := session.NewCache()
			require.NoError(t, err)

			gatewaySessionID := "gateway-session-123"
			routingConfig := &config.MCPServersConfig{
				Servers: []*config.MCPServer{
					{Name: tt.serverName, TokenURLElicitation: tt.elicitationConfig},
				},
			}

			handler := &ResponseHandler202511{
				Logger:             logger,
				SessionCache:       cache,
				ElicitationEnabled: tt.elicitationEnabled,
				RoutingConfig:      storeConfig(routingConfig),
			}

			require.NoError(t, cache.SetUserToken(context.Background(), gatewaySessionID, tt.serverName, "my-token", 0))

			mcpReq := &MCPRequest{
				SessionID:  gatewaySessionID,
				ServerName: tt.serverName,
				Method:     "tools/call",
			}

			input := &ResponseInput{
				GatewaySessionID: gatewaySessionID,
				StatusCode:       "401",
				Request:          mcpReq,
			}

			decision := handler.HandleResponse(context.Background(), input)

			require.NotNil(t, decision)

			_, ok, err := cache.GetUserToken(context.Background(), gatewaySessionID, tt.serverName)
			require.NoError(t, err)
			require.True(t, ok, "token should NOT be deleted")
		})
	}
}

func TestResponseHandler_401WithNilRequestNoAction(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:             logger,
		SessionCache:       cache,
		ElicitationEnabled: true,
	}

	input := &ResponseInput{
		GatewaySessionID: "gateway-session-123",
		StatusCode:       "401",
		Request:          nil,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
}

func TestResponseHandler_SuccessStatusDoesNotDeleteUserToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	serverName := "elicit-server"
	gatewaySessionID := "gateway-session-123"

	routingConfig := &config.MCPServersConfig{
		Servers: []*config.MCPServer{
			{
				Name:                serverName,
				TokenURLElicitation: &config.TokenURLElicitationConfig{},
			},
		},
	}

	handler := &ResponseHandler202511{
		Logger:             logger,
		SessionCache:       cache,
		ElicitationEnabled: true,
		RoutingConfig:      storeConfig(routingConfig),
	}

	require.NoError(t, cache.SetUserToken(context.Background(), gatewaySessionID, serverName, "valid-token", 0))

	mcpReq := &MCPRequest{
		SessionID:  gatewaySessionID,
		ServerName: serverName,
		Method:     "tools/call",
	}

	input := &ResponseInput{
		GatewaySessionID: gatewaySessionID,
		StatusCode:       "200",
		Request:          mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)

	// token should still be present
	_, ok, err := cache.GetUserToken(context.Background(), gatewaySessionID, serverName)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestResponseHandler_StreamBodyModeForElicitationRewrite(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	mcpReq := &MCPRequest{
		Method:            "tools/call",
		ClientElicitation: true,
	}

	input := &ResponseInput{
		StatusCode: "200",
		Request:    mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
	require.True(t, decision.StreamBody, "StreamBody should be true for tool/call with client elicitation on 200")
}

func TestResponseHandler_StreamBodyModeNotSetForNonToolCall(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	mcpReq := &MCPRequest{
		Method:            "initialize",
		ClientElicitation: true,
	}

	input := &ResponseInput{
		StatusCode: "200",
		Request:    mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
	require.False(t, decision.StreamBody, "StreamBody should be false for non-tool/call methods")
}

func TestResponseHandler_StreamBodyModeNotSetForNonSuccessStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	handler := &ResponseHandler202511{
		Logger:       logger,
		SessionCache: cache,
	}

	mcpReq := &MCPRequest{
		Method:            "tools/call",
		ClientElicitation: true,
	}

	input := &ResponseInput{
		StatusCode: "404",
		Request:    mcpReq,
	}

	decision := handler.HandleResponse(context.Background(), input)

	require.NotNil(t, decision)
	require.False(t, decision.StreamBody, "StreamBody should be false for non-200 status")
}

func storeConfig(cfg *config.MCPServersConfig) *atomic.Pointer[config.MCPServersConfig] {
	p := &atomic.Pointer[config.MCPServersConfig]{}
	p.Store(cfg)
	return p
}
