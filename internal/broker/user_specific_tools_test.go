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
			name: "strips x-mcp- prefixed headers",
			input: http.Header{
				"X-Mcp-Virtualserver": []string{"ns/vs"},
				"X-Mcp-Authorized":    []string{"jwt-value"},
				"X-Mcp-Custom":        []string{"value"},
				"Accept":              []string{"application/json"},
			},
			expected: map[string]string{
				"Accept": "application/json",
			},
		},
		{
			name: "preserves non-internal headers",
			input: http.Header{
				"Authorization": []string{"Bearer xyz"},
				"Content-Type":  []string{"application/json"},
				"X-Custom":      []string{"keep-this"},
			},
			expected: map[string]string{
				"Authorization": "Bearer xyz",
				"Content-Type":  "application/json",
				"X-Custom":      "keep-this",
			},
		},
		{
			name: "uses first value for multi-value headers",
			input: http.Header{
				"Accept": []string{"text/html", "application/json"},
			},
			expected: map[string]string{
				"Accept": "text/html",
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
	broker := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{toUserSpecificServer(*cfg)},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 5 * time.Second,
	}

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
	b := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{toUserSpecificServer(*cfg)},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}

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
	b := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{toUserSpecificServer(cfg)},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 2 * time.Second,
	}

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
	b := &mcpBrokerImpl{
		userSpecificServers:      []userSpecificServer{toUserSpecificServer(cfg)},
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}

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
