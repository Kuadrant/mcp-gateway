package broker

import (
	"context"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var _ ProtocolHandler = (*ProtocolHandler2026)(nil)

// ProtocolHandler2026 implements ProtocolHandler for MCP 2026-07-28.
// Stateless connect-list-close fetching, cache-metadata-driven fresh-fetch
// detection, and aggregated ttlMs/cacheScope on list responses.
type ProtocolHandler2026 struct {
	broker *mcpBrokerImpl
}

// NewProtocolHandler2026 creates a 2026 protocol handler backed by the broker.
func NewProtocolHandler2026(broker *mcpBrokerImpl) *ProtocolHandler2026 {
	return &ProtocolHandler2026{broker: broker}
}

// FetchUserSpecificTools performs stateless connect-list-close for each server.
func (h *ProtocolHandler2026) FetchUserSpecificTools(ctx context.Context, servers []userSpecificServer, headers http.Header, result *mcp.ListToolsResult) {
	if len(servers) == 0 {
		return
	}
	h.broker.fetchStatelessUserTools(ctx, servers, headers, result)
}

// ShouldFetchFresh returns true when the upstream's cache metadata indicates
// per-request fetching: private scope or zero TTL.
func (h *ProtocolHandler2026) ShouldFetchFresh(_ userSpecificServer, meta *upstream.CacheMetadata) bool {
	if meta == nil {
		return false
	}
	return meta.CacheScope == upstream.CacheScopePrivate || meta.TTLMs == 0
}

// AggregateCache computes the aggregate ttlMs and cacheScope across
// contributing upstreams. ttlMs is min of non-zero values; any upstream
// with TTLMs==0 forces the aggregate to 0 (uncacheable). cacheScope is
// "private" if any upstream is private, user-specific, or zero-TTL.
func (h *ProtocolHandler2026) AggregateCache(contributing []upstream.CacheMetadata) (int, string) {
	if len(contributing) == 0 {
		return 0, upstream.CacheScopePublic
	}

	minTTL := 0
	hasZero := false
	scope := upstream.CacheScopePublic

	for i := range contributing {
		c := &contributing[i]
		if c.TTLMs == 0 {
			hasZero = true
		} else if minTTL == 0 || c.TTLMs < minTTL {
			minTTL = c.TTLMs
		}
		if c.CacheScope == upstream.CacheScopePrivate || c.UserSpecificList {
			scope = upstream.CacheScopePrivate
		}
	}

	if hasZero {
		return 0, upstream.CacheScopePrivate
	}
	return minTTL, scope
}

// StartNotificationWatcher is a placeholder — wired in Task 6 with
// subscriptions/listen.
func (h *ProtocolHandler2026) StartNotificationWatcher(_ context.Context, _ *upstream.MCPServer) {
}
