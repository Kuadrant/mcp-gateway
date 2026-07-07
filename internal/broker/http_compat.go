package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TODO: remove this compatibility layer when we adopt streamable-HTTP
// protocol changes. it exists solely to keep the HTTP surface identical to
// mark3labs during the SDK migration so existing tests and clients are
// unaffected. once we cut over to the SDK's native error shapes and
// protocol version negotiation this file (and its tests) should be deleted.
//
// mark3labs answered every JSON-RPC-level failure (unknown method, bad
// version, unsupported capability) with HTTP 200 and a JSON-RPC error body,
// validated sessions before dispatch with plain-text 404s, ignored Accept
// and MCP-Protocol-Version headers, and 202'd stray notifications. the SDK
// handler turns most of those into HTTP 400s before the server sees them.
// this layer restores the old observable surface at the HTTP boundary; the
// SDK handler only ever sees requests it agrees with.

// MCPHandler returns the full /mcp HTTP handler: the SDK streamable handler
// wrapped with session resurrection and the mark3labs compatibility layer.
// JSONResponse matches mark3labs, which answered POSTs with application/json
// and delivered server-initiated notifications on the standalone GET stream.
func (m *mcpBrokerImpl) MCPHandler() http.Handler {
	opts := &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true, // behind envoy
		JSONResponse:               true,
	}
	if m.sessionValidator != nil {
		// cache hygiene: evict idle session table entries. invisible to
		// clients because a valid JWT resurrects the session on the next
		// request, so only set when resurrection is available.
		opts.SessionTimeout = SessionIdleTimeout
	}
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return m.MCPServer() },
		opts,
	)
	return &compatHandler{next: m.SessionResurrectionHandler(handler), broker: m}
}

// mark3labs error message and status constants, byte-for-byte.
const (
	msgInvalidContentType = "Invalid content type: must be 'application/json'"
	msgInvalidSessionID   = "Invalid session ID"
	msgSessionTerminated  = "Session terminated"
	msgBodyNotValidJSON   = "request body is not valid json"
	msgInvalidVersion     = "Invalid JSON-RPC version"

	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
)

// mainDefaultProtocolVersion is what mark3labs negotiated when the client
// sent an empty protocolVersion in initialize. deliberately the same value
// as defaultProtocolVersion (session_resurrection.go), which covers the
// header-absent case on resurrected sessions.
const mainDefaultProtocolVersion = "2025-03-26"

// maxRequestBodyBytes bounds POST body reads. in production the router's
// ext_proc bounds bodies before they reach the broker (--max-request-body-size,
// 5MB default); this guards direct broker access against unbounded allocation.
const maxRequestBodyBytes = 10 << 20

// unsupportedDomain maps methods mark3labs knew but the gateway never
// enabled to the capability name in its "<domain> not supported" error.
// the SDK would otherwise serve some of these (empty resource lists,
// logging level accepted) and reject others with HTTP 400.
var unsupportedDomain = map[string]string{
	"logging/setLevel":         "logging",
	"resources/list":           "resources",
	"resources/templates/list": "resources",
	"resources/read":           "resources",
	"resources/subscribe":      "resources",
	"resources/unsubscribe":    "resources",
	"completion/complete":      "completions",
	"tasks/get":                "tasks",
	"tasks/list":               "tasks",
	"tasks/result":             "tasks",
	"tasks/cancel":             "tasks",
}

// delegatedMethods are the request methods mark3labs dispatched and the
// gateway serves; everything else with an id got "Method %s not found".
var delegatedMethods = map[string]bool{
	"ping":         true,
	"tools/list":   true,
	"tools/call":   true,
	"prompts/list": true,
	"prompts/get":  true,
}

type compatHandler struct {
	next   http.Handler
	broker *mcpBrokerImpl
}

