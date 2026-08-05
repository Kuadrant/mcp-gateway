package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResourceTestServer starts a real MCP server exposing the given
// resources. delay, if non-zero, is applied only to the resources/list
// method (not the initialize handshake or other calls), so a slow upstream
// in a fan-out test connects normally and only stalls on the call under test.
func newResourceTestServer(t *testing.T, resources []*mcp.Resource, delay time.Duration, pageSize int) *httptest.Server {
	t.Helper()
	opts := &mcp.ServerOptions{}
	if pageSize > 0 {
		opts.PageSize = pageSize
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, opts)
	for _, r := range resources {
		res := r
		srv.AddResource(res, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: res.URI, Text: "x"}}}, nil
		})
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			var decoded struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &decoded)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if decoded.Method == "resources/list" {
				time.Sleep(delay)
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})
	return httptest.NewServer(handler)
}

// connectedResourceUpstream builds a real, connected ActiveMCPServer backed
// by ts, ready to insert into mcpServers. Connect uses its own generous
// timeout, independent of whatever per-fetch timeout the test configures on
// the broker.
func connectedResourceUpstream(t *testing.T, cfg config.MCPServer, ts *httptest.Server) upstream.ActiveMCPServer {
	t.Helper()
	cfg.URL = ts.URL
	mcpServer := upstream.NewUpstreamMCP(&cfg, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, mcpServer.Connect(ctx, func() {}))

	return upstream.NewActiveForTesting(manager)
}

func newResourcesTestBroker(fetchTimeout time.Duration) *mcpBrokerImpl {
	return &mcpBrokerImpl{
		mcpServers:               map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
		logger:                   slog.Default(),
		userSpecificFetchTimeout: fetchTimeout,
	}
}

func resourceURIs(result *mcp.ListResourcesResult) []string {
	uris := make([]string, len(result.Resources))
	for i, r := range result.Resources {
		uris[i] = r.URI
	}
	return uris
}

// TestFetchResources_SkipConditions exercises every reason an upstream can be
// excluded from the merged list in a single fan-out call: no prefix, a
// prefix failing the allowlist, no resources capability, and a live upstream
// error. Only the one well-formed, resource-capable, reachable server's
// resource should survive.
func TestFetchResources_SkipConditions(t *testing.T) {
	b := newResourcesTestBroker(5 * time.Second)

	good := newResourceTestServer(t, []*mcp.Resource{{Name: "good", URI: "ui://good.html"}}, 0, 0)
	defer good.Close()
	b.mcpServers["good"] = connectedResourceUpstream(t, config.MCPServer{Name: "good", Prefix: "good_"}, good)

	noPrefix := newResourceTestServer(t, []*mcp.Resource{{Name: "np", URI: "ui://noprefix.html"}}, 0, 0)
	defer noPrefix.Close()
	b.mcpServers["noprefix"] = connectedResourceUpstream(t, config.MCPServer{Name: "noprefix", Prefix: ""}, noPrefix)

	badPrefix := newResourceTestServer(t, []*mcp.Resource{{Name: "bp", URI: "ui://badprefix.html"}}, 0, 0)
	defer badPrefix.Close()
	b.mcpServers["badprefix"] = connectedResourceUpstream(t, config.MCPServer{Name: "badprefix", Prefix: "Bad-Prefix!"}, badPrefix)

	// no AddResource calls and no HasResources option: this upstream never
	// advertises the resources capability during initialize
	unsupported := newResourceTestServer(t, nil, 0, 0)
	defer unsupported.Close()
	b.mcpServers["unsupported"] = connectedResourceUpstream(t, config.MCPServer{Name: "unsupported", Prefix: "unsup_"}, unsupported)

	erroring := newResourceTestServer(t, []*mcp.Resource{{Name: "e", URI: "ui://err.html"}}, 0, 0)
	erroringUpstream := connectedResourceUpstream(t, config.MCPServer{Name: "erroring", Prefix: "err_"}, erroring)
	erroring.Close() // connected successfully, then the upstream goes away before the fetch
	b.mcpServers["erroring"] = erroringUpstream

	result := &mcp.ListResourcesResult{}
	b.FetchResources(context.Background(), result)

	assert.Equal(t, []string{"ui://good_good.html"}, resourceURIs(result))
}

