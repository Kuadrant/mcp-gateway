package routing

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func newTestRouter202607(t *testing.T, serverConfigs []*config.MCPServer, toolMap map[string]string, promptMap map[string]string) *Router202607 {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	builder := NewTableBuilder()
	for tool, svrName := range toolMap {
		for _, svr := range serverConfigs {
			if svr.Name == svrName {
				path, _ := svr.Path()
				route := &ServerRoute{
					Name:   svr.Name,
					Host:   svr.Hostname,
					Prefix: svr.Prefix,
					Path:   path,
					URL:    svr.URL,
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

	router := &Router202607{
		RoutingConfig: &routingConfig,
		Table:         func() RoutingTable { return table },
		Logger:        logger,
	}

	return router
}

func TestRouter202607_ToolCallWithoutPrefix(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{"mytool": "plain"}, map[string]string{})

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "mytool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.Equal(t, "localhost", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)
	require.Equal(t, "tools/call", decision.SetHeaders[MethodHeader])
	require.Equal(t, "mytool", decision.SetHeaders[ToolHeader])
	require.Equal(t, "plain", decision.SetHeaders[MCPServerNameHeader])
	require.Nil(t, decision.BodyMutation)
	require.Contains(t, decision.UnsetHeaders, MCPAuthorizedHeader)
	require.Contains(t, decision.UnsetHeaders, MCPVirtualServerHeader)
}

func TestRouter202607_ToolCallWithPrefix(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "prefixed",
			URL:      "http://localhost:8080/mcp",
			Prefix:   "s_",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{"s_mytool": "prefixed"}, map[string]string{})

	parsed := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  map[string]any{"name": "s_mytool", "arguments": map[string]any{"key": "val"}},
	}

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "s_mytool",
		RequestID: "req-1",
		Parsed:    parsed,
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.Equal(t, "localhost", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)
	require.Equal(t, "mytool", decision.SetHeaders[ToolHeader])
	require.Equal(t, "prefixed", decision.SetHeaders[MCPServerNameHeader])
	require.NotNil(t, decision.BodyMutation)
	require.Contains(t, string(decision.BodyMutation), `"name":"mytool"`)
	require.NotContains(t, string(decision.BodyMutation), `"name":"s_mytool"`)
}

func TestRouter202607_HeaderBodyMismatch(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{"actual_name": "plain"}, map[string]string{})

	parsed := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  map[string]any{"name": "different_name"},
	}

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "actual_name",
		RequestID: "req-1",
		Parsed:    parsed,
	}

	decision := router.RouteRequest(context.Background(), req)
	require.NotNil(t, decision.Error)
	require.Equal(t, 200, decision.Error.StatusCode)
	require.Contains(t, decision.Error.JSONRPCErr, "HeaderMismatch")
	require.Contains(t, decision.Error.JSONRPCErr, "-32602")
}

func TestRouter202607_PromptGet(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "prompts",
			URL:      "http://localhost:8080/mcp",
			Prefix:   "s_",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{"s_myprompt": "prompts"})

	parsed := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "prompts/get",
		Params:  map[string]any{"name": "s_myprompt"},
	}

	req := &Request{
		MCPMethod: MethodPromptGet,
		MCPName:   "s_myprompt",
		RequestID: "req-1",
		Parsed:    parsed,
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.Equal(t, "localhost", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)
	require.Equal(t, "prompts/get", decision.SetHeaders[MethodHeader])
	require.Equal(t, "myprompt", decision.SetHeaders[PromptHeader])
	require.Equal(t, "prompts", decision.SetHeaders[MCPServerNameHeader])
	require.NotNil(t, decision.BodyMutation)
	require.Contains(t, string(decision.BodyMutation), `"name":"myprompt"`)
}

func TestRouter202607_UnknownTool(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "unknown_tool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error, "unknown tools route to broker, not rejected by router")
	require.True(t, decision.BrokerPass, "unknown tools should pass through to broker")
	require.Equal(t, "mcpBroker", decision.SetHeaders[MCPServerNameHeader])
}

