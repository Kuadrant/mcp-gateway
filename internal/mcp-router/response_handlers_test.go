package mcprouter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

type mockBrokerImpl struct {
	// Servers known to this mock broker
	svrConfigs []*config.MCPServer

	// Map of tool name to server name
	tool2svr map[string]string
}

func TestHandleResponseHeaders_ReturnsGatewaySessionID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	gatewaySessionID := "gateway-session-123"
	upstreamSessionID := "upstream-session-456"

	// request headers with gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(gatewaySessionID),
				},
			},
		},
	}

	// response headers with upstream session ID
	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-sessionid",
					RawValue: []byte(upstreamSessionID),
				},
				{
					Key:      ":status",
					RawValue: []byte("200"),
				},
			},
		},
	}

	responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, nil)

	require.NoError(t, err)
	require.Len(t, responses, 1)
	require.IsType(t, &eppb.ProcessingResponse_ResponseHeaders{}, responses[0].Response)

	rh := responses[0].Response.(*eppb.ProcessingResponse_ResponseHeaders)
	require.NotNil(t, rh.ResponseHeaders)
	require.NotNil(t, rh.ResponseHeaders.Response)
	require.Len(t, rh.ResponseHeaders.Response.HeaderMutation.SetHeaders, 1)

	// verify gateway session ID is returned to client
	require.Equal(t, "mcp-session-id", rh.ResponseHeaders.Response.HeaderMutation.SetHeaders[0].Header.Key)
	require.Equal(t, gatewaySessionID, string(rh.ResponseHeaders.Response.HeaderMutation.SetHeaders[0].Header.RawValue))
}

func TestHandleResponseHeaders_NoGatewaySessionID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	// request headers without gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{},
		},
	}

	// response headers
	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      ":status",
					RawValue: []byte("200"),
				},
			},
		},
	}

	responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, nil)

	require.NoError(t, err)
	require.Len(t, responses, 1)
	require.IsType(t, &eppb.ProcessingResponse_ResponseHeaders{}, responses[0].Response)

	rh := responses[0].Response.(*eppb.ProcessingResponse_ResponseHeaders)
	require.NotNil(t, rh.ResponseHeaders)
	require.NotNil(t, rh.ResponseHeaders.Response)
	// no headers should be set since there was no gateway session ID
	require.Len(t, rh.ResponseHeaders.Response.HeaderMutation.SetHeaders, 0)
}

func TestHandleResponseHeaders_404RemovesServerSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	gatewaySessionID := "gateway-session-123"
	serverName := "test-server"

	// add a session to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName, "upstream-session-456")
	require.NoError(t, err)

	// verify session exists
	sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-456", sessions[serverName])

	// request headers with gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(gatewaySessionID),
				},
			},
		},
	}

	// response headers with 404 status
	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      ":status",
					RawValue: []byte("404"),
				},
			},
		},
	}

	// create MCP request with server name
	mcpReq := &MCPRequest{
		sessionID:  gatewaySessionID,
		serverName: serverName,
		Method:     "tools/call",
	}

	responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, mcpReq)

	require.NoError(t, err)
	require.Len(t, responses, 1)

	// verify the server session was removed from cache
	sessions, err = cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Empty(t, sessions)
}

func TestHandleResponseHeaders_404WithoutMCPRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	gatewaySessionID := "gateway-session-123"

	// request headers with gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(gatewaySessionID),
				},
			},
		},
	}

	// response headers with 404 status
	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      ":status",
					RawValue: []byte("404"),
				},
			},
		},
	}

	// call with nil MCPRequest (should not panic or error)
	responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, nil)

	require.NoError(t, err)
	require.Len(t, responses, 1)
	require.IsType(t, &eppb.ProcessingResponse_ResponseHeaders{}, responses[0].Response)
}

func TestHandleResponseHeaders_404WithMultipleServerSessions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	gatewaySessionID := "gateway-session-123"
	serverName1 := "server1"
	serverName2 := "server2"

	// add multiple server sessions to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName1, "upstream-session-1")
	require.NoError(t, err)
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName2, "upstream-session-2")
	require.NoError(t, err)

	// verify both sessions exist
	sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	// request headers with gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(gatewaySessionID),
				},
			},
		},
	}

	// response headers with 404 status
	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      ":status",
					RawValue: []byte("404"),
				},
			},
		},
	}

	// create MCP request with server1
	mcpReq := &MCPRequest{
		sessionID:  gatewaySessionID,
		serverName: serverName1,
		Method:     "tools/call",
	}

	responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, mcpReq)

	require.NoError(t, err)
	require.Len(t, responses, 1)

	// verify only server1 session was removed, server2 session remains
	sessions, err = cache.GetSession(context.Background(), gatewaySessionID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "upstream-session-2", sessions[serverName2])
	_, exists := sessions[serverName1]
	require.False(t, exists)
}

