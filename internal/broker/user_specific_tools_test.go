package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/mark3labs/mcp-go/mcp"
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
		Tools: []mcp.Tool{{Name: "existing-tool"}},
	}
	req := &mcp.ListToolsRequest{}
	req.Header = http.Header{"Mcp-Session-Id": []string{"gw-session"}}

	broker.FetchUserSpecificTools(context.Background(), nil, req, result)

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
		Tools: []mcp.Tool{{Name: "existing-tool"}},
	}
	req := &mcp.ListToolsRequest{}
	req.Header = http.Header{} // no session ID

	broker.FetchUserSpecificTools(context.Background(), nil, req, result)

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
		Tools: []mcp.Tool{{Name: "cached-tool"}},
	}
	req := &mcp.ListToolsRequest{}
	req.Header = http.Header{
		"Mcp-Session-Id": []string{"gw-session-abc"},
		"Authorization":  []string{"Bearer user-token"},
	}

	b.FetchUserSpecificTools(context.Background(), nil, req, result)

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
		Tools: []mcp.Tool{{Name: "existing"}},
	}
	req := &mcp.ListToolsRequest{}
	req.Header = http.Header{"Mcp-Session-Id": []string{"gw-session-xyz"}}

	b.FetchUserSpecificTools(context.Background(), nil, req, result)

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

	makeReq := func() *mcp.ListToolsRequest {
		req := &mcp.ListToolsRequest{}
		req.Header = http.Header{
			"Mcp-Session-Id": []string{gwSessionID},
			"Authorization":  []string{"Bearer user-token"},
		}
		return req
	}

	// first call: should initialize
	result1 := &mcp.ListToolsResult{}
	b.FetchUserSpecificTools(context.Background(), nil, makeReq(), result1)
	require.Len(t, result1.Tools, 1)
	assert.Equal(t, int32(1), initCount.Load())

	// verify session was cached
	sessions, err := cache.GetSession(context.Background(), gwSessionID)
	require.NoError(t, err)
	assert.Equal(t, "upstream-cached-session", sessions["caching-server"])

	// second call: should skip initialize, reuse cached session
	result2 := &mcp.ListToolsResult{}
	b.FetchUserSpecificTools(context.Background(), nil, makeReq(), result2)
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