func (h *compatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// mark3labs never read MCP-Protocol-Version; the SDK rejects unknown
	// values and gates features on it. strip so its absence semantics
	// (assume 2025-03-26) apply throughout.
	r.Header.Del(protocolVersionHeader)

	switch r.Method {
	case http.MethodPost:
		h.servePOST(w, r)
	case http.MethodGet:
		h.serveGET(w, r)
	case http.MethodDelete:
		h.serveDELETE(w, r)
	default:
		// mark3labs fell through to http.NotFound; the SDK answers 405
		http.NotFound(w, r)
	}
}

// jsonrpcEnvelope is the minimal parse mark3labs did before dispatch.
type jsonrpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// idValue reproduces mark3labs' id handling: decoded as any (null becomes
// nil, numbers become float64) and re-marshalled in error responses.
func (e *jsonrpcEnvelope) idValue() any {
	if len(e.ID) == 0 {
		return nil
	}
	var v any
	_ = json.Unmarshal(e.ID, &v)
	return v
}

func (h *compatHandler) servePOST(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, msgInvalidContentType, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeMainJSONRPCError(w, http.StatusRequestEntityTooLarge, nil, codeInvalidRequest, "request body too large")
			return
		}
		writeMainJSONRPCError(w, http.StatusBadRequest, nil, codeParseError, fmt.Sprintf("read request body error: %v", err))
		return
	}

	var env jsonrpcEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		// also covers JSON-RPC batches: mark3labs could not unmarshal an
		// array into its envelope and answered every batch this way
		writeMainJSONRPCError(w, http.StatusBadRequest, nil, codeParseError, msgBodyNotValidJSON)
		return
	}

	// response-shaped bodies: empty results (ping responses included)
	// acknowledged before any session validation, exactly as mark3labs did
	if env.Method == "" && env.ID != nil && isJSONEmpty(env.Result) && isJSONEmpty(env.Error) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if env.Method == "" && env.ID != nil && (env.Result != nil || env.Error != nil) {
		h.serveSamplingResponse(w, r, &env)
		return
	}

	if env.Method == "initialize" {
		h.serveInitialize(w, r, body, &env)
		return
	}

	if code, msg := h.checkSession(r.Header.Get(gatewaySessionHeader)); code != 0 {
		h.dropSession(r)
		http.Error(w, msg, code)
		return
	}

	if env.JSONRPC != "2.0" {
		writeMainJSONRPCError(w, http.StatusOK, env.idValue(), codeInvalidRequest, msgInvalidVersion)
		return
	}

	// "id":null and absent id were both notifications to mark3labs
	if env.idValue() == nil {
		if p := bytes.TrimSpace(env.Params); len(p) > 0 && p[0] != '{' && !bytes.Equal(p, []byte("null")) {
			// mark3labs failed to re-parse non-object notification params
			writeMainJSONRPCError(w, http.StatusOK, nil, codeParseError, "Failed to parse notification")
			return
		}
		if sdkAcceptsNotification(env.Method, env.Params) {
			h.delegate(w, r, body, env.Method)
			return
		}
		// mark3labs silently dropped notifications it had no handler for;
		// a "result" body with a method also fell through to a 202
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if len(env.Result) > 0 && !bytes.Equal(bytes.TrimSpace(env.Result), []byte("null")) {
		// request-shaped body that also carries a result: mark3labs treated
		// it as a response to a server request and dropped it
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if domain, ok := unsupportedDomain[env.Method]; ok {
		writeMainJSONRPCError(w, http.StatusOK, env.idValue(), codeMethodNotFound, domain+" not supported")
		return
	}
	if delegatedMethods[env.Method] {
		h.delegate(w, r, body, env.Method)
		return
	}
	writeMainJSONRPCError(w, http.StatusOK, env.idValue(), codeMethodNotFound, fmt.Sprintf("Method %s not found", env.Method))
}

// serveInitialize always creates a fresh session (mark3labs generated a new
// ID even when the client sent one) and pins mark3labs' default protocol
// version when the client did not name one.
func (h *compatHandler) serveInitialize(w http.ResponseWriter, r *http.Request, body []byte, env *jsonrpcEnvelope) {
	r.Header.Del(gatewaySessionHeader)

	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(env.Params, &params)
	if params.ProtocolVersion == "" {
		body = rewriteInitializeVersion(body, env, mainDefaultProtocolVersion)
	}
	h.delegate(w, r, body, env.Method)
}

// rewriteInitializeVersion rebuilds an initialize body with the given
// protocol version so the SDK negotiates what mark3labs would have.
func rewriteInitializeVersion(body []byte, env *jsonrpcEnvelope, version string) []byte {
	var params map[string]json.RawMessage
	if len(env.Params) > 0 {
		_ = json.Unmarshal(env.Params, &params)
	}
	if params == nil {
		params = map[string]json.RawMessage{}
	}
	pv, err := json.Marshal(version)
	if err != nil {
		return body
	}
	params["protocolVersion"] = pv
	rewritten, err := json.Marshal(struct {
		JSONRPC string                     `json:"jsonrpc"`
		ID      json.RawMessage            `json:"id,omitempty"`
		Method  string                     `json:"method"`
		Params  map[string]json.RawMessage `json:"params"`
	}{env.JSONRPC, env.ID, env.Method, params})
	if err != nil {
		return body
	}
	return rewritten
}

// serveSamplingResponse mirrors mark3labs' handling of client-to-server
// response bodies. the gateway never has pending sampling requests, so
// after validation every well-formed one got the same 400.
func (h *compatHandler) serveSamplingResponse(w http.ResponseWriter, r *http.Request, env *jsonrpcEnvelope) {
	sid := r.Header.Get(gatewaySessionHeader)
	if sid == "" {
		http.Error(w, "Missing session ID for sampling response", http.StatusBadRequest)
		return
	}
	if code, msg := h.checkSession(sid); code != 0 {
		h.dropSession(r)
		http.Error(w, msg, code)
		return
	}
	var requestID int64
	if err := json.Unmarshal(env.ID, &requestID); err != nil {
		http.Error(w, "Invalid request ID in sampling response", http.StatusBadRequest)
		return
	}
	http.Error(w, "No pending sampling request found for the given request ID", http.StatusBadRequest)
}

func (h *compatHandler) serveGET(w http.ResponseWriter, r *http.Request) {
	// mark3labs never validated GET sessions: it opened a stream for any
	// caller. sessions that fail validation get an equivalent hanging
	// stream that carries nothing, keeping the surface indistinguishable
	// while never attaching an unauthenticated client to real state.
	if code, _ := h.checkSession(r.Header.Get(gatewaySessionHeader)); code != 0 {
		serveDeadStream(w, r)
		return
	}
	r.Header.Set("Accept", "text/event-stream")
	sw := &sseFilterWriter{rw: w}
	h.next.ServeHTTP(sw, r)
	sw.flushResidue()
}

// serveDeadStream replays mark3labs' GET response headers and hangs until
// the client goes away.
func serveDeadStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

// serveDELETE mirrors mark3labs: terminate via the session manager and
// answer 200 whether or not the session was known; the SDK's 204/404/400
// never surface. live SDK state for the ID is still torn down.
func (h *compatHandler) serveDELETE(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get(gatewaySessionHeader)
	if h.broker.sessionTerminator != nil {
		notAllowed, err := h.broker.sessionTerminator(sid)
		if err != nil {
			http.Error(w, fmt.Sprintf("Session termination failed: %v", err), http.StatusInternalServerError)
			return
		}
		if notAllowed {
			http.Error(w, "Session termination not allowed", http.StatusMethodNotAllowed)
			return
		}
	}
	if sid != "" {
		h.dropSession(r)
	}
	w.WriteHeader(http.StatusOK)
}

// checkSession applies mark3labs' pre-dispatch session gate. zero code
// means proceed. mapping mirrors its SessionIdManager contract: validator
// error 404s as an invalid ID, isInvalid 404s as terminated.
func (h *compatHandler) checkSession(sid string) (int, string) {
	if h.broker.sessionValidator == nil {
		return 0, ""
	}
	isInvalid, err := h.broker.sessionValidator(sid)
	if err != nil {
		return http.StatusNotFound, msgInvalidSessionID
	}
	if isInvalid {
		return http.StatusNotFound, msgSessionTerminated
	}
	return 0, ""
}

// dropSession closes any live SDK or resurrected session for the request's
// session ID by replaying it as a DELETE through the inner chain, so the
// pod-local table entry and any hanging GET stream are released.
func (h *compatHandler) dropSession(r *http.Request) {
	if r.Header.Get(gatewaySessionHeader) == "" {
		return
	}
	// never leaves the process: the inner chain dispatches on method and
	// session header only, so a fixed path avoids carrying request taint
	dr, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, "/", nil)
	if err != nil {
		return
	}
	dr.Header.Set(gatewaySessionHeader, r.Header.Get(gatewaySessionHeader))
	h.next.ServeHTTP(&discardResponseWriter{}, dr)
}