func TestRouter202607_BrokerMetaTool(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	builder := NewTableBuilder()
	builder.AddBrokerTool("broker_tool")
	table := builder.Build()
	router.Table = func() RoutingTable { return table }

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "broker_tool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.True(t, decision.BrokerPass)
	require.Equal(t, "mcpBroker", decision.SetHeaders[MCPServerNameHeader])
	require.Equal(t, "tools/call", decision.SetHeaders[MethodHeader])
}

func TestRouter202607_NonToolMethod(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	req := &Request{
		MCPMethod: "tools/list",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.True(t, decision.BrokerPass)
	require.Equal(t, "mcpBroker", decision.SetHeaders[MCPServerNameHeader])
	require.Equal(t, "tools/list", decision.SetHeaders[MethodHeader])
}

func TestRouter202607_PrefixFallback(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "github",
			URL:      "http://github.mcp:8080/mcp",
			Prefix:   "gh_",
			State:    "Enabled",
			Hostname: "github.mcp",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	builder := NewTableBuilder()
	builder.AddPrefix("gh_", &ServerRoute{
		Name:   "github",
		Host:   "github.mcp",
		Prefix: "gh_",
		Path:   "/mcp",
		URL:    "http://github.mcp:8080/mcp",
	})
	table := builder.Build()
	router.Table = func() RoutingTable { return table }

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "gh_user_tool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.Equal(t, "github.mcp", decision.Authority)
	require.Equal(t, "/mcp", decision.Path)
	require.Equal(t, "user_tool", decision.SetHeaders[ToolHeader])
	require.Equal(t, "github", decision.SetHeaders[MCPServerNameHeader])
}

func TestRouter202607_ToolAnnotations(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "annotated",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	builder := NewTableBuilder()
	route := &ServerRoute{
		Name: "annotated",
		Host: "localhost",
		Path: "/mcp",
		URL:  "http://localhost:8080/mcp",
	}
	builder.AddTool("mytool", route)
	builder.AddAnnotation("annotated::localhost", "mytool", &ToolAnnotation{
		ReadOnlyHint:    ptr.To(true),
		DestructiveHint: ptr.To(false),
		IdempotentHint:  nil,
		OpenWorldHint:   ptr.To(true),
	})
	table := builder.Build()
	router.Table = func() RoutingTable { return table }

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "mytool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.Equal(t, "localhost", decision.Authority)
	require.Equal(t, "readOnly=true,destructive=false,idempotent=unspecified,openWorld=true", decision.SetHeaders[ToolAnnotationsHeader])
}

func TestRouter202607_EmptyToolName(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.NotNil(t, decision.Error)
	require.Equal(t, 400, decision.Error.StatusCode)
	require.Equal(t, "no tool name set", decision.Error.Message)
}

func TestRouter202607_EmptyPromptName(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	req := &Request{
		MCPMethod: MethodPromptGet,
		MCPName:   "",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.NotNil(t, decision.Error)
	require.Equal(t, 400, decision.Error.StatusCode)
	require.Equal(t, "no prompt name set", decision.Error.Message)
}

func TestRouter202607_BrokerPassthroughReInjectsInternalHeaders(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "plain",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607(t, serverConfigs, map[string]string{}, map[string]string{})

	parsed := &MCPRequest{
		ID:      ptr.To(1),
		JSONRPC: "2.0",
		Method:  "tools/list",
		Headers: map[string]string{
			MCPAuthorizedHeader:    "signed-jwt",
			MCPVirtualServerHeader: "test/vs",
		},
	}

	req := &Request{
		MCPMethod: "tools/list",
		RequestID: "req-1",
		Parsed:    parsed,
	}

	decision := router.RouteRequest(context.Background(), req)
	require.Nil(t, decision.Error)
	require.True(t, decision.BrokerPass)
	require.Equal(t, "signed-jwt", decision.SetHeaders[MCPAuthorizedHeader])
	require.Equal(t, "test/vs", decision.SetHeaders[MCPVirtualServerHeader])
}
