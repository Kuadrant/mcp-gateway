package broker

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// negotiatedMockServer adds the optional negotiatedVersioner and
// statefulSchemaSource capabilities.
type negotiatedMockServer struct {
	mockActiveServer
	negotiated    string
	legacySchemas map[string]any // nil: no 2025 listing available
}

func (m *negotiatedMockServer) NegotiatedProtocolVersion() string { return m.negotiated }

func (m *negotiatedMockServer) StatefulOutputSchema(served string) (any, bool) {
	if m.legacySchemas == nil {
		return nil, false
	}
	s, ok := m.legacySchemas[served]
	return s, ok
}

// a public, cacheable catalog keeps the tools in the shared stateless cache
// (a zero-TTL or private catalog is fetched per request instead).
func (m *negotiatedMockServer) ToolsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePublic}
}

// cacheableMockServer lacks both optional capabilities.
type cacheableMockServer struct{ mockActiveServer }

func (m *cacheableMockServer) ToolsCacheMetadata() upstream.CacheMetadata {
	return upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePublic}
}

func TestRebuildProtocolCaches_StatefulOutputSchemaFrom2025Listing(t *testing.T) {
	out2026 := map[string]any{"type": "object", "properties": map[string]any{"login": map[string]any{"type": "string"}}}
	out2025 := map[string]any{"type": "object", "properties": map[string]any{"v2025": map[string]any{"type": "string"}}}
	dual := []string{"2025-11-25", "2026-07-28"}
	tests := []struct {
		name         string
		server       upstream.ActiveMCPServer
		versions     []string
		wantStateful any // expected outputSchema on the stateful copy
		inStateless  bool
	}{
		{name: "listed over 2026, 2025 listing advertises a schema: use it",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: dual}, "2026-07-28", map[string]any{"get_me": out2025}},
			versions: dual, wantStateful: out2025, inStateless: true},
		{name: "listed over 2026, 2025 listing has no schema (github-mcp v2): dropped",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: dual}, "2026-07-28", map[string]any{"get_me": nil}},
			versions: dual, wantStateful: nil, inStateless: true},
		{name: "listed over 2026, no 2025 listing available: dropped (conservative)",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: dual}, "2026-07-28", nil},
			versions: dual, wantStateful: nil, inStateless: true},
		{name: "listed over 2026, tool absent from 2025 listing: dropped",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: dual}, "2026-07-28", map[string]any{"other": out2025}},
			versions: dual, wantStateful: nil, inStateless: true},
		{name: "listed over 2025: kept unchanged",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: dual}, "2025-11-25", nil},
			versions: dual, wantStateful: out2026, inStateless: true},
		{name: "2025-only upstream: kept unchanged",
			server:   &negotiatedMockServer{mockActiveServer{supportedVersions: []string{"2025-11-25"}}, "2025-11-25", nil},
			versions: []string{"2025-11-25"}, wantStateful: out2026},
		{name: "server without the optional capabilities: kept (unchanged behaviour)",
			server:   &cacheableMockServer{mockActiveServer{supportedVersions: dual}},
			versions: dual, wantStateful: out2026, inStateless: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
			id := config.UpstreamMCPID("server1")
			b.serverVersions.Store(id, tt.versions)
			b.mcpServers[id] = tt.server
			b.gatewayServer.AddTools(upstream.GatewayTool{
				Tool:    mcp.Tool{Name: "get_me", InputSchema: objectSchema, OutputSchema: out2026, Meta: map[string]any{"kuadrant/id": "server1"}},
				Handler: upstream.NoopToolHandler,
			})
			b.rebuildProtocolCaches()

			stateful := b.statefulTools.Load()
			require.NotNil(t, stateful)
			require.Len(t, stateful.items, 1)
			if tt.wantStateful == nil {
				assert.Nil(t, stateful.items[0].OutputSchema)
			} else {
				assert.Equal(t, tt.wantStateful, stateful.items[0].OutputSchema)
			}
			assert.Equal(t, "server1", stateful.items[0].Meta["kuadrant/id"], "meta preserved for FilterTools")

			stateless := b.statelessTools.Load()
			if !tt.inStateless {
				if stateless != nil {
					assert.Empty(t, stateless.items)
				}
				return
			}
			require.NotNil(t, stateless)
			require.Len(t, stateless.items, 1)
			assert.Equal(t, out2026, stateless.items[0].OutputSchema, "stateless (2026) cache keeps the 2026 schema")
		})
	}
}