// delegate forwards a vetted POST to the SDK handler with the body restored
// and the Accept header normalised (mark3labs never required one), shimming
// the response back into mark3labs' framing.
func (h *compatHandler) delegate(w http.ResponseWriter, r *http.Request, body []byte, method string) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Accept", "application/json, text/event-stream")
	shim := &postResponseShim{rw: w, rewrite: h.resultRewriter(method)}
	h.next.ServeHTTP(shim, r)
	shim.finish()
}

// resultRewriter returns the response-body rewrite for a method, if any.
// only list results are rewritten; everything else streams as the SDK
// wrote it.
func (h *compatHandler) resultRewriter(method string) func([]byte) []byte {
	switch method {
	case "tools/list":
		return h.rewriteToolsList
	case "prompts/list":
		return rewritePromptsList
	default:
		return nil
	}
}

// listEnvelope is a JSON-RPC response envelope with the result held raw.
type listEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// rewriteListBody applies rewriteResult to the result inside a JSON-RPC
// response envelope and re-marshals it without the error member. any parse
// trouble (rewriteResult returning !ok included) returns the body untouched.
// the items field name lives in each rewriteResult's typed struct: struct
// marshalling is what pins mark3labs' member order and empty-list bytes.
func rewriteListBody(body []byte, rewriteResult func(json.RawMessage) ([]byte, bool)) []byte {
	var env listEnvelope
	if err := json.Unmarshal(body, &env); err != nil || len(env.Error) > 0 || len(env.Result) == 0 {
		return body
	}
	result, ok := rewriteResult(env.Result)
	if !ok {
		return body
	}
	out, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{env.JSONRPC, env.ID, result})
	if err != nil {
		return body
	}
	return out
}

