package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyMockMCP is a dual-version upstream that negotiates 2026 on discovery
// and can list over a 2025 session.
type legacyMockMCP struct {
	*MockMCP
	versions    []string
	legacyTools []mcp.Tool
	legacyErr   error
	legacyCalls int
}

func (m *legacyMockMCP) SupportedVersions() []string { return m.versions }

// Connect only reports a new session when not already connected, like
// MCPServer.Connect.
func (m *legacyMockMCP) Connect(ctx context.Context, onConnected func()) error {
	if m.connected.Load() {
		return nil
	}
	return m.MockMCP.Connect(ctx, onConnected)
}

func (m *legacyMockMCP) ListToolsLegacy(context.Context) (*mcp.ListToolsResult, error) {
	m.legacyCalls++
	if m.legacyErr != nil {
		return nil, m.legacyErr
	}
	res := &mcp.ListToolsResult{}
	for i := range m.legacyTools {
		res.Tools = append(res.Tools, &m.legacyTools[i])
	}
	return res, nil
}

// notifyCounter counts NotifyMetadataChanged calls.
type notifyCounter struct {
	*MockToolsAdderDeleter
	notified int
}

func (n *notifyCounter) NotifyMetadataChanged() { n.notified++ }

func newLegacyMock(versions []string, negotiated string) *legacyMockMCP {
	base := newMockMCP("gh", "gh_")
	base.protocolVersion = negotiated
	base.hasToolsCap = false
	out := map[string]any{"type": "object"}
	base.tools = []mcp.Tool{{Name: "get_me", InputSchema: map[string]any{"type": "object"}, OutputSchema: out}}
	return &legacyMockMCP{MockMCP: base, versions: versions}
}

