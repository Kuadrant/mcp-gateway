package broker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// regression for #1625: pre-2026 clients require outputSchema.type to be
// exactly "object" and strict ones (the TS SDK) reject the whole list
// otherwise, so their tools/list drops any other root type and leaves
// conforming schemas intact. 2026-07-28 clients keep the original.
func TestToolsList_DropsNonObjectOutputSchemaForLegacyClients(t *testing.T) {
	h := newCompatHarness(t)

	_, manager := createTestManagerMCP(t, "up1", "", []mcp.Tool{
		{Name: "nullable_out"}, {Name: "object_out"}, {Name: "untyped_out"}, {Name: "no_out"},
	})
	// cacheable so the 2026 stateless list serves these from the shared cache
	manager.SetCacheMetadataForTesting(upstream.CacheMetadata{TTLMs: 60000, CacheScope: upstream.CacheScopePublic}, upstream.CacheMetadata{})
	h.b.mcpServers["up1"] = upstream.NewActiveForTesting(manager)
	h.b.serverVersions.Store(config.UpstreamMCPID("up1"), []string{"2025-11-25", "2026-07-28"})
	meta := mcp.Meta{"kuadrant/id": "up1"}

	handler := func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	objectOut := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}}
	h.b.gatewayServer.AddTools(
		upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:         "nullable_out",
				InputSchema:  map[string]any{"type": "object"},
				OutputSchema: map[string]any{"type": []any{"object", "null"}},
				Meta:         meta,
			},
			Handler: handler,
		},
		upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:         "object_out",
				InputSchema:  map[string]any{"type": "object"},
				OutputSchema: objectOut,
				Meta:         meta,
			},
			Handler: handler,
		},
		upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:         "untyped_out",
				InputSchema:  map[string]any{"type": "object"},
				OutputSchema: map[string]any{"properties": map[string]any{"n": map[string]any{"type": "integer"}}},
				Meta:         meta,
			},
			Handler: handler,
		},
		upstream.GatewayTool{
			Tool:    mcp.Tool{Name: "no_out", InputSchema: map[string]any{"type": "object"}, Meta: meta},
			Handler: handler,
		},
	)

	h.b.rebuildProtocolCaches()

	sid := h.initialize(t)
	res := h.post(t, sid, h.toolsListBody())
	require.Equal(t, http.StatusOK, res.status, res.body)

	var envelope struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.body), &envelope))
	byName := map[string]map[string]json.RawMessage{}
	for _, tool := range envelope.Result.Tools {
		var name string
		require.NoError(t, json.Unmarshal(tool["name"], &name))
		byName[name] = tool
	}

	require.Contains(t, byName, "nullable_out", "tool must still be listed")
	require.NotContains(t, byName["nullable_out"], "outputSchema")
	require.Contains(t, byName, "untyped_out")
	require.NotContains(t, byName["untyped_out"], "outputSchema")
	require.Contains(t, byName, "no_out")
	require.NotContains(t, byName["no_out"], "outputSchema")
	wantOut, err := json.Marshal(objectOut)
	require.NoError(t, err)
	require.JSONEq(t, string(wantOut), string(byName["object_out"]["outputSchema"]))

	// 2026-07-28 clients go through the stateless handler and keep the original schema
	ts := httptest.NewServer(h.b.MCPHandler())
	t.Cleanup(ts.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"0.0.1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/list")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), `"outputSchema":{"type":["object","null"]}`, string(body))
}

func TestStripNonObjectOutputSchemas(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	nullable := &mcp.Tool{Name: "nullable", OutputSchema: map[string]any{"type": []any{"object", "null"}}}
	object := &mcp.Tool{Name: "object", OutputSchema: map[string]any{"type": "object"}}
	untyped := &mcp.Tool{Name: "untyped", OutputSchema: map[string]any{"$ref": "#/$defs/r"}}
	none := &mcp.Tool{Name: "none"}
	tools := []*mcp.Tool{nullable, object, untyped, none, nil}

	b.stripNonObjectOutputSchemas(context.Background(), tools)

	require.Nil(t, tools[0].OutputSchema)
	require.Same(t, object, tools[1], "conforming tools are served as-is")
	require.Nil(t, tools[2].OutputSchema)
	require.Same(t, none, tools[3])
	require.Nil(t, tools[4])
	// the cached tools shared with 2026-07-28 clients are never mutated
	require.NotNil(t, nullable.OutputSchema)
	require.NotNil(t, untyped.OutputSchema)
}