// rewriteToolsList reshapes a tools/list response to mark3labs' result
// shape: the SDK's additive ttlMs/cacheScope fields are dropped and each
// tool's annotations are restored to the raw upstream bytes (mark3labs
// emitted the annotations key for every tool, {} when none were declared).
func (h *compatHandler) rewriteToolsList(body []byte) []byte {
	return rewriteListBody(body, func(result json.RawMessage) ([]byte, bool) {
		var res struct {
			Meta       json.RawMessage   `json:"_meta,omitempty"`
			NextCursor string            `json:"nextCursor,omitempty"`
			Tools      []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(result, &res); err != nil || res.Tools == nil {
			return nil, false
		}
		ups := h.broker.upstreamsSnapshot()
		for i, raw := range res.Tools {
			tool := map[string]json.RawMessage{}
			if err := json.Unmarshal(raw, &tool); err != nil {
				continue
			}
			var name string
			_ = json.Unmarshal(tool["name"], &name)
			if rawAnn := toolAnnotationsRaw(ups, name); rawAnn != nil {
				tool["annotations"] = rawAnn
			} else if _, ok := tool["annotations"]; !ok {
				tool["annotations"] = json.RawMessage(`{}`)
			}
			if rewritten, err := json.Marshal(tool); err == nil {
				res.Tools[i] = rewritten
			}
		}
		out, err := json.Marshal(res)
		return out, err == nil
	})
}

// toolAnnotationsRaw returns the exact upstream annotations bytes for a
// served tool name from the snapshot. nil when no upstream harvested
// annotations for it (broker-internal and user-specific tools included).
func toolAnnotationsRaw(ups []upstream.ActiveMCPServer, name string) json.RawMessage {
	for _, up := range ups {
		if h, ok := up.GetToolHints(name); ok {
			return h.Raw
		}
	}
	return nil
}

// rewritePromptsList drops the SDK's additive ttlMs/cacheScope fields from
// a prompts/list result.
func rewritePromptsList(body []byte) []byte {
	return rewriteListBody(body, func(result json.RawMessage) ([]byte, bool) {
		var res struct {
			Meta       json.RawMessage   `json:"_meta,omitempty"`
			NextCursor string            `json:"nextCursor,omitempty"`
			Prompts    []json.RawMessage `json:"prompts"`
		}
		if err := json.Unmarshal(result, &res); err != nil || res.Prompts == nil {
			return nil, false
		}
		out, err := json.Marshal(res)
		return out, err == nil
	})
}

// sdkAcceptsNotification reports whether the SDK transport can take the
// notification. everything else is acknowledged here: the SDK would turn
// it into an HTTP 400, mark3labs dropped it with a 202.
func sdkAcceptsNotification(method string, params json.RawMessage) bool {
	switch method {
	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return true
	case "notifications/progress":
		// the SDK rejects progress without params at the transport
		trimmed := bytes.TrimSpace(params)
		return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
	default:
		return false
	}
}

// mainJSONRPCError is the wire shape mark3labs produced for error
// responses, field order included.
type mainJSONRPCError struct {
	JSONRPC string               `json:"jsonrpc"`
	ID      any                  `json:"id"`
	Error   mainJSONRPCErrorBody `json:"error"`
}

type mainJSONRPCErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// writeMainJSONRPCError writes a JSON-RPC error exactly as mark3labs did:
// json.Encoder framing (trailing newline) with the given HTTP status.
func writeMainJSONRPCError(w http.ResponseWriter, status int, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mainJSONRPCError{
		JSONRPC: "2.0",
		ID:      id,
		Error:   mainJSONRPCErrorBody{Code: code, Message: message},
	})
}

