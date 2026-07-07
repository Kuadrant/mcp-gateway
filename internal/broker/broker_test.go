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

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/tests/server2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// mockGateway is a no-op ToolsAdderDeleter/PromptsAdderDeleter for tests that don't need real gateway behaviour.
type mockGateway struct{}

func newMockGateway() *mockGateway                                     { return &mockGateway{} }
func (m *mockGateway) AddTools(_ ...upstream.GatewayTool)              {}
func (m *mockGateway) DeleteTools(_ ...string)                         {}
func (m *mockGateway) ListTools() map[string]*upstream.GatewayTool     { return nil }
func (m *mockGateway) AddPrompts(_ ...upstream.GatewayPrompt)          {}
func (m *mockGateway) DeletePrompts(_ ...string)                       {}
func (m *mockGateway) ListPrompts() map[string]*upstream.GatewayPrompt { return nil }

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
		Name:   "test1",
		URL:    MCPAddr,
		Prefix: "test1_",
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

	vs, err := b.GetVirtualServerByHeader("test/test")
	require.Nil(t, err, "error should be nil from GetVirtualServerByHeader")
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

func TestOnConfigChange_VirtualServerRemoval(t *testing.T) {
	b := NewBroker(logger)

	vs1 := &config.VirtualServer{Name: "ns/vs-one", Tools: []string{"tool_a"}}
	vs2 := &config.VirtualServer{Name: "ns/vs-two", Tools: []string{"tool_b"}}

	// register both virtual servers
	conf := &config.MCPServersConfig{}
	conf.VirtualServers = []*config.VirtualServer{vs1, vs2}
	b.OnConfigChange(context.TODO(), conf)

	_, err := b.GetVirtualServerByHeader("ns/vs-one")
	require.NoError(t, err, "vs-one should be present after first config")
	_, err = b.GetVirtualServerByHeader("ns/vs-two")
	require.NoError(t, err, "vs-two should be present after first config")

	// remove vs-one from the config (simulates MCPVirtualServer deletion)
	conf.VirtualServers = []*config.VirtualServer{vs2}
	b.OnConfigChange(context.TODO(), conf)

	_, err = b.GetVirtualServerByHeader("ns/vs-one")
	require.Error(t, err, "vs-one should be removed after config update")

	remaining, err := b.GetVirtualServerByHeader("ns/vs-two")
	require.NoError(t, err, "vs-two should still be present")
	require.Equal(t, "ns/vs-two", remaining.Name)

	// remove all virtual servers
	conf.VirtualServers = []*config.VirtualServer{}
	b.OnConfigChange(context.TODO(), conf)

	_, err = b.GetVirtualServerByHeader("ns/vs-two")
	require.Error(t, err, "vs-two should be removed after empty config")
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
	bImpl.mcpServers["test1"] = upstream.NewActiveForTesting(createTestManager(t, "test1", "", []mcp.Tool{
		{Name: "pour_chocolate"},
	}))
	bImpl.mcpServers["test2"] = upstream.NewActiveForTesting(createTestManager(t, "test2", "", []mcp.Tool{
		{Name: "restore_from_tape"},
	}))
	bImpl.mcpServers["test3"] = upstream.NewActiveForTesting(createTestManager(t, "test3", "t", []mcp.Tool{
		{Name: "restore_from_tape"},
	}))
	bImpl.mcpServers["test4"] = upstream.NewActiveForTesting(createTestManager(t, "test4", "tt", []mcp.Tool{}))

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

func TestGetServerInfo_UserSpecificLongestPrefix(t *testing.T) {
	b := NewBroker(logger)
	bImpl, ok := b.(*mcpBrokerImpl)
	require.True(t, ok)

	// two user-specific servers with overlapping prefixes
	bImpl.mcpServers["short"] = upstream.NewActiveForTesting(createTestManager(t, "short", "gh_", []mcp.Tool{}))
	bImpl.mcpServers["long"] = upstream.NewActiveForTesting(createTestManager(t, "long", "gh_repos_", []mcp.Tool{}))

	// mark both as user-specific so prefix fallback is used
	for id, srv := range bImpl.mcpServers {
		cfg := srv.Config()
		cfg.UserSpecificList = true
		bImpl.mcpServers[id] = upstream.NewActiveForTesting(createTestManagerUserSpecific(t, cfg))
	}

	svr, err := b.GetServerInfo("gh_repos_search")
	require.NoError(t, err)
	require.NotNil(t, svr)
	require.Equal(t, "long", svr.Name, "should match longest prefix gh_repos_ not gh_")

	svr, err = b.GetServerInfo("gh_stars")
	require.NoError(t, err)
	require.NotNil(t, svr)
	require.Equal(t, "short", svr.Name, "should match gh_ when gh_repos_ doesn't match")
}

// createTestManagerMCP is createTestManager but also returns the underlying
// MCPServer so tests can seed tool hints.
func createTestManagerMCP(t *testing.T, serverName, prefix string, tools []mcp.Tool) (*upstream.MCPServer, *upstream.MCPManager) {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:   serverName,
		Prefix: prefix,
		URL:    "http://test.local/mcp",
	}, "")
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	manager.SetToolsForTesting(tools)
	return mcpServer, manager
}