func TestHandleResponseHeaders_SuccessStatusDoesNotRemoveSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	server := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	gatewaySessionID := "gateway-session-123"
	serverName := "test-server"

	// add a session to the cache
	_, err = cache.AddSession(context.Background(), gatewaySessionID, serverName, "upstream-session-456")
	require.NoError(t, err)

	// request headers with gateway session ID
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{
					Key:      "mcp-session-id",
					RawValue: []byte(gatewaySessionID),
				},
			},
		},
	}

	// test various success status codes
	successCodes := []string{"200", "201", "204"}

	for _, statusCode := range successCodes {
		t.Run("status_"+statusCode, func(t *testing.T) {
			responseHeaders := &eppb.HttpHeaders{
				Headers: &corev3.HeaderMap{
					Headers: []*corev3.HeaderValue{
						{
							Key:      ":status",
							RawValue: []byte(statusCode),
						},
					},
				},
			}

			mcpReq := &MCPRequest{
				sessionID:  gatewaySessionID,
				serverName: serverName,
				Method:     "tools/call",
			}

			responses, err := server.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, mcpReq)

			require.NoError(t, err)
			require.Len(t, responses, 1)

			// verify the session was NOT removed
			sessions, err := cache.GetSession(context.Background(), gatewaySessionID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.Equal(t, "upstream-session-456", sessions[serverName])
		})
	}
}

func TestHandleResponseHeaders_StoresElicitationForDirectInit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	srv := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	brokerSessionID := "broker-session-jwt-123"

	// no mcp-session-id (first init), no mcp-init-host (direct client request)
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{},
		},
	}

	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-session-id", RawValue: []byte(brokerSessionID)},
				{Key: ":status", RawValue: []byte("200")},
			},
		},
	}

	mcpReq := &MCPRequest{
		Method: "initialize",
		Params: map[string]any{
			"capabilities": map[string]any{
				"elicitation": map[string]any{},
			},
		},
	}

	_, err = srv.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, mcpReq)
	require.NoError(t, err)

	val, err := cache.GetClientElicitation(context.Background(), brokerSessionID)
	require.NoError(t, err)
	require.True(t, val)
}

func TestHandleResponseHeaders_SkipsElicitationForHairpinInit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cache, err := session.NewCache()
	require.NoError(t, err)

	srv := &ExtProcServer{
		Logger:       logger,
		SessionCache: cache,
		Broker:       newMockBroker(nil, map[string]string{}),
	}

	backendSessionID := "backend-session-456"

	// mcp-init-host present indicates hairpin backend init
	requestHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-init-host", RawValue: []byte("backend.example.com")},
			},
		},
	}

	responseHeaders := &eppb.HttpHeaders{
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-session-id", RawValue: []byte(backendSessionID)},
				{Key: ":status", RawValue: []byte("200")},
			},
		},
	}

	mcpReq := &MCPRequest{
		Method: "initialize",
		Params: map[string]any{
			"capabilities": map[string]any{
				"elicitation": map[string]any{},
			},
		},
	}

	_, err = srv.HandleResponseHeaders(context.Background(), responseHeaders, requestHeaders, mcpReq)
	require.NoError(t, err)

	val, err := cache.GetClientElicitation(context.Background(), backendSessionID)
	require.NoError(t, err)
	require.False(t, val)
}

func newMockBroker(svrConfigs []*config.MCPServer, tool2svr map[string]string) broker.MCPBroker {
	return &mockBrokerImpl{
		svrConfigs: svrConfigs,
		tool2svr:   tool2svr,
	}
}

// GetServerInfo implements broker.MCPBroker.
func (m *mockBrokerImpl) GetServerInfo(tool string) (*config.MCPServer, error) {
	svrName, ok := m.tool2svr[tool]
	if !ok {
		return nil, fmt.Errorf("No server for tool %q", tool)
	}

	for _, svrInfo := range m.svrConfigs {
		if svrName == svrInfo.Name {
			return svrInfo, nil
		}
	}

	return nil, fmt.Errorf("failed to get server %q for tool %q", svrName, tool)
}

// GetVirtualSeverByHeader implements broker.MCPBroker.
func (m *mockBrokerImpl) GetVirtualSeverByHeader(_ string) (config.VirtualServer, error) {
	panic("unimplemented")
}

// HandleStatusRequest implements broker.MCPBroker.
func (m *mockBrokerImpl) HandleStatusRequest(_ http.ResponseWriter, _ *http.Request) {
	panic("unimplemented")
}

// MCPServer implements broker.MCPBroker.
func (m *mockBrokerImpl) MCPServer() *server.MCPServer {
	panic("unimplemented")
}

// OnConfigChange implements broker.MCPBroker.
func (m *mockBrokerImpl) OnConfigChange(_ context.Context, _ *config.MCPServersConfig) {
	panic("unimplemented")
}

// RegisteredMCPServers implements broker.MCPBroker.
func (m *mockBrokerImpl) RegisteredMCPServers() map[config.UpstreamMCPID]*upstream.MCPManager {
	panic("unimplemented")
}

// Shutdown implements broker.MCPBroker.
func (m *mockBrokerImpl) Shutdown(_ context.Context) error {
	panic("unimplemented")
}

// ToolAnnotations implements broker.MCPBroker.
func (m *mockBrokerImpl) ToolAnnotations(_ config.UpstreamMCPID, _ string) (mcp.ToolAnnotation, bool) {
	return mcp.ToolAnnotation{}, false
}

// ValidateAllServers implements broker.MCPBroker.
func (m *mockBrokerImpl) ValidateAllServers() broker.StatusResponse {
	panic("unimplemented")
}