// postResponseShim reframes SDK JSON responses as mark3labs wrote them:
// no Cache-Control header, json.Encoder trailing newline and an optional
// result rewrite. non-JSON responses stream through untouched.
type postResponseShim struct {
	rw          http.ResponseWriter
	rewrite     func([]byte) []byte
	buf         bytes.Buffer
	status      int
	wroteHeader bool
	buffering   bool
}

var _ http.Flusher = (*postResponseShim)(nil)

func (s *postResponseShim) Header() http.Header { return s.rw.Header() }

func (s *postResponseShim) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = status
	if mediaType, _, err := mime.ParseMediaType(s.rw.Header().Get("Content-Type")); err == nil && mediaType == "application/json" {
		s.buffering = true
		// mark3labs set no cache headers on JSON responses
		s.rw.Header().Del("Cache-Control")
		return
	}
	s.rw.WriteHeader(status)
}

func (s *postResponseShim) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	if s.buffering {
		return s.buf.Write(b)
	}
	return s.rw.Write(b)
}

func (s *postResponseShim) Flush() {
	if s.buffering {
		return
	}
	if f, ok := s.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// finish releases a buffered JSON response with mark3labs framing.
func (s *postResponseShim) finish() {
	if !s.buffering {
		return
	}
	body := s.buf.Bytes()
	if s.rewrite != nil {
		body = s.rewrite(body)
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	s.rw.WriteHeader(s.status)
	_, _ = s.rw.Write(body)
}

// sseFilterWriter reframes the SDK's standalone GET stream as mark3labs
// wrote it: the priming ": ok" comment is dropped (headers are flushed
// eagerly instead, as mark3labs did), notification frames with empty
// params lose the "params":{} member, and the Cache-Control value loses
// the SDK's no-transform. events are assembled across writes so filtering
// never splits a frame.
type sseFilterWriter struct {
	rw          http.ResponseWriter
	buf         bytes.Buffer
	wroteHeader bool
}

var _ http.Flusher = (*sseFilterWriter)(nil)

func (s *sseFilterWriter) Header() http.Header { return s.rw.Header() }

func (s *sseFilterWriter) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	h := s.rw.Header()
	if strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		h.Set("Cache-Control", "no-cache")
	}
	s.rw.WriteHeader(status)
	if f, ok := s.rw.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *sseFilterWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	s.buf.Write(b)
	for {
		raw := s.buf.Bytes()
		idx := bytes.Index(raw, []byte("\n\n"))
		if idx < 0 {
			break
		}
		event := make([]byte, idx+2)
		copy(event, raw[:idx+2])
		s.buf.Next(idx + 2)
		if out := filterSSEEvent(event); len(out) > 0 {
			if _, err := s.rw.Write(out); err != nil {
				return len(b), err
			}
		}
	}
	return len(b), nil
}

