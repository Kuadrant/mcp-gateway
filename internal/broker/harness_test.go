package broker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// brokerHarness serves a broker's HTTP surface for wire-level tests. the
// composition of the served handler varies per constructor; the broker,
// validator and request helpers are shared.
type brokerHarness struct {
	b *mcpBrokerImpl
	// wrapped is the resurrection handler when it is in the served stack
	wrapped *sessionResurrectionHandler
	ts      *httptest.Server
	rpcID   atomic.Int64
	// invalid flips the validator to reject every session
	invalid    atomic.Bool
	terminated sync.Map
}

type compatResponse struct {
	status int
	header http.Header
	body   string
}

// validate mirrors JWTManager.Validate over recognisable prefixes: "valid"
// passes, "terminated" is valid-but-terminated, anything else fails to
// parse. the invalid flag rejects everything, simulating expiry mid-session.
func (h *brokerHarness) validate(s string) (bool, error) {
	switch {
	case h.invalid.Load():
		return true, nil
	case strings.HasPrefix(s, "valid"):
		return false, nil
	case strings.HasPrefix(s, "terminated"):
		return true, nil
	default:
		return true, fmt.Errorf("failed to parse token: bad segments")
	}
}

// newBrokerHarness builds a broker wired to the harness validator and
// terminator, then serves the handler composition compose returns.
func newBrokerHarness(t *testing.T, compose func(*brokerHarness) http.Handler, extra ...Option) *brokerHarness {
	t.Helper()
	h := &brokerHarness{}
	var counter atomic.Int64
	opts := []Option{
		WithDiscoveryToolsEnabled(false),
		WithSessionIDGenerator(func() string { return fmt.Sprintf("valid-gen-%d", counter.Add(1)) }),
		WithSessionValidator(h.validate),
		WithSessionTerminator(func(s string) (bool, error) {
			h.terminated.Store(s, true)
			return false, nil
		}),
	}
	opts = append(opts, extra...)
	h.b = NewBroker(slog.Default(), opts...).(*mcpBrokerImpl)
	h.ts = httptest.NewServer(compose(h))
	t.Cleanup(h.ts.Close)
	return h
}

// newCompatHarness serves the composed MCPHandler the broker binary mounts
// on /mcp: compat layer, session resurrection and the SDK handler.
func newCompatHarness(t *testing.T) *brokerHarness {
	t.Helper()
	return newBrokerHarness(t, func(h *brokerHarness) http.Handler {
		ch := h.b.MCPHandler().(*compatHandler)
		h.wrapped = ch.next.(*sessionResurrectionHandler)
		return ch
	})
}

// newResurrectionHarness serves the SDK handler wrapped with session
// resurrection only, isolating wrapper behaviour from the compat layer.
func newResurrectionHarness(t *testing.T) *brokerHarness {
	t.Helper()
	return newResurrectionHarnessIdle(t, 0)
}

// newResurrectionHarnessIdle additionally arms idle eviction: idle is set
// as the SDK handler's SessionTimeout and the wrapper's idle window. zero
// disables both.
func newResurrectionHarnessIdle(t *testing.T, idle time.Duration) *brokerHarness {
	t.Helper()
	return newBrokerHarness(t, func(h *brokerHarness) http.Handler {
		inner := mcp.NewStreamableHTTPHandler(
			func(*http.Request) *mcp.Server { return h.b.MCPServer() },
			&mcp.StreamableHTTPOptions{SessionTimeout: idle},
		)
		h.wrapped = h.b.SessionResurrectionHandler(inner).(*sessionResurrectionHandler)
		if idle > 0 {
			h.wrapped.idleTimeout = idle
		}
		return h.wrapped
	})
}

// newDiscoveryHarness serves the full /mcp stack with the discovery
// meta-tools enabled, for select_tools wire tests.
func newDiscoveryHarness(t *testing.T) *brokerHarness {
	t.Helper()
	return newBrokerHarness(t, func(h *brokerHarness) http.Handler {
		ch := h.b.MCPHandler().(*compatHandler)
		h.wrapped = ch.next.(*sessionResurrectionHandler)
		return ch
	}, WithDiscoveryToolsEnabled(true))
}

func (h *brokerHarness) isTerminated(sid string) bool {
	_, ok := h.terminated.Load(sid)
	return ok
}

func (h *brokerHarness) do(t *testing.T, method, sessionID, body string, hdr map[string]string) compatResponse {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.ts.URL, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return compatResponse{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

func (h *brokerHarness) post(t *testing.T, sessionID, body string) compatResponse {
	t.Helper()
	return h.do(t, http.MethodPost, sessionID, body, nil)
}

// initialize performs the init handshake and returns the session ID.
func (h *brokerHarness) initialize(t *testing.T) string {
	t.Helper()
	res := h.post(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)
	require.Equal(t, http.StatusOK, res.status, res.body)
	sid := res.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sid)
	ack := h.post(t, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	require.Equal(t, http.StatusAccepted, ack.status, ack.body)
	return sid
}

// toolsListBody issues a tools/list body with a unique JSON-RPC id:
// concurrent in-flight requests on one session must not collide on id.
func (h *brokerHarness) toolsListBody() string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}`, h.rpcID.Add(1))
}

func (h *brokerHarness) postToolsList(t *testing.T, sessionID string) compatResponse {
	t.Helper()
	return h.post(t, sessionID, h.toolsListBody())
}

// connect attaches a real SDK client to the served handler.
func (h *brokerHarness) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.ts.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (h *brokerHarness) liveSize() int {
	n := 0
	h.wrapped.live.Range(func(_, _ any) bool { n++; return true })
	return n
}

func (h *brokerHarness) serverSessionCount(sid string) int {
	n := 0
	for ss := range h.b.MCPServer().Sessions() {
		if ss.ID() == sid {
			n++
		}
	}
	return n
}
