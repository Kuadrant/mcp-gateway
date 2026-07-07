package broker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// notifyHarness wires a gatewayServer to a real streamable HTTP handler so
// tests exercise the SDK's async (debounced) list_changed dispatch.
type notifyHarness struct {
	gs *gatewayServer
	ts *httptest.Server
}

func newNotifyHarness(t *testing.T) *notifyHarness {
	t.Helper()
	var counter atomic.Int64
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "test-gateway", Version: "0.0.1"},
		&mcp.ServerOptions{
			GetSessionID: func() string { return fmt.Sprintf("sess-%d", counter.Add(1)) },
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		},
	)
	gs := newGatewayServer(srv)
	srv.AddSendingMiddleware(gs.notifyTargetMiddleware())

	// JSONResponse mirrors production (MCPHandler): notifications must still
	// reach clients via the standalone GET stream when POSTs are plain JSON
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &notifyHarness{gs: gs, ts: ts}
}

// notifyClient is a connected client session plus a channel that receives a
// signal for every tools/list_changed notification delivered to it.
type notifyClient struct {
	session  *mcp.ClientSession
	notified chan struct{}
}

func (h *notifyHarness) connect(t *testing.T) *notifyClient {
	t.Helper()
	nc := &notifyClient{notified: make(chan struct{}, 16)}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			nc.notified <- struct{}{}
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.ts.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	nc.session = session
	return nc
}

func (nc *notifyClient) waitNotified(t *testing.T, within time.Duration) bool {
	t.Helper()
	select {
	case <-nc.notified:
		return true
	case <-time.After(within):
		return false
	}
}

// sendTargeted issues a targeted tools/list_changed for the given session.
func sendTargeted(gs *gatewayServer, sessionID string) {
	gs.TriggerToolsListChanged(sessionID)
}

// regression for the SDK migration: the SDK dispatches list_changed
// asynchronously (~10ms debounce), so clearing the notify target
// synchronously after the sentinel cycle meant the sending middleware
// always saw an empty target and broadcast to every session.
func TestTriggerToolsListChanged_TargetedNotDeliveredToOthers(t *testing.T) {
	h := newNotifyHarness(t)
	target := h.connect(t)
	other := h.connect(t)

	sendTargeted(h.gs, target.session.ID())

	require.True(t, target.waitNotified(t, 5*time.Second), "target session should receive tools/list_changed")
	require.False(t, other.waitNotified(t, 500*time.Millisecond), "non-target session must not receive tools/list_changed")
}

func TestTriggerToolsListChanged_ConcurrentTargetsDeliverCorrectly(t *testing.T) {
	h := newNotifyHarness(t)
	a := h.connect(t)
	b := h.connect(t)
	c := h.connect(t)

	var wg sync.WaitGroup
	for _, nc := range []*notifyClient{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendTargeted(h.gs, nc.session.ID())
		}()
	}
	wg.Wait()

	require.True(t, a.waitNotified(t, 5*time.Second), "session a should receive its scope change notification")
	require.True(t, b.waitNotified(t, 5*time.Second), "session b should receive its scope change notification")
	require.False(t, c.waitNotified(t, 500*time.Millisecond), "untargeted session must not receive tools/list_changed")
}

func TestTriggerToolsListChanged_BroadcastReachesAll(t *testing.T) {
	h := newNotifyHarness(t)
	a := h.connect(t)
	b := h.connect(t)

	// empty target means broadcast
	h.gs.TriggerToolsListChanged("")

	require.True(t, a.waitNotified(t, 5*time.Second))
	require.True(t, b.waitNotified(t, 5*time.Second))
}

// an upstream-driven tool change coalesced with a targeted scope change
// must still reach every session.
func TestUpstreamChangeDuringTargetedNotify_Broadcasts(t *testing.T) {
	h := newNotifyHarness(t)
	target := h.connect(t)
	other := h.connect(t)

	sendTargeted(h.gs, target.session.ID())
	h.gs.AddTools(upstream.GatewayTool{
		Tool:    mcp.Tool{Name: "new_upstream_tool", InputSchema: map[string]any{"type": "object"}},
		Handler: upstream.NoopToolHandler,
	})

	require.True(t, target.waitNotified(t, 5*time.Second))
	require.True(t, other.waitNotified(t, 5*time.Second), "upstream change must reach non-target sessions")
}
