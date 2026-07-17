package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// the sdk's own standalone SSE stream is disabled on broker transports
// because it opens the GET synchronously inside Connect on a detached
// context and treats any failure as session-fatal. this watcher rebuilds
// the stream at the broker's layer, where the failure semantics are ours:
// never fatal to the session, infinite reconnects with capped backoff, and
// a permanent stop only when the upstream says it does not offer the
// stream. it exists purely to push tools/prompts list-changed
// notifications into the manager's existing refresh path; the periodic
// re-list remains the freshness backstop because events sent while the
// stream is down are not buffered by upstreams without event replay.

// watchBackoff paces stream reconnects, aligned with the manager's
// backoff: growth stops at the cap but retries never do. var so tests can
// shrink it.
var watchBackoff = wait.Backoff{
	Duration: 1 * time.Second,
	Factor:   2,
	Jitter:   0.1,
	Steps:    10,
	Cap:      DefaultTickerInterval,
}

// maxSSELineBytes bounds a single SSE line; larger frames abort the scan
// and force a reconnect rather than an unbounded allocation.
const maxSSELineBytes = 1 << 20

// notificationWatcher holds an upstream standalone SSE stream open for one
// connected session, dispatching list-changed notifications to the
// upstream's handler. everything it needs is captured at construction so
// the watch goroutine never touches MCPServer state.
type notificationWatcher struct {
	endpoint        string
	httpClient      *http.Client
	sessionID       string
	protocolVersion string
	serverID        string
	notify          func(method string)
	logger          *slog.Logger

	// streamsEstablished counts healthy streams entered; read by tests to
	// know the server has bound the stream before triggering events.
	streamsEstablished atomic.Int64
	done               chan struct{} // closed when the watch goroutine exits
}

// watch runs until ctx is cancelled or the upstream reports it does not
// offer a standalone stream. all other failures retry forever with capped
// backoff and never affect the session.
func (w *notificationWatcher) watch(ctx context.Context) {
	defer close(w.done)
	bo := watchBackoff
	lastEventID := ""
	for {
		resp, err := w.get(ctx, lastEventID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Debug("upstream notification stream request failed, retrying", "upstream mcp server", w.serverID, "error", err)
			if !sleepCtx(ctx, bo.Step()) {
				return
			}
			continue
		}
		ct := resp.Header.Get("Content-Type")
		switch {
		case resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotFound:
			// the upstream does not offer the standalone stream; stop for
			// good, the session is unaffected and the ticker re-list keeps
			// tools fresh
			discardBody(resp)
			w.logger.Info("upstream does not offer a standalone notification stream, watcher stopped", "upstream mcp server", w.serverID, "status", resp.StatusCode)
			return
		case resp.StatusCode != http.StatusOK:
			discardBody(resp)
			w.logger.Debug("unexpected status for upstream notification stream, retrying", "upstream mcp server", w.serverID, "status", resp.StatusCode)
			// drop the replay cursor: some servers reject resumption they
			// cannot honour, which would otherwise poison every retry
			lastEventID = ""
			if !sleepCtx(ctx, bo.Step()) {
				return
			}
			continue
		case !isEventStream(ct):
			// a 200 without an SSE content type means the server answers
			// GET with something else entirely; treat as not offering the
			// stream, matching the sdk
			discardBody(resp)
			w.logger.Info("upstream returned non-SSE content for notification stream, watcher stopped", "upstream mcp server", w.serverID, "content-type", ct)
			return
		}

		bo = watchBackoff // healthy stream: start the next backoff cycle fresh
		w.streamsEstablished.Add(1)
		w.logger.Debug("upstream notification stream established", "upstream mcp server", w.serverID)
		lastEventID = w.consumeStream(ctx, resp.Body, lastEventID)
		_ = resp.Body.Close()
		if ctx.Err() != nil {
			return
		}
		w.logger.Debug("upstream notification stream ended, reconnecting", "upstream mcp server", w.serverID)
		if !sleepCtx(ctx, bo.Step()) {
			return
		}
	}
}

// get issues the standalone GET. the shared upstream http client injects
// auth headers and the TLS trust pool; ResponseHeaderTimeout bounds the
// header wait without capping the stream body.
func (w *notificationWatcher) get(ctx context.Context, lastEventID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	w.setSessionHeaders(req)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	return w.httpClient.Do(req)
}

func (w *notificationWatcher) setSessionHeaders(req *http.Request) {
	if w.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", w.sessionID)
	}
	if w.protocolVersion != "" {
		req.Header.Set("Mcp-Protocol-Version", w.protocolVersion)
	}
}

// consumeStream reads SSE frames until the stream ends or ctx is
// cancelled, returning the last event id seen for resumption against
// upstreams that support replay. only data and id fields matter here:
// event names are irrelevant to json-rpc payloads and server retry hints
// are ignored in favour of our own backoff.
func (w *notificationWatcher) consumeStream(ctx context.Context, body io.Reader, lastEventID string) string {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				w.dispatch(ctx, strings.Join(data, "\n"))
				data = data[:0]
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "id:"):
			lastEventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
	}
	return lastEventID
}

// jsonrpcFrame is the minimal shape needed to route a stream payload.
type jsonrpcFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

func (w *notificationWatcher) dispatch(ctx context.Context, payload string) {
	var frame jsonrpcFrame
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		w.logger.Debug("ignoring unparseable notification stream payload", "upstream mcp server", w.serverID, "error", err)
		return
	}
	switch frame.Method {
	case notificationToolsListChanged, notificationPromptsListChanged:
		w.logger.Debug("received upstream notification", "upstream mcp server", w.serverID, "method", frame.Method)
		w.notify(frame.Method)
	case "ping":
		if len(frame.ID) > 0 {
			w.respondPing(ctx, frame.ID)
		}
	case "":
		// a response to a request we never sent; nothing to do
	default:
		// other server-to-client requests (roots/list, sampling) never
		// worked over the broker's backend session and are out of scope
		w.logger.Debug("ignoring unsupported method on notification stream", "upstream mcp server", w.serverID, "method", frame.Method)
	}
}

// respondPing answers a server ping delivered on the stream so keepalive
// enabled upstreams do not conclude the broker session is dead. runs
// inline: pings are rare and a goroutine here would be a leak surface.
func (w *notificationWatcher) respondPing(ctx context.Context, id json.RawMessage) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	w.setSessionHeaders(req)
	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.logger.Debug("failed to answer upstream ping", "upstream mcp server", w.serverID, "error", err)
		return
	}
	discardBody(resp)
}

func isEventStream(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "text/event-stream"
}

func discardBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))
	_ = resp.Body.Close()
}

// sleepCtx waits for d or ctx, reporting whether the watch should continue.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
