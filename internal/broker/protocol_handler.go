package broker

import (
	"context"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProtocolHandler encapsulates version-specific broker behavior for MCP
// protocol versions. The broker holds one handler per supported version
// and delegates to the appropriate one based on the upstream's or
// client's negotiated version.
type ProtocolHandler interface {
	// FetchUserSpecificTools fetches tools from per-request servers using the
	// caller's headers and merges them into result.
	FetchUserSpecificTools(ctx context.Context, servers []userSpecificServer, headers http.Header, result *mcp.ListToolsResult)

	// ShouldFetchFresh returns true if the given server's tools must be
	// fetched per request rather than served from the broker's cache.
	ShouldFetchFresh(srv userSpecificServer, meta *upstream.CacheMetadata) bool

	// AggregateCache computes the aggregate ttlMs and cacheScope across
	// contributing upstream servers' cache metadata.
	AggregateCache(contributing []upstream.CacheMetadata) (ttlMs int, cacheScope string)
}
