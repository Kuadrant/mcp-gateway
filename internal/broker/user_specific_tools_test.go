package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterUserHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    http.Header
		expected map[string]string
	}{
		{
			name:     "empty headers",
			input:    http.Header{},
			expected: map[string]string{},
		},
		{
			name: "strips mcp-session-id",
			input: http.Header{
				"Mcp-Session-Id": []string{"session-123"},
				"Authorization":  []string{"Bearer token"},
			},
			expected: map[string]string{
				"Authorization": "Bearer token",
			},
		},
		{
			name: "strips x-mcp- prefixed and transport headers",
			input: http.Header{
				"X-Mcp-Virtualserver": []string{"ns/vs"},
				"X-Mcp-Authorized":    []string{"jwt-value"},
				"X-Mcp-Custom":        []string{"value"},
				"Accept":              []string{"application/json"},
				"X-Custom":            []string{"keep"},
			},
			expected: map[string]string{
				"X-Custom": "keep",
			},
		},
		{
			name: "strips transport headers, preserves user headers",
			input: http.Header{
				"Authorization": []string{"Bearer xyz"},
				"Content-Type":  []string{"application/json"},
				"X-Custom":      []string{"keep-this"},
			},
			expected: map[string]string{
				"Authorization": "Bearer xyz",
				"X-Custom":      "keep-this",
			},
		},
		{
			name: "strips accept and mcp-protocol-version",
			input: http.Header{
				"Accept":               []string{"text/html"},
				"Mcp-Protocol-Version": []string{"2026-07-28"},
				"Authorization":        []string{"Bearer tok"},
			},
			expected: map[string]string{
				"Authorization": "Bearer tok",
			},
		},
		{
			name: "strips cookie and proxy-authorization, preserves authorization",
			input: http.Header{
				"Cookie":              []string{"session=secret"},
				"Proxy-Authorization": []string{"Basic abc"},
				"Authorization":       []string{"Bearer user-token"},
			},
			expected: map[string]string{
				"Authorization": "Bearer user-token",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := filterUserHeaders(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestFetchUserSpecificTools_NoUserSpecificServers(t *testing.T) {
	cache, _ := session.NewCache()
	broker := &mcpBrokerImpl{
		mcpServers:               map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 5 * time.Second,
	}

	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "existing-tool"}},
	}
	headers := http.Header{"Mcp-Session-Id": []string{"gw-session"}}

	broker.FetchUserSpecificTools(context.Background(), headers, result)

	assert.Len(t, result.Tools, 1)
	assert.Equal(t, "existing-tool", result.Tools[0].Name)
}

func TestFetchUserSpecificTools_NoGatewaySessionID(t *testing.T) {
	// create a mock server entry with UserSpecificList=true so we reach the session check
	mockServer := newMockActiveMCPServer(config.MCPServer{
		Name:             "test",
		URL:              "http://localhost:9999/mcp",
		Prefix:           "test_",
		State:            "Enabled",
		UserSpecificList: true,
	})

	cfg := mockServer.configPtr()
	cache, _ := session.NewCache()
	servers := []userSpecificServer{toUserSpecificServer(*cfg)}
	broker := &mcpBrokerImpl{
		userSpecificServers:      servers,
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 5 * time.Second,
	}
	withStatefulVersions(broker, servers)
	withProtocolHandlers(broker)

	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "existing-tool"}},
	}
	headers := http.Header{} // no session ID

	broker.FetchUserSpecificTools(context.Background(), headers, result)

	// should return early, no tools added
	assert.Len(t, result.Tools, 1)
}

func toUserSpecificServer(cfg config.MCPServer) userSpecificServer {
	return userSpecificServer{id: cfg.ID(), name: cfg.Name, url: cfg.URL, prefix: cfg.Prefix}
}

// withStatefulVersions populates serverVersions for test servers so
// ServerSupportsVersion returns true for 2025-11-25 (stateful).
func withStatefulVersions(b *mcpBrokerImpl, servers []userSpecificServer) {
	for _, srv := range servers {
		b.serverVersions.Store(srv.id, []string{"2025-11-25"})
	}
}

// withProtocolHandlers initializes the protocol handlers on a test broker.
func withProtocolHandlers(b *mcpBrokerImpl) {
	b.handler2025 = NewProtocolHandler2025(b)
	b.handler2026 = NewProtocolHandler2026(b)
}

