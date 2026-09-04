package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

type testRouteOpts struct {
	stateless bool
	stateful  bool
}

func newTestRouter202607(t *testing.T, serverConfigs []*config.MCPServer, toolMap map[string]string, promptMap map[string]string) *Router202607 {
	return newTestRouter202607WithOpts(t, serverConfigs, toolMap, promptMap, nil)
}

func newTestRouter202607WithOpts(t *testing.T, serverConfigs []*config.MCPServer, toolMap map[string]string, promptMap map[string]string, opts map[string]*testRouteOpts) *Router202607 {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	builder := NewTableBuilder()
	for tool, svrName := range toolMap {
		for _, svr := range serverConfigs {
			if svr.Name == svrName {
				path, _ := svr.Path()
				route := &ServerRoute{
					Name:                svr.Name,
					Host:                svr.Hostname,
					Prefix:              svr.Prefix,
					Path:                path,
					URL:                 svr.URL,
					GuardrailsConfigIDs: svr.GuardrailsConfigIDs,
					Stateless:           true,
				}
				if o, ok := opts[svr.Name]; ok {
					route.Stateless = o.stateless
				}
				builder.AddTool(tool, route)
			}
		}
	}
	for prompt, svrName := range promptMap {
		for _, svr := range serverConfigs {
			if svr.Name == svrName {
				path, _ := svr.Path()
				route := &ServerRoute{
					Name:      svr.Name,
					Host:      svr.Hostname,
					Prefix:    svr.Prefix,
					Path:      path,
					URL:       svr.URL,
					Stateless: true,
				}
				if o, ok := opts[svr.Name]; ok {
					route.Stateless = o.stateless
				}
				builder.AddPrompt(prompt, route)
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
		Name:      "github",
		Host:      "github.mcp",
		Prefix:    "gh_",
		Path:      "/mcp",
		URL:       "http://github.mcp:8080/mcp",
		Stateless: true,
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
		Name:      "annotated",
		Host:      "localhost",
		Path:      "/mcp",
		URL:       "http://localhost:8080/mcp",
		Stateless: true,
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

// protocol version is implicit: the ext_proc adapter selects Router202607 for
// 2026 clients, so no version header appears in the request.
func TestRouter202607_StatefulOnlyToolRejected(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "stateful-backend",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607WithOpts(t, serverConfigs,
		map[string]string{"mytool": "stateful-backend"},
		map[string]string{},
		map[string]*testRouteOpts{"stateful-backend": {stateless: false}},
	)

	req := &Request{
		MCPMethod: MethodToolCall,
		MCPName:   "mytool",
		RequestID: "req-1",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.NotNil(t, decision.Error)
	require.Equal(t, 200, decision.Error.StatusCode)
	require.Contains(t, decision.Error.JSONRPCErr, "-32602")
	require.Contains(t, decision.Error.JSONRPCErr, "Tool not found")
	require.Contains(t, decision.Error.JSONRPCErr, `"id":"req-1"`)
}

func TestRouter202607_StatefulOnlyPromptRejected(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:     "stateful-backend",
			URL:      "http://localhost:8080/mcp",
			State:    "Enabled",
			Hostname: "localhost",
		},
	}

	router := newTestRouter202607WithOpts(t, serverConfigs,
		map[string]string{},
		map[string]string{"myprompt": "stateful-backend"},
		map[string]*testRouteOpts{"stateful-backend": {stateless: false}},
	)

	req := &Request{
		MCPMethod: MethodPromptGet,
		MCPName:   "myprompt",
		RequestID: "req-2",
	}

	decision := router.RouteRequest(context.Background(), req)
	require.NotNil(t, decision.Error)
	require.Equal(t, 200, decision.Error.StatusCode)
	require.Contains(t, decision.Error.JSONRPCErr, "-32602")
	require.Contains(t, decision.Error.JSONRPCErr, "Prompt not found")
	require.Contains(t, decision.Error.JSONRPCErr, `"id":"req-2"`)
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

func TestRouter202607_Guardrails(t *testing.T) {
	serverConfigs := []*config.MCPServer{
		{
			Name:                "dummy",
			URL:                 "http://localhost:8080/mcp",
			Prefix:              "s_",
			State:               "Enabled",
			Hostname:            "localhost",
			GuardrailsConfigIDs: []string{"svr-1"},
		},
	}

	toolReq := func() *Request {
		return &Request{
			MCPMethod: MethodToolCall,
			MCPName:   "s_mytool",
			RequestID: "req-1",
			Parsed: &MCPRequest{
				ID:      ptr.To(1),
				JSONRPC: "2.0",
				Method:  MethodToolCall,
				Params: map[string]any{
					"name":      "s_mytool",
					"arguments": map[string]any{"query": "SELECT 1"},
				},
			},
		}
	}

	t.Run("allowed proceeds and uses unprefixed tool name", func(t *testing.T) {
		router := newTestRouter202607(t, serverConfigs, map[string]string{"s_mytool": "dummy"}, map[string]string{})
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed}}
		cfg := &config.MCPServersConfig{
			Servers:          serverConfigs,
			GlobalGuardrails: &config.GuardrailsConfig{ConfigIDs: []string{"global-1"}},
		}
		cfg.SetGuardrailsChecker(fc)
		router.RoutingConfig.Store(cfg)
		decision := router.RouteRequest(context.Background(), toolReq())
		require.Nil(t, decision.Error)
		require.Equal(t, "localhost", decision.Authority)
		require.Equal(t, 1, fc.calls)
		require.Equal(t, "mytool", fc.lastToolName)
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("blocked does not reach upstream", func(t *testing.T) {
		router := newTestRouter202607(t, serverConfigs, map[string]string{"s_mytool": "dummy"}, map[string]string{})
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusBlocked, Reason: "sql-injection"}}
		cfg := &config.MCPServersConfig{
			Servers:          serverConfigs,
			GlobalGuardrails: &config.GuardrailsConfig{ConfigIDs: []string{"global-1"}},
		}
		cfg.SetGuardrailsChecker(fc)
		router.RoutingConfig.Store(cfg)
		decision := router.RouteRequest(context.Background(), toolReq())
		require.NotNil(t, decision.Error)
		require.Equal(t, 403, decision.Error.StatusCode)
		require.Empty(t, decision.Authority)
		require.Equal(t, "application/json", decision.Error.ContentType)
		require.Contains(t, decision.Error.JSONRPCErr, guardrailsBlockedMessage)
		require.NotContains(t, decision.Error.JSONRPCErr, "sql-injection", "the triggering rail must not reach the client")
	})

	t.Run("modified arguments are forwarded in the body mutation", func(t *testing.T) {
		router := newTestRouter202607(t, serverConfigs, map[string]string{"s_mytool": "dummy"}, map[string]string{})
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusModified, Content: `{"query":"SELECT sanitized"}`}}
		cfg := &config.MCPServersConfig{
			Servers:          serverConfigs,
			GlobalGuardrails: &config.GuardrailsConfig{ConfigIDs: []string{"global-1"}},
		}
		cfg.SetGuardrailsChecker(fc)
		router.RoutingConfig.Store(cfg)
		decision := router.RouteRequest(context.Background(), toolReq())
		require.Nil(t, decision.Error)
		require.Equal(t, "localhost", decision.Authority)
		require.Equal(t, 1, fc.calls)
		require.Equal(t, "mytool", fc.lastToolName)
		require.NotEmpty(t, decision.BodyMutation)
		require.Equal(t, fmt.Sprintf("%d", len(decision.BodyMutation)), decision.SetHeaders["content-length"])

		var restored MCPRequest
		require.NoError(t, json.Unmarshal(decision.BodyMutation, &restored))
		require.Equal(t, "mytool", restored.Params["name"])
		require.Equal(t, map[string]any{"query": "SELECT sanitized"}, restored.Params["arguments"])
	})
}
