package broker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerServer wires an upstream into the broker with the given prefix and
// tools cache metadata, and registers one tool for it in the gateway server.
func registerServer(t *testing.T, b *mcpBrokerImpl, id config.UpstreamMCPID, prefix string, meta upstream.CacheMetadata) {
	t.Helper()
	b.serverVersions.Store(id, []string{"2026-07-28"})
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:             string(id),
		Prefix:           prefix,
		URL:              "http://test.local/mcp",
		UserSpecificList: meta.UserSpecificList,
	}, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetToolsForTesting([]mcp.Tool{{Name: "tool1", InputSchema: objectSchema}})
	manager.SetCacheMetadataForTesting(meta, upstream.CacheMetadata{})
	b.mcpServers[id] = upstream.NewActiveForTesting(manager)
	b.gatewayServer.AddTools(upstream.GatewayTool{
		Tool:    mcp.Tool{Name: prefix + "tool1", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": string(id)}},
		Handler: upstream.NoopToolHandler,
	})
}

func TestRebuildProtocolCaches_ExcludesNonCacheableTools(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		meta       upstream.CacheMetadata
		wantCached bool
		wantFresh  bool
	}{
		{
			name:       "private scope, no prefix -> excluded as unroutable",
			prefix:     "",
			meta:       upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePrivate},
			wantCached: false,
			wantFresh:  false,
		},
		{
			name:       "private scope, with prefix -> fresh only",
			prefix:     "s1_",
			meta:       upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePrivate},
			wantCached: false,
			wantFresh:  true,
		},
		{
			name:       "zero TTL, with prefix -> fresh only",
			prefix:     "s1_",
			meta:       upstream.CacheMetadata{TTLMs: 0, CacheScope: upstream.CacheScopePublic},
			wantCached: false,
			wantFresh:  true,
		},
		{
			name:       "public scope, positive TTL -> cached",
			prefix:     "",
			meta:       upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePublic},
			wantCached: true,
			wantFresh:  false,
		},
		{
			name:       "userSpecificList, no prefix -> excluded as unroutable",
			prefix:     "",
			meta:       upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePublic, UserSpecificList: true},
			wantCached: false,
			wantFresh:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
			id := config.UpstreamMCPID("srv1")
			registerServer(t, b, id, tt.prefix, tt.meta)

			b.rebuildProtocolCaches()

			cached := b.statelessTools.Load()
			if cached == nil {
				t.Fatal("statelessTools is nil")
			}
			if got := len(cached.items); got != btoi(tt.wantCached) {
				t.Errorf("cached tool count: got %d, want %d", got, btoi(tt.wantCached))
			}
			if got := len(cached.freshFetchServers); got != btoi(tt.wantFresh) {
				t.Errorf("fresh-fetch server count: got %d, want %d", got, btoi(tt.wantFresh))
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

// warnCounter counts warn records matching a specific message.
type warnCounter struct {
	slog.Handler
	msg   string
	count *int
}

// Enabled must return true: the embedded DiscardHandler disables all levels,
// which would stop slog from ever calling Handle.
func (h warnCounter) Enabled(context.Context, slog.Level) bool { return true }

func (h warnCounter) Handle(ctx context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn && r.Message == h.msg {
		*h.count++
	}
	return h.Handler.Handle(ctx, r)
}

func TestRebuildProtocolCaches_WarnsOncePerExcludedServer(t *testing.T) {
	const msg = "private-scope server has no prefix configured, tools excluded from listing"
	count := 0
	logger := slog.New(warnCounter{Handler: slog.DiscardHandler, msg: msg, count: &count})

	b := NewBroker(logger, WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	id := config.UpstreamMCPID("srv1")
	registerServer(t, b, id, "", upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePrivate})
	// second tool for the same excluded server: the warning must not fire per tool
	b.gatewayServer.AddTools(upstream.GatewayTool{
		Tool:    mcp.Tool{Name: "tool2", InputSchema: objectSchema, Meta: mcp.Meta{"kuadrant/id": string(id)}},
		Handler: upstream.NoopToolHandler,
	})

	before := count
	b.rebuildProtocolCaches()
	got := count - before
	if got != 1 {
		t.Errorf("expected exactly one warning per excluded server per rebuild, got %d", got)
	}
}