// newTestMCPServer returns a test HTTP server that handles MCP initialize and
// tools/list, using the provided counter to track initialize calls and the
// given session ID in responses.
func newTestMCPServer(initCount *atomic.Int32, sessionID string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		id := req["id"]

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", sessionID)

		switch method {
		case "initialize":
			initCount.Add(1)
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"serverInfo":      map[string]any{"name": "test-server", "version": "1.0"},
					"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "user_tool",
							"description": "a user tool",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
}

func TestFetchUserSpecificTools_FetchesAndMergesTools(t *testing.T) {
	var initCount atomic.Int32
	ts := newTestMCPServer(&initCount, "upstream-session-1")
	defer ts.Close()

	mockServer := newMockActiveMCPServer(config.MCPServer{
		Name:             "user-server",
		URL:              ts.URL,
		Prefix:           "us_",
		State:            "Enabled",
		UserSpecificList: true,
	})

	cfg := mockServer.configPtr()
	cache, _ := session.NewCache()
	servers := []userSpecificServer{toUserSpecificServer(*cfg)}
	b := &mcpBrokerImpl{
		userSpecificServers:      servers,
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}
	withStatefulVersions(b, servers)
	withProtocolHandlers(b)

	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "cached-tool"}},
	}
	headers := http.Header{
		"Mcp-Session-Id": []string{"gw-session-abc"},
		"Authorization":  []string{"Bearer user-token"},
	}

	b.FetchUserSpecificTools(context.Background(), headers, result)

	require.Len(t, result.Tools, 2)
	assert.Equal(t, "cached-tool", result.Tools[0].Name)
	assert.Equal(t, "us_user_tool", result.Tools[1].Name)
	assert.Equal(t, int32(1), initCount.Load())

	// verify meta has kuadrant/id
	meta := result.Tools[1].Meta
	require.NotNil(t, meta)
}

func TestFetchUserSpecificTools_GracefulDegradation(t *testing.T) {
	// server that always fails
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := config.MCPServer{
		Name:             "broken-server",
		URL:              ts.URL,
		Prefix:           "brk_",
		State:            "Enabled",
		UserSpecificList: true,
	}

	cache, _ := session.NewCache()
	servers := []userSpecificServer{toUserSpecificServer(cfg)}
	b := &mcpBrokerImpl{
		userSpecificServers:      servers,
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 2 * time.Second,
	}
	withStatefulVersions(b, servers)
	withProtocolHandlers(b)

	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "existing"}},
	}
	headers := http.Header{"Mcp-Session-Id": []string{"gw-session-xyz"}}

	b.FetchUserSpecificTools(context.Background(), headers, result)

	// should still have the original tool, error swallowed
	assert.Len(t, result.Tools, 1)
	assert.Equal(t, "existing", result.Tools[0].Name)
}

func TestGatewaySessionTTL(t *testing.T) {
	t.Run("valid JWT returns positive TTL", func(t *testing.T) {
		// build a JWT with exp 1 hour from now (unsigned, just base64 payload)
		exp := time.Now().Add(1 * time.Hour).Unix()
		payload := fmt.Sprintf(`{"exp":%d}`, exp)
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		fakeJWT := "eyJhbGciOiJIUzI1NiJ9." + encoded + ".signature"

		ttl := gatewaySessionTTL(fakeJWT)
		assert.True(t, ttl > 50*time.Minute && ttl <= 1*time.Hour, "expected ~1h TTL, got %v", ttl)
	})

	t.Run("expired JWT returns 0", func(t *testing.T) {
		exp := time.Now().Add(-1 * time.Hour).Unix()
		payload := fmt.Sprintf(`{"exp":%d}`, exp)
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		fakeJWT := "eyJhbGciOiJIUzI1NiJ9." + encoded + ".signature"

		ttl := gatewaySessionTTL(fakeJWT)
		assert.Equal(t, time.Duration(0), ttl)
	})

	t.Run("invalid JWT returns 0", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), gatewaySessionTTL("not-a-jwt"))
	})
}

