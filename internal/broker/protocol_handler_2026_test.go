package broker

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtocolHandler2026_ShouldFetchFresh_PrivateScope(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	assert.True(t, h.ShouldFetchFresh(userSpecificServer{}, &upstream.CacheMetadata{CacheScope: upstream.CacheScopePrivate, TTLMs: 5000}))
}

func TestProtocolHandler2026_ShouldFetchFresh_ZeroTTL(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	assert.True(t, h.ShouldFetchFresh(userSpecificServer{}, &upstream.CacheMetadata{CacheScope: upstream.CacheScopePublic, TTLMs: 0}))
}

func TestProtocolHandler2026_ShouldFetchFresh_PublicNonZero(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	assert.False(t, h.ShouldFetchFresh(userSpecificServer{}, &upstream.CacheMetadata{CacheScope: upstream.CacheScopePublic, TTLMs: 5000}))
}

func TestProtocolHandler2026_ShouldFetchFresh_NilMeta(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	assert.False(t, h.ShouldFetchFresh(userSpecificServer{}, nil))
}

func TestProtocolHandler2026_AggregateCache_AllPublic(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 60000, CacheScope: upstream.CacheScopePublic},
		{TTLMs: 30000, CacheScope: upstream.CacheScopePublic},
	})
	assert.Equal(t, 30000, ttl)
	assert.Equal(t, upstream.CacheScopePublic, scope)
}

func TestProtocolHandler2026_AggregateCache_Mixed(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 60000, CacheScope: upstream.CacheScopePublic},
		{TTLMs: 30000, CacheScope: upstream.CacheScopePrivate},
	})
	assert.Equal(t, 30000, ttl)
	assert.Equal(t, upstream.CacheScopePrivate, scope)
}

func TestProtocolHandler2026_AggregateCache_AllPrivate(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 10000, CacheScope: upstream.CacheScopePrivate},
		{TTLMs: 20000, CacheScope: upstream.CacheScopePrivate},
	})
	assert.Equal(t, 10000, ttl)
	assert.Equal(t, upstream.CacheScopePrivate, scope)
}

func TestProtocolHandler2026_AggregateCache_AllZeroTTL(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 0, CacheScope: upstream.CacheScopePublic},
		{TTLMs: 0, CacheScope: upstream.CacheScopePublic},
	})
	assert.Equal(t, 0, ttl)
	assert.Equal(t, upstream.CacheScopePrivate, scope)
}

func TestProtocolHandler2026_AggregateCache_ZeroTTLForcesUncacheable(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 60000, CacheScope: upstream.CacheScopePublic},
		{TTLMs: 0, CacheScope: upstream.CacheScopePublic},
	})
	assert.Equal(t, 0, ttl)
	assert.Equal(t, upstream.CacheScopePrivate, scope)
}

func TestProtocolHandler2026_AggregateCache_UserSpecificListForcesPrivate(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 60000, CacheScope: upstream.CacheScopePublic},
		{TTLMs: 30000, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
	})
	assert.Equal(t, 30000, ttl)
	assert.Equal(t, upstream.CacheScopePrivate, scope)
}

func TestProtocolHandler2026_AggregateCache_SingleServer(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache([]upstream.CacheMetadata{
		{TTLMs: 45000, CacheScope: upstream.CacheScopePublic},
	})
	assert.Equal(t, 45000, ttl)
	assert.Equal(t, upstream.CacheScopePublic, scope)
}

func TestProtocolHandler2026_AggregateCache_Empty(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	ttl, scope := h.AggregateCache(nil)
	assert.Equal(t, 0, ttl)
	assert.Equal(t, upstream.CacheScopePublic, scope)
}

func TestProtocolHandler2026_FetchUserSpecificTools_EmptyServers(t *testing.T) {
	h := NewProtocolHandler2026(nil)
	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{{Name: "existing"}},
	}
	h.FetchUserSpecificTools(context.Background(), nil, http.Header{}, result)
	assert.Len(t, result.Tools, 1)
}

func TestProtocolHandler2026_FetchUserSpecificTools_Stateless(t *testing.T) {
	ts := newStatelessTestMCPServer(t)
	defer ts.Close()

	cache, _ := session.NewCache()
	srv := userSpecificServer{
		id: "ns/stateless-server", name: "stateless-server",
		url: ts.URL, prefix: "sl_",
	}
	b := &mcpBrokerImpl{
		logger:                   slog.Default(),
		sessionCache:             cache,
		userSpecificFetchTimeout: 10 * time.Second,
	}
	h := NewProtocolHandler2026(b)

	result := &mcp.ListToolsResult{}
	headers := http.Header{
		"Authorization": []string{"Bearer user-token"},
	}

	h.FetchUserSpecificTools(context.Background(), []userSpecificServer{srv}, headers, result)

	require.Len(t, result.Tools, 1)
	assert.Equal(t, "sl_tool", result.Tools[0].Name)
}
