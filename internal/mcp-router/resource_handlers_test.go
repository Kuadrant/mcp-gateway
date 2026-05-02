package mcprouter

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestMCPRequest_ResourceURI(t *testing.T) {
	cases := []struct {
		name string
		req  *MCPRequest
		want string
	}{
		{
			name: "extracts string uri",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  methodResourcesRead,
				Params:  map[string]any{"uri": "weather_+file:///x"},
			},
			want: "weather_+file:///x",
		},
		{
			name: "non-resources/read returns empty",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  "tools/call",
				Params:  map[string]any{"uri": "should-be-ignored"},
			},
			want: "",
		},
		{
			name: "missing uri param returns empty",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  methodResourcesRead,
				Params:  map[string]any{},
			},
			want: "",
		},
		{
			name: "non-string uri returns empty",
			req: &MCPRequest{
				JSONRPC: "2.0",
				Method:  methodResourcesRead,
				Params:  map[string]any{"uri": 42},
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, c.req.ResourceURI())
		})
	}
}

func TestMCPRequest_ReWriteResourceURI(t *testing.T) {
	req := &MCPRequest{
		JSONRPC: "2.0",
		Method:  methodResourcesRead,
		Params:  map[string]any{"uri": "weather_+file:///x"},
	}
	req.ReWriteResourceURI("file:///x")
	require.Equal(t, "file:///x", req.Params["uri"])
}

func TestHeadersBuilder_WithMCPResourceURI(t *testing.T) {
	hb := NewHeaders().WithMCPResourceURI("file:///forecast.json")
	headers := hb.Build()
	require.Len(t, headers, 1)
	require.Equal(t, resourceURIHeader, headers[0].Header.Key)
	require.Equal(t, []byte("file:///forecast.json"), headers[0].Header.RawValue)
}

func TestHandleResourceRead_StripsPrefixAndSetsHeaders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	jwtManager, err := session.NewJWTManager()
	require.NoError(t, err)
	cache, err := session.NewCache()
	require.NoError(t, err)

	validToken, err := jwtManager.NewToken()
	require.NoError(t, err)

	// pre-seed a backend session so HandleResourceRead does not try to initialize
	added, err := cache.AddSession(context.Background(), validToken, "weather", "mock-backend-session-id")
	require.NoError(t, err)
	require.True(t, added)

	mockInitForClient := func(_ context.Context, _, _ string, _ *config.MCPServer, _ map[string]string, _ bool) (*client.Client, error) {
		return nil, fmt.Errorf("InitForClient should not be called when session exists")
	}

	serverConfigs := []*config.MCPServer{
		{
			Name:       "weather",
			URL:        "http://localhost:8080/mcp",
			ToolPrefix: "weather_",
			Enabled:    true,
			Hostname:   "localhost",
		},
	}

	srv := &ExtProcServer{
		RoutingConfig: &config.MCPServersConfig{Servers: serverConfigs},
		JWTManager:    jwtManager,
		Logger:        logger,
		SessionCache:  cache,
		InitForClient: mockInitForClient,
		Broker: &mockBrokerImpl{
			svrConfigs: serverConfigs,
			uri2svr: map[string]string{
				"weather_+file:///forecast.json": "weather",
			},
		},
	}

	data := &MCPRequest{
		ID:      ptr.To(0),
		JSONRPC: "2.0",
		Method:  methodResourcesRead,
		Params: map[string]any{
			"uri": "weather_+file:///forecast.json",
		},
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-session-id", RawValue: []byte(validToken)},
			},
		},
	}

	resp := srv.RouteMCPRequest(context.Background(), data)
	require.Len(t, resp, 1)
	rb, ok := resp[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.True(t, ok, "expected request_body response")
	require.NotNil(t, rb.RequestBody.Response)

	headers := rb.RequestBody.Response.HeaderMutation.SetHeaders
	require.Len(t, headers, 7)
	require.Equal(t, "x-mcp-method", headers[0].Header.Key)
	require.Equal(t, []byte(methodResourcesRead), headers[0].Header.RawValue)
	require.Equal(t, "x-mcp-resource-uri", headers[1].Header.Key)
	require.Equal(t, []byte("file:///forecast.json"), headers[1].Header.RawValue)
	require.Equal(t, "x-mcp-servername", headers[2].Header.Key)
	require.Equal(t, []byte("weather"), headers[2].Header.RawValue)
	require.Equal(t, "mcp-session-id", headers[3].Header.Key)
	require.Equal(t, []byte("mock-backend-session-id"), headers[3].Header.RawValue)
	require.Equal(t, ":authority", headers[4].Header.Key)
	require.Equal(t, []byte("localhost"), headers[4].Header.RawValue)
	require.Equal(t, ":path", headers[5].Header.Key)
	require.Equal(t, []byte("/mcp"), headers[5].Header.RawValue)

	body := string(rb.RequestBody.Response.BodyMutation.GetBody())
	require.Contains(t, body, `"method":"resources/read"`)
	require.Contains(t, body, `"uri":"file:///forecast.json"`)
	require.NotContains(t, body, "weather_+", "federated prefix must be stripped before forwarding upstream")
}

func TestHandleResourceRead_UnknownURIReturnsJSONRPCError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	jwtManager, err := session.NewJWTManager()
	require.NoError(t, err)
	cache, err := session.NewCache()
	require.NoError(t, err)
	validToken, err := jwtManager.NewToken()
	require.NoError(t, err)

	emap, err := idmap.New()
	require.NoError(t, err)
	srv := &ExtProcServer{
		RoutingConfig:  &config.MCPServersConfig{},
		JWTManager:     jwtManager,
		Logger:         logger,
		SessionCache:   cache,
		Broker:         &mockBrokerImpl{},
		ElicitationMap: emap,
	}

	data := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  methodResourcesRead,
		Params:  map[string]any{"uri": "weather_+file:///nope"},
		Headers: &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{
				{Key: "mcp-session-id", RawValue: []byte(validToken)},
			},
		},
	}

	resp := srv.RouteMCPRequest(context.Background(), data)
	require.NotEmpty(t, resp)
	// the immediate response carries the SSE-framed JSON-RPC -32002 error
	ir, ok := resp[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
	require.True(t, ok, "expected immediate response")
	body := string(ir.ImmediateResponse.Body)
	require.Contains(t, body, `"code":-32002`)
	require.Contains(t, body, "Resource not found")
}