func TestFetchUserSpecificTools_SessionCaching(t *testing.T) {
	exp := time.Now().Add(1 * time.Hour).Unix()
	payload := fmt.Sprintf(`{"exp":%d,"iss":"mcp-gateway","aud":"session"}`, exp)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	gwSessionID := "eyJhbGciOiJIUzI1NiJ9." + encoded + ".sig"

	var initCount atomic.Int32
	ts := newTestMCPServer(&initCount, "upstream-cached-session")
	defer ts.Close()

	cfg := config.MCPServer{
		Name:             "caching-server",
		URL:              ts.URL,
		Prefix:           "cs_",
		State:            "Enabled",
		UserSpecificList: true,
	}

	cache, _ := session.NewCache()
	servers := []userSpecificServer{toUserSpecificServer(cfg)}
	b := &mcpBrokerImpl{
		userSpecificServers:      servers,
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}
	withStatefulVersions(b, servers)
	withProtocolHandlers(b)

	makeHeaders := func() http.Header {
		return http.Header{
			"Mcp-Session-Id": []string{gwSessionID},
			"Authorization":  []string{"Bearer user-token"},
		}
	}

	// first call: should initialize
	result1 := &mcp.ListToolsResult{}
	b.FetchUserSpecificTools(context.Background(), makeHeaders(), result1)
	require.Len(t, result1.Tools, 1)
	assert.Equal(t, int32(1), initCount.Load())

	// verify session was cached
	sessions, err := cache.GetSession(context.Background(), gwSessionID)
	require.NoError(t, err)
	assert.Equal(t, "upstream-cached-session", sessions["caching-server"])

	// second call: should skip initialize, reuse cached session
	result2 := &mcp.ListToolsResult{}
	b.FetchUserSpecificTools(context.Background(), makeHeaders(), result2)
	require.Len(t, result2.Tools, 1)
	assert.Equal(t, int32(1), initCount.Load(), "expected no second initialize call")
}

// mockActiveMCPServer is a minimal ActiveMCPServer for testing
type mockActiveMCPServer struct {
	cfg config.MCPServer
}

func newMockActiveMCPServer(cfg config.MCPServer) *mockActiveMCPServer {
	return &mockActiveMCPServer{cfg: cfg}
}

func (m *mockActiveMCPServer) Stop()           {}
func (m *mockActiveMCPServer) MCPName() string { return m.cfg.Name }
func (m *mockActiveMCPServer) GetStatus() upstream.ServerValidationStatus {
	return upstream.ServerValidationStatus{Ready: true}
}
func (m *mockActiveMCPServer) GetManagedTools() []mcp.Tool                 { return nil }
func (m *mockActiveMCPServer) GetServedManagedTool(_ string) *mcp.Tool     { return nil }
func (m *mockActiveMCPServer) GetManagedPrompts() []mcp.Prompt             { return nil }
func (m *mockActiveMCPServer) GetServedManagedPrompt(_ string) *mcp.Prompt { return nil }
func (m *mockActiveMCPServer) Config() config.MCPServer                    { return m.cfg }
func (m *mockActiveMCPServer) configPtr() *config.MCPServer                { return &m.cfg }
func (m *mockActiveMCPServer) GetToolHints(_ string) (upstream.ToolHints, bool) {
	return upstream.ToolHints{}, false
}
func (m *mockActiveMCPServer) SupportedVersions() []string   { return nil }
func (m *mockActiveMCPServer) SupportsVersion(_ string) bool { return false }
func (m *mockActiveMCPServer) ToolsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *mockActiveMCPServer) PromptsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{}
}
func (m *mockActiveMCPServer) SupportsResources() bool { return false }
func (m *mockActiveMCPServer) ListResources(context.Context) (*mcp.ListResourcesResult, error) {
	return &mcp.ListResourcesResult{}, nil
}

// seedUserSession dials the fake upstream and stores a pooled session for
// the given gateway session ID.
func seedUserSession(t *testing.T, b *mcpBrokerImpl, srv userSpecificServer, gwSessionID string) *mcp.ClientSession {
	t.Helper()
	sess, err := b.getOrCreateUserSession(context.Background(), srv, map[string]string{"Authorization": "Bearer u"}, gwSessionID)
	require.NoError(t, err)
	return sess
}

func poolSize(b *mcpBrokerImpl) int {
	n := 0
	b.userSessionPool.Range(func(_, _ any) bool { n++; return true })
	return n
}

