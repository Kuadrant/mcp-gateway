package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

// mark3labs-shaped upstream bytes: all four hints are *bool, absent keys
// stay absent and explicit false survives.
const rawListResult = `{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
	`{"name":"plain","inputSchema":{"type":"object"}},` +
	`{"name":"empty_ann","inputSchema":{"type":"object"},"annotations":{}},` +
	`{"name":"mixed","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"destructiveHint":false}},` +
	`{"name":"explicit_false","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false,"idempotentHint":false}}` +
	`]}}`

func requireHarvest(t *testing.T, hints map[string]ToolHints) {
	t.Helper()
	require.Len(t, hints, 4)

	require.Nil(t, hints["plain"].ReadOnlyHint)
	require.Nil(t, hints["plain"].Raw, "tool without annotations has no raw bytes")

	require.Nil(t, hints["empty_ann"].ReadOnlyHint)
	require.JSONEq(t, `{}`, string(hints["empty_ann"].Raw))

	require.Equal(t, ptr.To(true), hints["mixed"].ReadOnlyHint)
	require.Equal(t, ptr.To(false), hints["mixed"].DestructiveHint)
	require.Nil(t, hints["mixed"].IdempotentHint)
	require.Nil(t, hints["mixed"].OpenWorldHint)
	require.Equal(t, `{"readOnlyHint":true,"destructiveHint":false}`, string(hints["mixed"].Raw))

	require.Equal(t, ptr.To(false), hints["explicit_false"].ReadOnlyHint)
	require.Equal(t, ptr.To(false), hints["explicit_false"].IdempotentHint)
	require.Nil(t, hints["explicit_false"].DestructiveHint)
}

func TestParseToolHints_JSONFraming(t *testing.T) {
	hints, ok := parseToolHints([]byte(rawListResult))
	require.True(t, ok)
	requireHarvest(t, hints)
}

func TestParseToolHints_SSEFraming(t *testing.T) {
	stream := "event: message\ndata: " + rawListResult + "\n\n"
	payloads := ssePayloads([]byte(stream))
	require.Len(t, payloads, 1)
	hints, ok := parseToolHints(payloads[0])
	require.True(t, ok)
	requireHarvest(t, hints)
}

func TestParseToolHints_SSEFramingMultiEvent(t *testing.T) {
	stream := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n" +
		"event: message\ndata: " + rawListResult + "\n\n"
	var got map[string]ToolHints
	for _, p := range ssePayloads([]byte(stream)) {
		if hints, ok := parseToolHints(p); ok {
			got = hints
			break
		}
	}
	require.NotNil(t, got)
	requireHarvest(t, got)
}

func TestParseToolHints_NotAListResult(t *testing.T) {
	_, ok := parseToolHints([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	require.False(t, ok)
	_, ok = parseToolHints([]byte(`not json`))
	require.False(t, ok)
}

// end to end: an upstream connect + ListTools populates the hint store via
// the transport tee, prefixed with the server prefix. exercises both
// framings the SDK server can answer with.
func TestToolHintsTee_EndToEnd(t *testing.T) {
	for name, jsonResponse := range map[string]bool{"sse framing": false, "json framing": true} {
		t.Run(name, func(t *testing.T) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, nil)
			srv.AddTool(&mcp.Tool{
				Name:        "annotated",
				InputSchema: map[string]any{"type": "object"},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: ptr.To(false)},
			}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
			handler := mcp.NewStreamableHTTPHandler(
				func(*http.Request) *mcp.Server { return srv },
				&mcp.StreamableHTTPOptions{JSONResponse: jsonResponse},
			)
			ts := httptest.NewServer(handler)
			defer ts.Close()

			up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			require.NoError(t, up.Connect(ctx, func() {}))
			defer func() { _ = up.Disconnect() }()

			_, err := up.ListTools(ctx)
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				_, ok := up.GetToolHints("up_annotated")
				return ok
			}, 5*time.Second, 10*time.Millisecond, "tee must harvest hints keyed by served name")

			hints, _ := up.GetToolHints("up_annotated")
			require.Equal(t, ptr.To(true), hints.ReadOnlyHint, fmt.Sprintf("%#v", hints))
			require.Equal(t, ptr.To(false), hints.DestructiveHint)
			require.NotEmpty(t, hints.Raw)

			_, ok := up.GetToolHints("annotated")
			require.False(t, ok, "hints are keyed by prefixed name only")
		})
	}
}
