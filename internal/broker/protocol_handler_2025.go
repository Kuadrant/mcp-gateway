package broker

import (
	"context"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var _ ProtocolHandler = (*ProtocolHandler2025)(nil)

// ProtocolHandler2025 implements ProtocolHandler for MCP 2025-11-25.
// It wraps existing stateful fetch behavior: session pool reuse,
// gateway session ID requirement, and no cache aggregation.
type ProtocolHandler2025 struct {
	broker *mcpBrokerImpl
}

// NewProtocolHandler2025 creates a 2025 protocol handler backed by the broker.
func NewProtocolHandler2025(broker *mcpBrokerImpl) *ProtocolHandler2025 {
	return &ProtocolHandler2025{broker: broker}
}

// FetchUserSpecificTools performs stateful fetch via the session pool.
func (h *ProtocolHandler2025) FetchUserSpecificTools(ctx context.Context, servers []userSpecificServer, headers http.Header, result *mcp.ListToolsResult) {
	if len(servers) == 0 {
		return
	}
	h.broker.fetchStatefulUserTools(ctx, servers, headers, result)
}

// ShouldFetchFresh for 2025 returns true only when the CRD declares
// userSpecificList — no runtime cache metadata signals.
func (h *ProtocolHandler2025) ShouldFetchFresh(srv userSpecificServer, _ *upstream.CacheMetadata) bool {
	return h.broker.isUserSpecificByCRD(srv)
}

// AggregateCache returns zero values for 2025 — the compat handler
// strips ttlMs and cacheScope from responses to 2025 clients.
func (h *ProtocolHandler2025) AggregateCache(_ []upstream.CacheMetadata) (int, string) {
	return 0, ""
}