// regression for the SDK migration: pooled upstream sessions (carrying the
// user's Authorization header and a live SSE connection) were only evicted
// on ListTools error, never when the gateway session ended.
func TestUserSessionPool_EvictedOnGatewaySessionEnd(t *testing.T) {
	var initCount atomic.Int32
	ts := newTestMCPServer(&initCount, "upstream-sess")
	defer ts.Close()

	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: ts.URL, prefix: "us_"}
	b := &mcpBrokerImpl{
		logger:                   slog.Default(),
		userSpecificFetchTimeout: 10 * time.Second,
	}

	seedUserSession(t, b, srv, "gw-a")
	seedUserSession(t, b, srv, "gw-b")
	require.Equal(t, 2, poolSize(b))

	b.onGatewaySessionEnd("gw-a")

	require.Equal(t, 1, poolSize(b))
	_, stillA := b.userSessionPool.Load(userSessionKey("gw-a", srv.name))
	require.False(t, stillA, "gw-a pool entry must be evicted on session end")
	_, stillB := b.userSessionPool.Load(userSessionKey("gw-b", srv.name))
	require.True(t, stillB, "gw-b pool entry must survive gw-a session end")
}

// gateway session end must evict via the real SDK session lifecycle: when
// the client disconnects, the broker's Wait goroutine runs the cleanup.
func TestUserSessionPool_EvictedWhenClientDisconnects(t *testing.T) {
	var counter atomic.Int64
	b := NewBroker(slog.Default(),
		WithDiscoveryToolsEnabled(false),
		WithSessionIDGenerator(func() string { return fmt.Sprintf("gw-sess-%d", counter.Add(1)) }),
	).(*mcpBrokerImpl)

	gwHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return b.MCPServer() }, nil))
	defer gwHTTP.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gwHTTP.URL}, nil)
	require.NoError(t, err)
	gwSessionID := cs.ID()
	require.NotEmpty(t, gwSessionID)

	var initCount atomic.Int32
	upstreamTS := newTestMCPServer(&initCount, "upstream-sess")
	defer upstreamTS.Close()
	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: upstreamTS.URL, prefix: "us_"}
	seedUserSession(t, b, srv, gwSessionID)
	require.Equal(t, 1, poolSize(b))

	require.NoError(t, cs.Close())

	require.Eventually(t, func() bool { return poolSize(b) == 0 }, 5*time.Second, 20*time.Millisecond,
		"pool entry must be evicted when the gateway session ends")
}

func TestUserSessionPool_DrainedOnShutdown(t *testing.T) {
	var initCount atomic.Int32
	ts := newTestMCPServer(&initCount, "upstream-sess")
	defer ts.Close()

	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: ts.URL, prefix: "us_"}
	b := &mcpBrokerImpl{
		mcpServers:               map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
		logger:                   slog.Default(),
		userSpecificFetchTimeout: 10 * time.Second,
	}

	sess := seedUserSession(t, b, srv, "gw-a")
	require.Equal(t, 1, poolSize(b))

	require.NoError(t, b.Shutdown(context.Background()))

	require.Zero(t, poolSize(b), "pool must be drained on shutdown")
	_, err := sess.ListTools(context.Background(), nil)
	require.Error(t, err, "drained session must be closed")
}

// regression: the pool used to pin the first-seen Authorization into the
// cached transport. mark3labs connected fresh per fetch, so the upstream
// always saw the caller's current token; a refreshed token must reach the
// upstream through a pooled session too.
func TestUserSessionPool_AuthHeaderStaysFresh(t *testing.T) {
	var lastAuth atomic.Value
	lastAuth.Store("")
	var initCount atomic.Int32
	inner := newTestMCPServer(&initCount, "upstream-sess")
	defer inner.Close()
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			lastAuth.Store(r.Header.Get("Authorization"))
		}
		// fixed local target and constant methods keep the proxy taint-free
		method := http.MethodPost
		switch r.Method {
		case http.MethodGet:
			method = http.MethodGet
		case http.MethodDelete:
			method = http.MethodDelete
		}
		req, err := http.NewRequestWithContext(r.Context(), method, inner.URL, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer capture.Close()

	srv := userSpecificServer{id: "ns/user-server", name: "user-server", url: capture.URL, prefix: "us_"}
	b := &mcpBrokerImpl{
		logger:                   slog.Default(),
		userSpecificFetchTimeout: 10 * time.Second,
	}
	defer b.drainUserSessionPool()

	ctx := context.Background()

	// first fetch with token A
	sessA, err := b.getOrCreateUserSession(ctx, srv, map[string]string{"Authorization": "Bearer token-a"}, "gw-1")
	require.NoError(t, err)
	_, err = sessA.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer token-a", lastAuth.Load())

	// second fetch on the same gateway session with a refreshed token: the
	// pooled session must be reused but carry the new token upstream
	sessB, err := b.getOrCreateUserSession(ctx, srv, map[string]string{"Authorization": "Bearer token-b"}, "gw-1")
	require.NoError(t, err)
	require.Same(t, sessA, sessB, "session must be reused from the pool")
	require.Equal(t, 1, int(initCount.Load()), "no reconnect on token refresh")

	_, err = sessB.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer token-b", lastAuth.Load(), "refreshed token must reach the upstream")
}

