package broker

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gateway session JWTs are validated statelessly, so a session must stay
// usable across broker restarts and replicas. the SDK's streamable handler
// keeps a pod-local session table and 404s IDs it has never seen. this
// wrapper intercepts that 404: when the ID is a valid gateway JWT it
// connects a fresh server session with the same ID and synthesised
// initialize state (mirroring the SDK's stateless mode), then serves the
// request through it. invalid or expired JWTs still 404.

// protocolVersionHeader is the spec's MCP-Protocol-Version header in the
// canonical form http.Header.Get expects.
const protocolVersionHeader = "Mcp-Protocol-Version"

// defaultProtocolVersion is assumed when the client omits the
// MCP-Protocol-Version header, matching the SDK's default. deliberately the
// same value as mainDefaultProtocolVersion (http_compat.go), which covers
// the empty-initialize-version case.
const defaultProtocolVersion = "2025-03-26"

// SessionIdleTimeout is how long a session table entry may go without an
// HTTP request before it is closed. cache hygiene, not session expiry:
// the JWT stays the sole validity semantic, and the next request with a
// valid JWT resurrects the session transparently. without it the SDK's
// pod-local table (and the broker's per-session state) accumulates
// abandoned sessions until JWT expiry; mark3labs kept no table at all.
// set as the SDK handler's SessionTimeout and mirrored for wrapper-owned
// resurrected sessions.
const SessionIdleTimeout = 30 * time.Minute

// sessionNotFoundBody is the exact body the SDK handler writes when a
// session ID is missing from its pod-local table. the transport's other
// 404s ("session is closing", json method-not-found) must pass through:
// their request bodies are already consumed, so a replay could not be
// served anyway.
const sessionNotFoundBody = "session not found\n"

// SessionResurrectionHandler wraps the streamable HTTP handler so valid
// gateway session JWTs unknown to this pod are re-established instead of
// 404ing. returns next unchanged when no session validator is configured.
func (m *mcpBrokerImpl) SessionResurrectionHandler(next http.Handler) http.Handler {
	if m.sessionValidator == nil {
		return next
	}
	return &sessionResurrectionHandler{next: next, broker: m, idleTimeout: SessionIdleTimeout}
}

type resurrectedSession struct {
	transport *mcp.StreamableServerTransport
	session   *mcp.ServerSession

	// idle timer mirroring the SDK handler's sessionInfo: paused while
	// POSTs are in flight, re-armed when the last one ends. hanging GET
	// streams do not hold it, matching the SDK (clients keep sessions
	// alive with pings; an evicted stream resurrects on reconnect).
	timerMu sync.Mutex
	refs    int
	timer   *time.Timer
}

// startRequest pauses the idle timer for an in-flight request.
func (e *resurrectedSession) startRequest() {
	e.timerMu.Lock()
	defer e.timerMu.Unlock()
	if e.timer == nil {
		return // no idle timeout, or timer stopped permanently
	}
	if e.refs == 0 {
		e.timer.Stop()
	}
	e.refs++
}

// endRequest re-arms the idle timer once no requests are in flight.
func (e *resurrectedSession) endRequest(timeout time.Duration) {
	e.timerMu.Lock()
	defer e.timerMu.Unlock()
	if e.timer == nil {
		return
	}
	e.refs--
	if e.refs == 0 {
		e.timer.Reset(timeout)
	}
}

// stopTimer stops the idle timer permanently on session end.
func (e *resurrectedSession) stopTimer() {
	e.timerMu.Lock()
	defer e.timerMu.Unlock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

type sessionResurrectionHandler struct {
	next   http.Handler
	broker *mcpBrokerImpl
	// live holds sessions this wrapper resurrected, keyed by gateway
	// session ID. hits are served directly: the SDK handler would 404 them.
	live sync.Map
	// mu serialises resurrection so concurrent requests for the same
	// unknown ID yield one session. only taken on the rare miss path.
	mu sync.Mutex
	// idleTimeout closes resurrected sessions after inactivity, mirroring
	// the SDK handler's SessionTimeout for its own table.
	idleTimeout time.Duration
}

func (h *sessionResurrectionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodDelete:
	default:
		h.next.ServeHTTP(w, r)
		return
	}
	sid := r.Header.Get(gatewaySessionHeader)
	if sid == "" {
		h.next.ServeHTTP(w, r)
		return
	}
	if e, ok := h.live.Load(sid); ok {
		h.serve(w, r, e.(*resurrectedSession))
		return
	}
	sw := &sniff404Writer{rw: w}
	h.next.ServeHTTP(sw, r)
	if !sw.sessionNotFound() {
		sw.flushBuffered()
		return
	}
	if r.Method == http.MethodDelete {
		// nothing to resurrect and nothing to tear down: connecting a
		// session solely to close it is wasted work, and the compat DELETE
		// path has already run the terminator and discards this response
		sw.flushBuffered()
		return
	}
	isInvalid, err := h.broker.sessionValidator(sid)
	if err != nil || isInvalid {
		sw.flushBuffered()
		return
	}
	e, err := h.resurrect(r, sid)
	if err != nil {
		h.broker.logger.Error("session resurrection failed",
			"sessionID", internaljwt.LogSafeSessionID(sid), "error", err)
		http.Error(w, "failed connection", http.StatusInternalServerError)
		return
	}
	h.serve(w, r, e)
}

