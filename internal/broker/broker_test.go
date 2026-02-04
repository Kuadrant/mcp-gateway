package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/tests/server2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

const (
	// MCPPort is the port the test server should listen on (TODO make dynamic?)
	MCPPort = "8088"

	// MCPAddr is the URL the client will use to contact the test server
	MCPAddr = "http://localhost:8088/mcp"

	// MCPAddrForgetAddr is the URL the client will use to force the server to forget a session
	MCPAddrForgetAddr = "http://localhost:8088/admin/forget"
)

var logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

// TestMain starts an MCP server that we will run actual tests against
func TestMain(m *testing.M) {
	// Start an MCP server to test our broker client logic
	startFunc, shutdownFunc, err := server2.RunServer("http", MCPPort)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Server setup error: %v\n", err)
		os.Exit(1)
	}

	go func() {
		// Start the server in a Goroutine
		_ = startFunc()
	}()

	// wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	code := m.Run()

	err = shutdownFunc()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server shutdown error: %v\n", err)
		// Don't fail if the server doesn't shut down; it might have open clients
		// os.Exit(1)
	}

	os.Exit(code)
}

func TestOnConfigChange(t *testing.T) {
	b := NewBroker(logger)
	conf := &config.MCPServersConfig{}
	server1 := &config.MCPServer{
		Name:       "test1",
		URL:        MCPAddr,
		ToolPrefix: "_test1",
	}
	virtualServer1 := &config.VirtualServer{
		Name:  "test/test",
		Tools: []string{"test"},
	}
	b.OnConfigChange(context.TODO(), conf)
	servers := b.RegisteredMCPServers()
	require.Equal(t, 0, len(servers))
	if _, ok := servers[server1.ID()]; ok {
		t.Fatalf("expected server 1 not to be registered")
	}

	conf.Servers = append(conf.Servers, server1)
	conf.VirtualServers = append(conf.VirtualServers, virtualServer1)
	b.OnConfigChange(context.TODO(), conf)
	servers = b.RegisteredMCPServers()
	require.Equal(t, 1, len(servers))
	if _, ok := servers[server1.ID()]; !ok {
		t.Fatalf("expected server 1 to be registered")
	}

	vs, err := b.GetVirtualSeverByHeader("test/test")
	require.Nil(t, err, "error should be nil from GetVirtualSeverByHeader")
	if vs.Name != "test/test" {
		t.Fatalf("expected virtual server to have same name")
	}
	if len(vs.Tools) != 1 && vs.Tools[0] != "test" {
		t.Fatalf("expected the virtual server to have the test tool listed")
	}

	conf.Servers = []*config.MCPServer{}
	b.OnConfigChange(context.TODO(), conf)
	servers = b.RegisteredMCPServers()
	require.Equal(t, 0, len(servers))
	if _, ok := servers[server1.ID()]; ok {
		t.Fatalf("expected server 1 not to be registered")
	}

	_ = b.Shutdown(context.Background())
}

var _ http.ResponseWriter = &simpleResponseWriter{}

type simpleResponseWriter struct {
	Status  int
	Body    []byte
	Headers []http.Header
}

func (srw *simpleResponseWriter) Header() http.Header {
	h := http.Header{}
	srw.Headers = append(srw.Headers, h)
	return h
}

func (srw *simpleResponseWriter) WriteHeader(status int) {
	srw.Status = status
}
func (srw *simpleResponseWriter) Write(b []byte) (int, error) {
	srw.Body = b
	return len(b), nil
}

func TestOauthResourceHandler(t *testing.T) {
	var (
		resourceName = "mcp gateway"
		resource     = "https://test.com/mcp"
		idp          = "https://idp.com"
		bearerMethod = "header"
		scopes       = "groups,audience,roles"
	)
	t.Setenv(envOAuthResourceName, resourceName)
	t.Setenv(envOAuthResource, resource)
	t.Setenv(envOAuthAuthorizationServers, idp)
	t.Setenv(envOAuthBearerMethodsSupported, bearerMethod)
	t.Setenv(envOAuthScopesSupported, scopes)

	r := &http.Request{
		Method: http.MethodGet,
	}
	pr := &ProtectedResourceHandler{Logger: logger}
	recorder := &simpleResponseWriter{}
	pr.Handle(recorder, r)
	if recorder.Status != 200 {
		t.Fatalf("expected 200 status code got %v", recorder.Status)
	}
	config := &OAuthProtectedResource{}
	if err := json.Unmarshal(recorder.Body, config); err != nil {
		t.Fatalf("unexpected error %s", err)
	}
	if !slices.Contains(config.AuthorizationServers, idp) {
		t.Fatalf("expected %s to be in %v", idp, config.AuthorizationServers)
	}
	if config.Resource != resource {
		t.Fatalf("expected resource to be %s but was %s", resource, config.Resource)
	}
	if config.ResourceName != resourceName {
		t.Fatalf("expected resource to be %s but was %s", resourceName, config.ResourceName)
	}
	if !slices.ContainsFunc(config.ScopesSupported, func(val string) bool {
		return slices.Contains(strings.Split(scopes, ","), val)
	}) {
		t.Fatalf("expected %s to be in %v", scopes, config.ScopesSupported)
	}
	if !slices.Contains(config.BearerMethodsSupported, bearerMethod) {
		t.Fatalf("expected %s to be in %v", bearerMethod, config.BearerMethodsSupported)
	}

}