func createTestManagerUserSpecific(t *testing.T, cfg config.MCPServer) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&cfg, "")
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	return manager
}

func TestToolHints(t *testing.T) {
	b := NewBroker(logger,
		WithEnforceCapabilityFilter(true),
		WithManagerTickerInterval(time.Microsecond),
		WithTrustedHeadersPublicKey("abc"))
	require.NotNil(t, b)
	require.NotNil(t, b.MCPServer())

	// Attach phony tools to the upstreams
	bImpl, ok := b.(*mcpBrokerImpl)
	require.True(t, ok)
	mcpServer, manager := createTestManagerMCP(t, "test1", "", []mcp.Tool{
		{Name: "get_status"},
		{Name: "pour_chocolate"},
		{Name: "check_gauge"},
	})
	// hints as harvested from the raw upstream tools/list: explicit values
	// and absent (nil) hints must both survive
	boolPtr := func(v bool) *bool { return &v }
	mcpServer.SetToolHintsForTesting(map[string]upstream.ToolHints{
		"get_status": {
			ReadOnlyHint:   boolPtr(true),
			IdempotentHint: boolPtr(true),
			Raw:            []byte(`{"readOnlyHint":true,"idempotentHint":true}`),
		},
		"pour_chocolate": {
			ReadOnlyHint:   boolPtr(false),
			IdempotentHint: boolPtr(false),
			Raw:            []byte(`{"readOnlyHint":false,"idempotentHint":false}`),
		},
		// check_gauge has no harvested hints: all unspecified
	})
	bImpl.mcpServers["test1"] = upstream.NewActiveForTesting(manager)

	testCases := []struct {
		name       string
		serverName config.UpstreamMCPID
		toolName   string
		shouldFail bool
		readOnly   *bool
		idempotent *bool
	}{
		{
			name:       "status tool explicit true",
			serverName: "test1",
			toolName:   "get_status",
			readOnly:   boolPtr(true),
			idempotent: boolPtr(true),
		},
		{
			name:       "pour tool explicit false",
			serverName: "test1",
			toolName:   "pour_chocolate",
			readOnly:   boolPtr(false),
			idempotent: boolPtr(false),
		},
		{
			name:       "served tool without hints is all-unspecified",
			serverName: "test1",
			toolName:   "check_gauge",
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
			hints, exists := b.ToolAnnotations(tc.serverName, tc.toolName)
			if tc.shouldFail {
				require.False(t, exists, "expected no hints to be found")
				return
			}
			require.True(t, exists, "expected hints to be found")
			require.Equal(t, tc.readOnly, hints.ReadOnlyHint, "readOnly mismatch: %#v", hints)
			require.Equal(t, tc.idempotent, hints.IdempotentHint, "idempotent mismatch: %#v", hints)
			require.Nil(t, hints.DestructiveHint)
			require.Nil(t, hints.OpenWorldHint)
		})
	}
}

func TestIsReady(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(b *mcpBrokerImpl)
		expected bool
	}{
		{
			name:     "no servers configured",
			setup:    func(_ *mcpBrokerImpl) {},
			expected: true,
		},
		{
			name: "servers configured, none healthy",
			setup: func(b *mcpBrokerImpl) {
				mgr := createTestManager(t, "s1", "", nil)
				mgr.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: false})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(mgr)
			},
			expected: false,
		},
		{
			name: "one unhealthy one healthy",
			setup: func(b *mcpBrokerImpl) {
				m1 := createTestManager(t, "s1", "", nil)
				m1.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: false})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(m1)
				m2 := createTestManager(t, "s2", "", nil)
				m2.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s2", Ready: true})
				b.mcpServers["s2"] = upstream.NewActiveForTesting(m2)
			},
			expected: true,
		},
		{
			name: "all servers healthy",
			setup: func(b *mcpBrokerImpl) {
				mgr := createTestManager(t, "s1", "", nil)
				mgr.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: true})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(mgr)
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(logger).(*mcpBrokerImpl)
			tt.setup(b)
			require.Equal(t, tt.expected, b.IsReady())
		})
	}
}