// TestFetchResources_ConcurrentFanOut proves one slow/down upstream does not
// add its own timeout to every other upstream's contribution: a fast server
// and a server slower than the configured fetch timeout are queried
// together, and the call must return close to the timeout, not the slow
// server's actual delay, with the fast server's resource still present.
func TestFetchResources_ConcurrentFanOut(t *testing.T) {
	const fetchTimeout = 100 * time.Millisecond
	const slowDelay = 500 * time.Millisecond
	b := newResourcesTestBroker(fetchTimeout)

	fast := newResourceTestServer(t, []*mcp.Resource{{Name: "fast", URI: "ui://fast.html"}}, 0, 0)
	defer fast.Close()
	b.mcpServers["fast"] = connectedResourceUpstream(t, config.MCPServer{Name: "fast", Prefix: "fast_"}, fast)

	slow := newResourceTestServer(t, []*mcp.Resource{{Name: "slow", URI: "ui://slow.html"}}, slowDelay, 0)
	defer slow.Close()
	b.mcpServers["slow"] = connectedResourceUpstream(t, config.MCPServer{Name: "slow", Prefix: "slow_"}, slow)

	result := &mcp.ListResourcesResult{}
	start := time.Now()
	b.FetchResources(context.Background(), result)
	elapsed := time.Since(start)

	assert.Equal(t, []string{"ui://fast_fast.html"}, resourceURIs(result))
	assert.Less(t, elapsed, slowDelay, "slow upstream's timeout must not be added to the fast upstream's contribution")
}

// TestFetchResources_OnlyUIRewrittenOthersUntouched confirms the rewrite is
// scoped to the ui:// scheme, as the design requires: a non-ui:// resource
// must reach the client exactly as the upstream returned it.
func TestFetchResources_OnlyUIRewrittenOthersUntouched(t *testing.T) {
	b := newResourcesTestBroker(5 * time.Second)

	ts := newResourceTestServer(t, []*mcp.Resource{
		{Name: "ui", URI: "ui://template.html"},
		{Name: "other", URI: "https://example.com/doc.html"},
	}, 0, 0)
	defer ts.Close()
	b.mcpServers["mixed"] = connectedResourceUpstream(t, config.MCPServer{Name: "mixed", Prefix: "mx_"}, ts)

	result := &mcp.ListResourcesResult{}
	b.FetchResources(context.Background(), result)

	assert.ElementsMatch(t, []string{"ui://mx_template.html", "https://example.com/doc.html"}, resourceURIs(result))
}

// TestFetchResources_NextCursorLoggedNotFollowed confirms pagination is
// observed, not chased: only the first page an upstream returns ends up in
// the merged result, even when the upstream signals more pages exist.
func TestFetchResources_NextCursorLoggedNotFollowed(t *testing.T) {
	var buf recordingHandler
	b := newResourcesTestBroker(5 * time.Second)
	b.logger = slog.New(&buf)

	// PageSize 1 with 2 registered resources forces a NextCursor on the first page
	ts := newResourceTestServer(t, []*mcp.Resource{
		{Name: "a", URI: "ui://a.html"},
		{Name: "b", URI: "ui://b.html"},
	}, 0, 1)
	defer ts.Close()
	b.mcpServers["paged"] = connectedResourceUpstream(t, config.MCPServer{Name: "paged", Prefix: "pg_"}, ts)

	result := &mcp.ListResourcesResult{}
	b.FetchResources(context.Background(), result)

	assert.Len(t, result.Resources, 1, "only the first page should be merged, pagination is not followed")
	assert.True(t, buf.hasMessage("paginated"), "expected a log entry noting the unfollowed nextCursor")
}

type nilResultServer struct{ mockActiveServer }

func (n *nilResultServer) ListResources(context.Context) (*mcp.ListResourcesResult, error) {
	return nil, nil
}

// TestFetchResourcesFromServer_NilResult guards against a contract slip an
// upstream/SDK implementation could make: ListResources returning (nil, nil)
// instead of a non-nil result with no error. fetchResourcesFromServer runs
// inside a bare g.Go() goroutine with no recover(), so dereferencing
// result.NextCursor on a nil result would crash the whole broker process,
// not just this request.
func TestFetchResourcesFromServer_NilResult(t *testing.T) {
	b := newResourcesTestBroker(5 * time.Second)
	resources, err := b.fetchResourcesFromServer(context.Background(), &nilResultServer{}, "pfx_")

	require.NoError(t, err)
	assert.Nil(t, resources)
}

// recordingHandler is a minimal slog.Handler that records whether any log
// record's message contains a given substring, just enough to assert a log
// line fired without pulling in a full logging test framework.
type recordingHandler struct {
	messages []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.messages = append(h.messages, r.Message)
	return nil
}
func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) hasMessage(substr string) bool {
	for _, m := range h.messages {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}