func (s *sseFilterWriter) Flush() {
	if f, ok := s.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// flushResidue forwards a trailing partial event once the stream ends.
func (s *sseFilterWriter) flushResidue() {
	if s.buf.Len() > 0 {
		_, _ = s.rw.Write(s.buf.Bytes())
		s.buf.Reset()
	}
}

// filterSSEEvent transforms one complete SSE event. nil drops the event.
func filterSSEEvent(event []byte) []byte {
	data, commentOnly := transport.SSEEventData(event)
	if commentOnly {
		// SDK priming comment (": ok"); mark3labs sent none
		return nil
	}
	if len(data) == 0 {
		return event
	}
	payload := bytes.Join(data, []byte("\n"))
	rewritten, ok := stripEmptyNotificationParams(payload)
	if !ok {
		return event
	}
	// rebuild with the shared frame shape (identical in both libraries)
	var out bytes.Buffer
	out.Grow(len(rewritten) + 32)
	out.WriteString("event: message\ndata: ")
	out.Write(rewritten)
	out.WriteString("\n\n")
	return out.Bytes()
}

// stripEmptyNotificationParams rewrites a notification payload carrying
// "params":{} to mark3labs' shape without the member. ok is false when the
// payload is not such a notification.
func stripEmptyNotificationParams(payload []byte) ([]byte, bool) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, false
	}
	if msg.ID != nil || msg.Method == "" || !bytes.Equal(bytes.TrimSpace(msg.Params), []byte("{}")) {
		return nil, false
	}
	out, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}{msg.JSONRPC, msg.Method})
	if err != nil {
		return nil, false
	}
	return out, true
}

// discardResponseWriter swallows internal replay responses.
type discardResponseWriter struct{ header http.Header }

func (d *discardResponseWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}
func (d *discardResponseWriter) WriteHeader(int)             {}
func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

// isJSONEmpty reports whether the JSON value is null, {}, [] or absent,
// mirroring mark3labs' empty-response detection.
func isJSONEmpty(data json.RawMessage) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return true
	}
	switch trimmed[0] {
	case '{':
		for i := 1; i < len(trimmed); i++ {
			if !unicode.IsSpace(rune(trimmed[i])) {
				return trimmed[i] == '}'
			}
		}
	case '[':
		for i := 1; i < len(trimmed); i++ {
			if !unicode.IsSpace(rune(trimmed[i])) {
				return trimmed[i] == ']'
			}
		}
	case 'n':
		return bytes.Equal(trimmed, []byte("null"))
	}
	return false
}