// newStatelessTestMCPServer creates an httptest server running a 2026-07-28
// stateless MCP server with a single tool.
func newStatelessTestMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "stateless-test", Version: "1.0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "tool",
		Description: "stateless tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
	return httptest.NewServer(handler)
}

func TestFetchUserSpecificTools_StatelessFetch(t *testing.T) {
	ts := newStatelessTestMCPServer(t)
	defer ts.Close()

	cache, _ := session.NewCache()
	srv := userSpecificServer{
		id: "ns/stateless-server", name: "stateless-server",
		url: ts.URL, prefix: "sl_",
	}
	b := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{srv},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}
	b.serverVersions.Store(srv.id, []string{"2026-07-28"})
	withProtocolHandlers(b)

	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "cached-tool"}},
	}
	headers := http.Header{
		"Mcp-Session-Id":       []string{"gw-session-1"},
		"Mcp-Protocol-Version": []string{"2026-07-28"},
		"Authorization":        []string{"Bearer user-token"},
	}

	b.FetchUserSpecificTools(context.Background(), headers, result)

	require.Len(t, result.Tools, 2, "should merge stateless tool with existing")
	assert.Equal(t, "cached-tool", result.Tools[0].Name)
	assert.Equal(t, "sl_tool", result.Tools[1].Name, "tool should be prefixed with sl_")

	// no session should be cached (stateless path)
	assert.Equal(t, 0, poolSize(b), "stateless fetch should not cache sessions")
}

// registerActiveServer wires an upstream into b.mcpServers with the given
// prefix, URL and tools cache metadata (so the exclusion predicate can read
// them) and returns the userSpecificServer used to seed the fresh-fetch set.
func registerActiveServer(t *testing.T, b *mcpBrokerImpl, id config.UpstreamMCPID, prefix, url string, meta upstream.CacheMetadata) userSpecificServer {
	t.Helper()
	b.serverVersions.Store(id, []string{"2026-07-28"})
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:             string(id),
		Prefix:           prefix,
		URL:              url,
		UserSpecificList: meta.UserSpecificList,
	}, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	manager.SetCacheMetadataForTesting(meta, upstream.CacheMetadata{})
	b.mcpServers[id] = upstream.NewActiveForTesting(manager)
	return userSpecificServer{id: id, name: string(id), url: url, prefix: prefix}
}

// backstops rebuildProtocolCaches: a server whose cacheScope metadata was not
// yet populated at the last rebuild can still be scheduled for per-request
// fetch. The per-request path must drop private-scope, no-prefix servers whose
// tools would be unroutable, without leaking them onto the merged list.
func TestFetchUserSpecificTools_ExcludesPrivateScopeWithoutPrefix(t *testing.T) {
	ts := newStatelessTestMCPServer(t) // serves a tool named "tool"
	defer ts.Close()

	tests := []struct {
		name     string
		prefix   string
		meta     upstream.CacheMetadata
		wantTool string // merged tool name; "" means the server must be excluded
	}{
		{
			name:   "private scope, no prefix -> excluded",
			prefix: "",
			meta:   upstream.CacheMetadata{CacheScope: upstream.CacheScopePrivate},
		},
		{
			name:     "private scope, with prefix -> included",
			prefix:   "p_",
			meta:     upstream.CacheMetadata{CacheScope: upstream.CacheScopePrivate},
			wantTool: "p_tool",
		},
		{
			name:     "public scope, no prefix (ttlMs:0) -> included",
			prefix:   "",
			meta:     upstream.CacheMetadata{TTLMs: 0, CacheScope: upstream.CacheScopePublic},
			wantTool: "tool",
		},
		{
			name:   "userSpecificList, no prefix -> excluded",
			prefix: "",
			meta:   upstream.CacheMetadata{CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
			id := config.UpstreamMCPID("srv1")
			srv := registerActiveServer(t, b, id, tt.prefix, ts.URL, tt.meta)

			// simulate a prior rebuild having scheduled this server for per-request fetch
			b.statelessTools.Store(&protocolCacheEntry[*mcp.Tool]{
				freshFetchServers: []userSpecificServer{srv},
			})

			result := &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "cached-tool"}}}
			headers := http.Header{
				"Mcp-Session-Id":       []string{"gw-session-1"},
				"Mcp-Protocol-Version": []string{"2026-07-28"},
				"Authorization":        []string{"Bearer user-token"},
			}

			b.FetchUserSpecificTools(context.Background(), headers, result)

			names := make([]string, 0, len(result.Tools))
			for _, tool := range result.Tools {
				names = append(names, tool.Name)
			}
			assert.Contains(t, names, "cached-tool")
			if tt.wantTool == "" {
				assert.Len(t, result.Tools, 1, "excluded server must contribute no tools")
			} else {
				assert.Contains(t, names, tt.wantTool, "included server's tool must be merged")
			}
		})
	}
}