// serve dispatches a request to a resurrected session. content-type,
// accept and protocol-version negotiation are the SDK handler's; the
// transport tolerates their absence, so they are not replicated here.
func (h *sessionResurrectionHandler) serve(w http.ResponseWriter, r *http.Request, e *resurrectedSession) {
	if r.Method == http.MethodDelete {
		// mirrors the SDK handler's DELETE: close then 204. the Wait
		// goroutine wired in resurrect releases per-session state.
		_ = e.session.Close()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPost {
		e.startRequest()
		defer e.endRequest(h.idleTimeout)
	}
	e.transport.ServeHTTP(w, r)
}

func (h *sessionResurrectionHandler) resurrect(r *http.Request, sid string) (*resurrectedSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.live.Load(sid); ok {
		// lost the race; a concurrent request already resurrected it
		return e.(*resurrectedSession), nil
	}
	pv := r.Header.Get(protocolVersionHeader)
	if pv == "" {
		pv = defaultProtocolVersion
	}
	// synthesise pre-initialised state the way the SDK's stateless mode
	// does, so the init gate accepts mid-session requests. the SDK's
	// protocol-version context key is unexported, so transport-internal
	// version checks on resurrected sessions see none and fall back to
	// 2025-03-26 semantics.
	state := &mcp.ServerSessionState{
		InitializeParams:  &mcp.InitializeParams{ProtocolVersion: pv},
		InitializedParams: &mcp.InitializedParams{},
		LogLevel:          "info",
	}
	transport := &mcp.StreamableServerTransport{SessionID: sid}
	// Server.Connect cannot wire the transport's unexported toolLookup, so
	// the SDK's tools/call param-header validation (>= 2026-07-28 clients
	// only) is skipped for resurrected sessions.
	ss, err := h.broker.MCPServer().Connect(r.Context(), transport, &mcp.ServerSessionOptions{State: state})
	if err != nil {
		return nil, err
	}
	e := &resurrectedSession{transport: transport, session: ss}
	if h.idleTimeout > 0 {
		e.timer = time.AfterFunc(h.idleTimeout, func() { _ = ss.Close() })
	}
	h.live.Store(sid, e)
	// resurrected sessions never send notifications/initialized, so wire
	// the session-end cleanup InitializedHandler would normally register.
	// Wait returns on any close (DELETE, jwt invalidation, idle timeout).
	go func() {
		_ = ss.Wait()
		e.stopTimer()
		h.live.CompareAndDelete(sid, e)
		h.broker.onGatewaySessionEnd(sid)
	}()
	h.broker.logger.Debug("gateway client session resurrected",
		"gatewaySessionID", internaljwt.LogSafeSessionID(sid))
	return e, nil
}

// sniff404Writer defers a potential session-not-found 404 so the request
// can be replayed on a resurrected session. any other response commits on
// first write and streams through untouched.
type sniff404Writer struct {
	rw       http.ResponseWriter
	header   http.Header
	buf      bytes.Buffer
	status   int
	passthru bool
	buffered bool
}

var _ http.Flusher = (*sniff404Writer)(nil)

func (s *sniff404Writer) Header() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *sniff404Writer) WriteHeader(status int) {
	if s.passthru || s.buffered {
		return
	}
	if status == http.StatusNotFound {
		s.status = status
		s.buffered = true
		return
	}
	s.commit(status)
}

func (s *sniff404Writer) Write(b []byte) (int, error) {
	if s.buffered {
		return s.buf.Write(b)
	}
	if !s.passthru {
		s.commit(http.StatusOK)
	}
	return s.rw.Write(b)
}

// Flush implements http.Flusher; the SDK flushes each SSE event. buffered
// 404s never stream, so flushing only applies once committed.
func (s *sniff404Writer) Flush() {
	if !s.passthru {
		return
	}
	if f, ok := s.rw.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *sniff404Writer) commit(status int) {
	dst := s.rw.Header()
	for k, v := range s.header {
		dst[k] = v
	}
	s.rw.WriteHeader(status)
	s.passthru = true
}

// sessionNotFound reports whether the response was the SDK handler's
// pod-local table miss, written before the request body is read.
func (s *sniff404Writer) sessionNotFound() bool {
	return s.buffered && s.buf.String() == sessionNotFoundBody
}

// flushBuffered releases a withheld 404 that turned out not to be
// resurrectable. no-op when the response already streamed through.
func (s *sniff404Writer) flushBuffered() {
	if !s.buffered {
		return
	}
	s.commit(s.status)
	_, _ = s.rw.Write(s.buf.Bytes())
}
