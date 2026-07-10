package upstream

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func watcherTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

// watcherStreams reports how many healthy streams the current watcher has
// entered; the server has bound the stream by the time it is counted, so
// events sent afterwards cannot be lost to the attach race.
func watcherStreams(up *MCPServer) int64 {
	up.clientMu.RLock()
	defer up.clientMu.RUnlock()
	if up.watcher == nil {
		return 0
	}
	return up.watcher.streamsEstablished.Load()
}

func waitForWatcherStream(t *testing.T, up *MCPServer) {
	t.Helper()
	require.Eventually(t, func() bool { return watcherStreams(up) >= 1 }, 10*time.Second, 10*time.Millisecond,
		"watcher never established a notification stream")
}

func shrinkWatchBackoff(t *testing.T, d time.Duration) {
	t.Helper()
	old := watchBackoff
	watchBackoff.Duration = d
	watchBackoff.Jitter = 0
	t.Cleanup(func() { watchBackoff = old })
}

// regression: OnNotification used to be a no-op before Connect (nil client),
// silently dropping list-changed deliveries. a handler registered before
// Connect must receive notifications pushed by the upstream over the
// broker-owned standalone stream.
func TestNotificationWatcher_DeliversUpstreamListChanged(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "up", Version: "0.0.1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "up", URL: ts.URL}, "", watcherTestLogger())
	got := make(chan string, 4)
	up.OnNotification(func(method string) { got <- method })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	// events sent while no stream is attached are not buffered by the sdk
	// server, so wait for the watcher to bind before changing the tool set
	waitForWatcherStream(t, up)

	srv.AddTool(&mcp.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})

	select {
	case method := <-got:
		require.Equal(t, "notifications/tools/list_changed", method)
	case <-time.After(10 * time.Second):
		t.Fatal("notification never reached a handler registered before Connect")
	}
}

// a pushed list_changed must flow through the manager's existing refresh
// path and update the gateway registry without waiting for the ticker.
func TestNotificationWatcher_TriggersManagerRefetch(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "push-server", Version: "0.0.1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	srv.AddTool(&mcp.Tool{Name: "tool1", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "push-server", URL: ts.URL, Prefix: "push_"}, "", watcherTestLogger())
	gateway := NewMockGatewayServer()
	// hour-long ticker: only the pushed notification can explain a refresh
	manager, err := NewUpstreamMCPManager(up, gateway, nil, watcherTestLogger(), time.Hour, mcpv1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := manager.Start(ctx)
	defer active.Stop()

	require.Eventually(t, func() bool { return len(gateway.ListTools()) == 1 }, 10*time.Second, 10*time.Millisecond,
		"initial tools should be registered")
	waitForWatcherStream(t, up)

	srv.AddTool(&mcp.Tool{Name: "tool2", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})

	require.Eventually(t, func() bool {
		tools := gateway.ListTools()
		_, has := tools["push_tool2"]
		return has
	}, 10*time.Second, 10*time.Millisecond, "pushed list_changed should trigger a re-list")
}

// an upstream answering the standalone GET with 405 does not offer the
// stream: the watcher must stop for good without touching the session.
func TestNotificationWatcher_405StopsPermanently(t *testing.T) {
	shrinkWatchBackoff(t, 10*time.Millisecond)

	s := mcp.NewServer(&mcp.Implementation{Name: "no-sse", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "no-sse", URL: srv.URL}, "", watcherTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	up.clientMu.RLock()
	w := up.watcher
	up.clientMu.RUnlock()
	require.NotNil(t, w)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher should stop permanently on 405")
	}
	require.Equal(t, int32(1), gets.Load(), "405 must not be retried")

	_, err := up.ListTools(ctx)
	require.NoError(t, err, "session must be unaffected by a 405 on the stream")
}

// transient stream failures retry forever with growing backoff and never
// touch the session.
func TestNotificationWatcher_RetriesNonFatally(t *testing.T) {
	base := 20 * time.Millisecond
	shrinkWatchBackoff(t, base)

	s := mcp.NewServer(&mcp.Implementation{Name: "flaky-sse", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	var mu sync.Mutex
	var getTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			getTimes = append(getTimes, time.Now())
			mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "flaky-sse", URL: srv.URL}, "", watcherTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(getTimes) >= 4
	}, 10*time.Second, 10*time.Millisecond, "watcher should keep retrying transient failures")

	// sleeps can only stretch under load, so a lower bound on the total
	// spacing is flake-safe: with jitter zeroed the first three gaps are
	// base, 2*base and 4*base
	mu.Lock()
	elapsed := getTimes[3].Sub(getTimes[0])
	mu.Unlock()
	require.GreaterOrEqual(t, elapsed, 7*base-base/2, "retry gaps should grow with backoff")

	_, err := up.ListTools(ctx)
	require.NoError(t, err, "session must be unaffected by stream retries")
}

// a server ping delivered on the stream must be answered so keepalive
// enabled upstreams do not conclude the broker session is dead.
func TestNotificationWatcher_AnswersPing(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "pinger", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	posts := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":\"keepalive-1\",\"method\":\"ping\"}\n\n"))
			require.NoError(t, http.NewResponseController(w).Flush())
			<-r.Context().Done()
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			posts <- string(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			inner.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "pinger", URL: srv.URL}, "", watcherTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case body := <-posts:
			if strings.Contains(body, `"keepalive-1"`) && strings.Contains(body, `"result"`) {
				return
			}
		case <-deadline:
			t.Fatal("ping on the notification stream was never answered")
		}
	}
}

// disconnect must tear down the watcher goroutine and its hanging GET;
// goleak verifies nothing started by this test survives it.
func TestNotificationWatcher_CleanShutdown(t *testing.T) {
	leakOpts := []goleak.Option{
		goleak.IgnoreCurrent(),
		// the sdk server fixture keeps a jsonrpc session reader running
		// with no exported way to close it (closeAll is unexported); it is
		// server-side scaffolding, not broker code under test
		goleak.IgnoreAnyFunction("github.com/modelcontextprotocol/go-sdk/internal/jsonrpc2.(*Connection).readIncoming"),
	}
	defer func() {
		// remaining teardown (http idle connections, client readers) is
		// asynchronous; give it a moment to quiesce before requiring a
		// clean goroutine set
		deadline := time.Now().Add(5 * time.Second)
		for goleak.Find(leakOpts...) != nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		goleak.VerifyNone(t, leakOpts...)
	}()

	s := mcp.NewServer(&mcp.Implementation{Name: "shutdown", Version: "0.0.1"}, nil)
	inner := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, nil)
	getGone := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
			<-r.Context().Done() // hang the stream until torn down
			getGone <- struct{}{}
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "shutdown", URL: srv.URL}, "", watcherTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, up.Connect(ctx, func() {}))
	waitForWatcherStream(t, up)

	up.clientMu.RLock()
	w := up.watcher
	up.clientMu.RUnlock()
	require.NotNil(t, w)

	require.NoError(t, up.Disconnect())

	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher goroutine did not exit on disconnect")
	}
	select {
	case <-getGone:
	case <-time.After(5 * time.Second):
		t.Fatal("hanging GET was not torn down on disconnect")
	}

	// disconnect is idempotent with the watcher already stopped
	require.NoError(t, up.Disconnect())
}