// the per-request path must skip excluded servers silently: the warning is
// emitted once at rebuild, never per request (hot-path rule).
func TestFetchUserSpecificTools_NoWarnOnExclusion(t *testing.T) {
	const msg = "private-scope server has no prefix configured, tools excluded from listing"
	count := 0
	logger := slog.New(warnCounter{Handler: slog.DiscardHandler, msg: msg, count: &count})

	ts := newStatelessTestMCPServer(t)
	defer ts.Close()

	b := NewBroker(logger, WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	id := config.UpstreamMCPID("srv1")
	srv := registerActiveServer(t, b, id, "", ts.URL, upstream.CacheMetadata{CacheScope: upstream.CacheScopePrivate})
	b.statelessTools.Store(&protocolCacheEntry[*mcp.Tool]{
		freshFetchServers: []userSpecificServer{srv},
	})

	result := &mcp.ListToolsResult{}
	headers := http.Header{
		"Mcp-Session-Id":       []string{"gw-session-1"},
		"Mcp-Protocol-Version": []string{"2026-07-28"},
		"Authorization":        []string{"Bearer user-token"},
	}

	b.FetchUserSpecificTools(context.Background(), headers, result)

	assert.Empty(t, result.Tools, "excluded server must contribute no tools")
	assert.Equal(t, 0, count, "no per-request warning on the exclusion path")
}

func TestFetchUserSpecificTools_ProtocolFiltering(t *testing.T) {
	// stateful (2025) test server
	var initCount2025 atomic.Int32
	ts2025 := newTestMCPServer(&initCount2025, "upstream-2025")
	defer ts2025.Close()

	// stateless (2026) test server
	ts2026 := newStatelessTestMCPServer(t)
	defer ts2026.Close()

	srv2025 := userSpecificServer{
		id: "ns/server-2025", name: "server-2025",
		url: ts2025.URL, prefix: "s25_",
	}
	srv2026 := userSpecificServer{
		id: "ns/server-2026", name: "server-2026",
		url: ts2026.URL, prefix: "s26_",
	}

	cache, _ := session.NewCache()
	b := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{srv2025, srv2026},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}
	b.serverVersions.Store(srv2025.id, []string{"2025-11-25"})
	b.serverVersions.Store(srv2026.id, []string{"2026-07-28"})
	withProtocolHandlers(b)

	t.Run("2025 client sees only 2025 tools", func(t *testing.T) {
		result := &mcp.ListToolsResult{}
		headers := http.Header{
			"Mcp-Session-Id": []string{"gw-session-2025"},
			"Authorization":  []string{"Bearer token"},
		}
		b.FetchUserSpecificTools(context.Background(), headers, result)

		var names []string
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
		}
		assert.Contains(t, names, "s25_user_tool", "2025 client should see 2025 server tools")
		assert.NotContains(t, names, "s26_tool", "2025 client should NOT see 2026 server tools")
	})

	t.Run("2026 client sees only 2026 tools", func(t *testing.T) {
		result := &mcp.ListToolsResult{}
		headers := http.Header{
			"Mcp-Session-Id":       []string{"gw-session-2026"},
			"Mcp-Protocol-Version": []string{"2026-07-28"},
			"Authorization":        []string{"Bearer token"},
		}
		b.FetchUserSpecificTools(context.Background(), headers, result)

		var names []string
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
		}
		assert.Contains(t, names, "s26_tool", "2026 client should see 2026 server tools")
		assert.NotContains(t, names, "s25_user_tool", "2026 client should NOT see 2025 server tools")
	})
}