func TestGetServerInfo(t *testing.T) {
	b := NewBroker(logger)

	// Attach phony tools to the upstreams
	bImpl, ok := b.(*mcpBrokerImpl)
	require.True(t, ok)
	bImpl.mcpServers["test1"] = createTestManager(t, "test1", "", []mcp.Tool{
		mcp.NewTool("pour_chocolate"),
	})
	bImpl.mcpServers["test2"] = createTestManager(t, "test2", "", []mcp.Tool{
		mcp.NewTool("restore_from_tape"),
	})
	bImpl.mcpServers["test3"] = createTestManager(t, "test3", "t", []mcp.Tool{
		mcp.NewTool("restore_from_tape"),
	})
	bImpl.mcpServers["test4"] = createTestManager(t, "test4", "tt", []mcp.Tool{})

	svr, err := b.GetServerInfo("pour_chocolate")
	require.NotNil(t, svr)
	require.NoError(t, err)
	require.Equal(t, "test1", svr.Name)

	svr, err = b.GetServerInfo("restore_from_tape")
	require.NotNil(t, svr)
	require.NoError(t, err)
	require.Equal(t, "test2", svr.Name)

	// We used a prefix so that this tool exists
	svr, err = b.GetServerInfo("trestore_from_tape")
	require.NotNil(t, svr)
	require.NoError(t, err)
	require.Equal(t, "test3", svr.Name)

	// There is no tool, even though the prefix matches
	svr, err = b.GetServerInfo("tt_orbit_mars")
	require.Error(t, err)
	require.Nil(t, svr)
}

func TestToolAnnotations(t *testing.T) {
	b := NewBroker(logger,
		WithEnforceToolFilter(true),
		WithManagerTickerInterval(time.Microsecond),
		WithTrustedHeadersPublicKey("abc"))
	require.NotNil(t, b)
	require.NotNil(t, b.MCPServer())

	// Attach phony tools to the upstreams
	bImpl, ok := b.(*mcpBrokerImpl)
	require.True(t, ok)
	bImpl.mcpServers["test1"] = createTestManager(t, "test1", "", []mcp.Tool{
		mcp.NewTool("get_status", mcp.WithToolAnnotation(mcp.ToolAnnotation{
			ReadOnlyHint:   mcp.ToBoolPtr(true),
			IdempotentHint: mcp.ToBoolPtr(true),
		})),
		mcp.NewTool("pour_chocolate", mcp.WithToolAnnotation(mcp.ToolAnnotation{
			ReadOnlyHint:   mcp.ToBoolPtr(false),
			IdempotentHint: mcp.ToBoolPtr(false),
		})),
	})

	testCases := []struct {
		name       string
		serverName config.UpstreamMCPID
		toolName   string
		shouldFail bool
		readOnly   bool
		idempotent bool
	}{
		{
			name:       "status tool",
			serverName: "test1",
			toolName:   "get_status",
			shouldFail: false,
			readOnly:   true,
			idempotent: true,
		},
		{
			name:       "pour tool",
			serverName: "test1",
			toolName:   "pour_chocolate",
			shouldFail: false,
			readOnly:   false,
			idempotent: false,
		},
		{
			name:       "invalid tool",
			serverName: "test1",
			toolName:   "plant_rutabaga",
			shouldFail: true,
		},
		{
			name:       "invalid server",
			serverName: "miami",
			toolName:   "get_status",
			shouldFail: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			annotation, exists := b.ToolAnnotations(tc.serverName, tc.toolName)
			if tc.shouldFail {
				require.False(t, exists, "expected no annotation to be found")
				return
			}
			require.True(t, exists, "expected annotation to be found")
			require.Equal(t, tc.readOnly, *annotation.ReadOnlyHint, "readOnly mismatch: %#v", annotation)
			require.Equal(t, tc.idempotent, *annotation.IdempotentHint, "idempotent mismatch: %#v", annotation)
		})
	}
}