func TestMCPManager_StatefulOutputSchemaFromLegacyListing(t *testing.T) {
	dual := []string{"2025-11-25", "2026-07-28"}
	legacyOut := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}

	t.Run("dual-version listed over 2026: 2025 schemas recorded per served name", func(t *testing.T) {
		m := newLegacyMock(dual, "2026-07-28")
		m.legacyTools = []mcp.Tool{{Name: "get_me", InputSchema: map[string]any{"type": "object"}, OutputSchema: legacyOut}, {Name: "plain", InputSchema: map[string]any{"type": "object"}}}
		gw := &notifyCounter{MockToolsAdderDeleter: newMockToolsAdderDeleter()}
		man, err := NewUpstreamMCPManager(m, gw, nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
		require.NoError(t, err)
		man.manage(context.Background(), eventTypeTimer)

		s, known := man.StatefulOutputSchema("gh_get_me")
		assert.True(t, known)
		assert.Equal(t, legacyOut, s)
		s, known = man.StatefulOutputSchema("gh_plain")
		assert.True(t, known, "listed on 2025 without an outputSchema is known")
		assert.Nil(t, s)
		_, known = man.StatefulOutputSchema("gh_absent")
		assert.False(t, known)

		// unchanged tool set: no re-list
		man.manage(context.Background(), eventTypeTimer)
		assert.Equal(t, 1, m.legacyCalls)
		assert.Equal(t, 0, gw.notified)

		// reconnected over 2026 with only the 2025 definition changed: re-listed
		legacyOut2 := map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}}
		m.legacyTools = []mcp.Tool{{Name: "get_me", InputSchema: map[string]any{"type": "object"}, OutputSchema: legacyOut2}}
		_ = m.Disconnect()
		man.manage(context.Background(), eventTypeTimer)
		assert.Equal(t, 2, m.legacyCalls)
		s, known = man.StatefulOutputSchema("gh_get_me")
		assert.True(t, known)
		assert.Equal(t, legacyOut2, s)
		assert.Equal(t, 1, gw.notified)

		// list_changed with only the 2025 definition changed: re-listed
		m.legacyTools = []mcp.Tool{{Name: "get_me", InputSchema: map[string]any{"type": "object"}}}
		man.manage(context.Background(), eventTypeToolNotification)
		assert.Equal(t, 3, m.legacyCalls)
		s, known = man.StatefulOutputSchema("gh_get_me")
		assert.True(t, known)
		assert.Nil(t, s, "2025 schema removed upstream")
		assert.Equal(t, 2, gw.notified)

		// reconnected over 2025: the 2025 schemas no longer apply
		m.protocolVersion = "2025-11-25"
		man.manage(context.Background(), eventTypeTimer)
		_, known = man.StatefulOutputSchema("gh_get_me")
		assert.False(t, known)
		assert.Equal(t, 3, gw.notified, "stateful cache rebuilt for the 2025 definitions")
	})

	t.Run("reconnected over 2026 with the 2025 listing failing: stateful cache rebuilt", func(t *testing.T) {
		m := newLegacyMock(dual, "2025-11-25")
		gw := &notifyCounter{MockToolsAdderDeleter: newMockToolsAdderDeleter()}
		man, err := NewUpstreamMCPManager(m, gw, nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
		require.NoError(t, err)
		man.manage(context.Background(), eventTypeTimer)

		m.protocolVersion = "2026-07-28"
		m.legacyErr = errors.New("boom")
		man.manage(context.Background(), eventTypeTimer)
		_, known := man.StatefulOutputSchema("gh_get_me")
		assert.False(t, known)
		assert.Equal(t, 1, gw.notified)
	})

	t.Run("legacy listing failure: unknown, retried next cycle", func(t *testing.T) {
		m := newLegacyMock(dual, "2026-07-28")
		m.legacyErr = errors.New("boom")
		gw := &notifyCounter{MockToolsAdderDeleter: newMockToolsAdderDeleter()}
		man, err := NewUpstreamMCPManager(m, gw, nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
		require.NoError(t, err)
		man.manage(context.Background(), eventTypeTimer)
		_, known := man.StatefulOutputSchema("gh_get_me")
		assert.False(t, known)
		assert.Equal(t, 0, gw.notified)

		m.legacyErr = nil
		m.legacyTools = []mcp.Tool{{Name: "get_me", InputSchema: map[string]any{"type": "object"}}}
		man.manage(context.Background(), eventTypeTimer)
		assert.Equal(t, 2, m.legacyCalls, "retried while unknown")
		assert.Equal(t, 1, gw.notified, "late listing rebuilds the stateful cache")
		s, known := man.StatefulOutputSchema("gh_get_me")
		assert.True(t, known)
		assert.Nil(t, s)
	})

	t.Run("listed over 2025: no legacy listing", func(t *testing.T) {
		m := newLegacyMock(dual, "2025-11-25")
		man, err := NewUpstreamMCPManager(m, newMockToolsAdderDeleter(), nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
		require.NoError(t, err)
		man.manage(context.Background(), eventTypeTimer)
		assert.Equal(t, 0, m.legacyCalls)
		_, known := man.StatefulOutputSchema("gh_get_me")
		assert.False(t, known)
	})

	t.Run("2026-only upstream: no legacy listing", func(t *testing.T) {
		m := newLegacyMock([]string{"2026-07-28"}, "2026-07-28")
		man, err := NewUpstreamMCPManager(m, newMockToolsAdderDeleter(), nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
		require.NoError(t, err)
		man.manage(context.Background(), eventTypeTimer)
		assert.Equal(t, 0, m.legacyCalls)
	})
}

func TestListToolsLegacy(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{protocol.Version2025, protocol.Version2026},
		PageSize:                  1,
	})
	noop := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	srv.AddTool(&mcp.Tool{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)}, noop)
	srv.AddTool(&mcp.Tool{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`)}, noop)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	res, err := up.ListToolsLegacy(t.Context())
	require.NoError(t, err)
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	assert.ElementsMatch(t, []string{"a", "b"}, names, "all pages listed")
}

func TestListToolsLegacy_2026OnlyUpstreamFails(t *testing.T) {
	srv := newStateless2026Server()
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true}))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", nil)
	_, err := up.ListToolsLegacy(t.Context())
	require.Error(t, err)
}
