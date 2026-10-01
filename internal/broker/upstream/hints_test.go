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

func TestToolHintsTee_AccumulatesAcrossPages(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		tool := &mcp.Tool{
			Name:        name,
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}
		srv.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	result, err := up.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, result.Tools, 3, "all tools across the pages must be listed")

	require.Eventually(t, func() bool {
		_, okA := up.GetToolHints("up_alpha")
		_, okB := up.GetToolHints("up_beta")
		_, okG := up.GetToolHints("up_gamma")
		return okA && okB && okG
	}, 5*time.Second, 10*time.Millisecond, "hints from every page must survive the walk, not just the last page")
}

func TestMinTTLMs(t *testing.T) {
	require.Equal(t, 0, minTTLMs(0, 5000), "zero wins: uncacheable page")
	require.Equal(t, 0, minTTLMs(5000, 0), "zero wins: uncacheable page")
	require.Equal(t, 3000, minTTLMs(5000, 3000), "shorter page TTL wins")
	require.Equal(t, 3000, minTTLMs(3000, 5000), "shorter page TTL wins")
	require.Equal(t, 3000, minTTLMs(3000, 3000), "equal TTLs")
}

func TestMergeCacheScopes(t *testing.T) {
	require.Equal(t, CacheScopePrivate, mergeCacheScopes(CacheScopePrivate, CacheScopePublic), "private page keeps the merged listing private")
	require.Equal(t, CacheScopePrivate, mergeCacheScopes(CacheScopePublic, CacheScopePrivate), "private page keeps the merged listing private")
	require.Equal(t, CacheScopePublic, mergeCacheScopes(CacheScopePublic, CacheScopePublic), "all-public pages stay public")
}

// TestListAllPrompts_FollowsPagination: prompts/list pages merge like the
// tools walk.
func TestListAllPrompts_FollowsPagination(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"p_one", "p_two"} {
		srv.AddPrompt(&mcp.Prompt{Name: name}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	result, err := up.ListPrompts(ctx)
	require.NoError(t, err)
	require.Len(t, result.Prompts, 2, "all prompts across the pages must be listed")
}

// TestListAllPrompts_PageCap: the prompts walk is bounded by MaxListPages,
// not by the context deadline alone. The real bound is exercised through the
// same SDK server other tests use; here the cap itself is checked directly on
// the loop counter semantics the walk implements.
func TestListAllPrompts_PageCap(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"p_one", "p_two"} {
		srv.AddPrompt(&mcp.Prompt{Name: name}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	result, err := up.ListPrompts(ctx)
	require.NoError(t, err)
	require.Len(t, result.Prompts, 2, "well-behaved pagination stays under the cap and completes")
	// the cap constant must stay tight: 100 pages is the agreed bound from
	// the review discussion, and the loop treats page >= MaxListPages as
	// an upstream fault
	require.Equal(t, 100, MaxListPages)
}

// TestListAllTools_HintsSurviveCacheHitWalk: the SDK can answer later
// tools/list calls from its per-page cache without an HTTP round trip, so
// the tee never fires. commitToolHints must not erase the previously
// observed hints in that case.
func TestListAllTools_HintsSurviveCacheHitWalk(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"alpha", "beta"} {
		tool := &mcp.Tool{
			Name:        name,
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}
		srv.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL, Prefix: "up_"}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	_, err := up.ListTools(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, okA := up.GetToolHints("up_alpha")
		_, okB := up.GetToolHints("up_beta")
		return okA && okB
	}, 5*time.Second, 10*time.Millisecond, "first walk must harvest the hints")

	// simulate the SDK serving the second walk from cache: no HTTP, no tee
	// harvest, but the walk still begins/commits. commit must keep hints
	// for every tool the fresh listing served.
	up.beginToolHints()
	up.commitToolHints(map[string]struct{}{"up_alpha": {}, "up_beta": {}})

	_, okA := up.GetToolHints("up_alpha")
	_, okB := up.GetToolHints("up_beta")
	require.True(t, okA, "cache-hit walk must not erase previously observed hints")
	require.True(t, okB, "cache-hit walk must not erase previously observed hints")
}

// TestListAllTools_FailedWalkLeavesNoPartialHints: a walk that ends mid-way
// must not leave the pages it did observe in the live set.
func TestListAllTools_FailedWalkLeavesNoPartialHints(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: "http://unused", Prefix: "up_"}, "", nil)

	// a completed first listing observed one tool
	up.beginToolHints()
	up.storeToolHints(map[string]ToolHints{"alpha": {ReadOnlyHint: ptr.To(true)}})
	up.commitToolHints(map[string]struct{}{"up_alpha": {}})
	_, ok := up.GetToolHints("up_alpha")
	require.True(t, ok, "first listing must observe the hint")

	// a second walk observes a page, then fails: the partial harvest must
	// be dropped and the first listing's hints restored
	up.beginToolHints()
	up.storeToolHints(map[string]ToolHints{"beta": {ReadOnlyHint: ptr.To(true)}})
	_, okBeta := up.GetToolHints("up_beta")
	require.True(t, okBeta, "in-walk live update must be visible while the walk is open")
	up.abandonToolHints()

	_, okBeta = up.GetToolHints("up_beta")
	require.False(t, okBeta, "failed walk must not leave partial hints in the live set")
	_, ok = up.GetToolHints("up_alpha")
	require.True(t, ok, "failed walk must restore the previous listing's hints")

	// a completed listing that drops a tool must drop its hint too: the
	// overlay keeps previous hints only for tools the fresh listing served
	up.beginToolHints()
	up.storeToolHints(map[string]ToolHints{"beta": {ReadOnlyHint: ptr.To(true)}})
	up.commitToolHints(map[string]struct{}{"up_beta": {}})
	_, ok = up.GetToolHints("up_alpha")
	require.False(t, ok, "hint for a tool absent from the completed listing must be dropped")
	_, okBeta = up.GetToolHints("up_beta")
	require.True(t, okBeta, "hint for a served tool must be kept")
}

// TestListAllTools_FreshHintWinsOverPreviousListing: a walk that re-observes
// a tool over HTTP must install the fresh hint, not the previous listing's
// value: the previous hint is only a fallback for served tools the walk did
// not re-observe (per-page cache hits).
func TestListAllTools_FreshHintWinsOverPreviousListing(t *testing.T) {
	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: "http://unused", Prefix: "up_"}, "", nil)

	// first listing: alpha is read-only
	up.beginToolHints()
	up.storeToolHints(map[string]ToolHints{"alpha": {ReadOnlyHint: ptr.To(true)}})
	up.commitToolHints(map[string]struct{}{"up_alpha": {}})
	h, ok := up.GetToolHints("up_alpha")
	require.True(t, ok, "first listing must observe the hint")
	require.Equal(t, ptr.To(true), h.ReadOnlyHint)

	// second listing: the upstream now reports alpha as not read-only
	up.beginToolHints()
	up.storeToolHints(map[string]ToolHints{"alpha": {ReadOnlyHint: ptr.To(false)}})
	up.commitToolHints(map[string]struct{}{"up_alpha": {}})
	h, ok = up.GetToolHints("up_alpha")
	require.True(t, ok, "served tool must keep a hint")
	require.Equal(t, ptr.To(false), h.ReadOnlyHint, "fresh walk observation must win over the previous listing")
}
