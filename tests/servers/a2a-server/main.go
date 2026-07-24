// a2a-test-server is an A2A protocol test server used by e2e tests.
// It serves an Agent Card at the well-known path and handles the v1.0
// JSON-RPC methods SendMessage, SendStreamingMessage, GetTask, CancelTask
// and SubscribeToTask as JSON-RPC 2.0 over HTTP, with SSE streaming support.
//
// The wire format follows the A2A v1.0 JSON-RPC binding (ProtoJSON): enum
// values are SCREAMING_SNAKE (TASK_STATE_*, ROLE_*), Parts are a flat oneof
// (text / raw / data) with no kind discriminator, streaming results are wrapped
// in a StreamResponse oneof (task / statusUpdate / artifactUpdate) and a
// non-streaming SendMessage result is wrapped in a SendMessageResponse oneof
// (task / message), while GetTask and CancelTask return a bare Task.
//
// Behaviour is driven by the incoming message text:
//   - text containing "slow" starts the task in TASK_STATE_WORKING and
//     completes it after TASK_DURATION_MS
//   - text containing "fail" returns a task in TASK_STATE_FAILED
//   - anything else returns a TASK_STATE_COMPLETED task immediately
//
// Every completed task carries an "echo" artifact with the message text
// and a "request-info" artifact with the received HTTP headers so tests
// can assert on what the server actually saw. Text containing "large" (or
// "image") additionally attaches a file artifact with a deterministic
// base64 payload of ARTIFACT_BYTES bytes (default 1 MiB); on streaming
// it is delivered as chunked artifactUpdate events (append/lastChunk) so the
// gateway's SSE passthrough is exercised on a heavy multi-modal payload it must
// forward without decoding.
//
// AUTH_MODE makes the card auth-aware and enforces it: "apikey" declares an
// apiKey scheme and requires X-API-Key (API_KEY) on the card fetch and /a2a;
// "bearer" declares an oauth2 scheme and requires Authorization: Bearer on /a2a;
// "none" (default) leaves the agent open.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stateSubmitted = "TASK_STATE_SUBMITTED"
	stateWorking   = "TASK_STATE_WORKING"
	stateCompleted = "TASK_STATE_COMPLETED"
	stateFailed    = "TASK_STATE_FAILED"
	stateCanceled  = "TASK_STATE_CANCELED"
)

// a2a protocol types, subset of the v1.0 spec (JSON-RPC binding / ProtoJSON)

type agentCard struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description"`
	SupportedInterfaces  []agentInterface          `json:"supportedInterfaces"`
	Version              string                    `json:"version"`
	Capabilities         agentCapabilities         `json:"capabilities"`
	DefaultInputModes    []string                  `json:"defaultInputModes"`
	DefaultOutputModes   []string                  `json:"defaultOutputModes"`
	Skills               []agentSkill              `json:"skills"`
	SecuritySchemes      map[string]securityScheme `json:"securitySchemes,omitempty"`
	SecurityRequirements []securityRequirement     `json:"securityRequirements,omitempty"`
}

// agentInterface declares a target URL, transport and protocol version; v1.0
// moved the endpoint off the top-level card `url` into this repeated field.
type agentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion"`
}

type agentCapabilities struct {
	Streaming bool `json:"streaming"`
}

type agentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// securityScheme is the v1.0 wrapped oneof; only one member is set.
type securityScheme struct {
	APIKey *apiKeyScheme `json:"apiKeySecurityScheme,omitempty"`
	OAuth2 *oauth2Scheme `json:"oauth2SecurityScheme,omitempty"`
}

type apiKeyScheme struct {
	Location string `json:"location"` // "header" | "query" | "cookie"
	Name     string `json:"name"`
}

type oauth2Scheme struct {
	Flows oauthFlows `json:"flows"`
}

type oauthFlows struct {
	ClientCredentials *clientCredentialsFlow `json:"clientCredentials,omitempty"`
}

type clientCredentialsFlow struct {
	TokenURL string            `json:"tokenUrl,omitempty"`
	Scopes   map[string]string `json:"scopes,omitempty"`
}

// securityRequirement maps a scheme name to its required scopes (StringList).
type securityRequirement struct {
	Schemes map[string]stringList `json:"schemes"`
}

type stringList struct {
	List []string `json:"list"`
}

// part is the v1.0 flat oneof: exactly one of text/raw/data is set, with the
// file metadata (filename/mediaType) alongside raw. There is no kind field.
type part struct {
	Text      string         `json:"text,omitempty"`
	Raw       string         `json:"raw,omitempty"` // base64-encoded file bytes
	Data      map[string]any `json:"data,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
}

type message struct {
	MessageID string `json:"messageId"`
	ContextID string `json:"contextId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
}

type taskStatus struct {
	State     string `json:"state"`
	Timestamp string `json:"timestamp,omitempty"`
}

type artifact struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name,omitempty"`
	Parts      []part `json:"parts"`
}

type task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    taskStatus `json:"status"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	History   []message  `json:"history,omitempty"`
	createdAt time.Time  `json:"-"`
}

// v1.0 dropped the kind discriminator and the status-update `final` flag; the
// stream terminates when the status carries a terminal TaskState.
type statusUpdateEvent struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Status    taskStatus `json:"status"`
}

type artifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId"`
	Artifact  artifact `json:"artifact"`
	Append    bool     `json:"append"`
	LastChunk bool     `json:"lastChunk"`
}

// sendResult is the SendMessageResponse oneof (non-streaming SendMessage).
type sendResult struct {
	Task    *task    `json:"task,omitempty"`
	Message *message `json:"message,omitempty"`
}

// streamResult is the StreamResponse oneof (SendStreamingMessage / SubscribeToTask).
type streamResult struct {
	Task           *task                `json:"task,omitempty"`
	StatusUpdate   *statusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *artifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

// json-rpc envelope

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type messageSendParams struct {
	Message message `json:"message"`
}

type taskIDParams struct {
	ID string `json:"id"`
}

type server struct {
	card        agentCard
	mu          sync.Mutex
	tasks       map[string]*task
	taskDelay   time.Duration
	streamDelay time.Duration
	taskTTL     time.Duration
}

// sweepLoop periodically drops terminal tasks older than taskTTL so the in-memory
// store stays bounded under sustained load (e.g. stress-testing the gateway). Only
// terminal tasks are removed, so an in-flight task is never deleted mid-test.
func (s *server) sweepLoop() {
	interval := s.taskTTL
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.sweep()
	}
}

func (s *server) sweep() {
	cutoff := time.Now().Add(-s.taskTTL)
	s.mu.Lock()
	for id, t := range s.tasks {
		if isTerminal(t.Status.State) && t.createdAt.Before(cutoff) {
			delete(s.tasks, id)
		}
	}
	s.mu.Unlock()
}

// snapshot returns a copy of the task taken under the lock, so a handler can
// encode it without racing concurrent mutation (e.g. completeLater).
func (s *server) snapshot(t *task) task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *t
}

func newID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return prefix + hex.EncodeToString(b)
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envMillis(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}

func buildCard() agentCard {
	prefix := os.Getenv("AGENT_PREFIX")
	var skills []agentSkill
	for _, id := range strings.Split(envDefault("SKILLS", "forecast,alerts"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		skills = append(skills, agentSkill{
			ID:          prefix + id,
			Name:        id,
			Description: "test skill " + id,
			Tags:        []string{"test"},
		})
	}
	card := agentCard{
		Name:        envDefault("AGENT_NAME", "a2a-test-agent"),
		Description: envDefault("AGENT_DESCRIPTION", "A2A test agent for e2e tests"),
		SupportedInterfaces: []agentInterface{{
			URL:             envDefault("AGENT_URL", "http://localhost:9090/a2a"),
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: "1.0",
		}},
		Version:            "1.0.0",
		Capabilities:       agentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
	}
	// AUTH_MODE makes the card self-describing; the same mode is enforced at the
	// endpoints, so a card that declares auth actually rejects unauthenticated
	// requests (otherwise the gateway's auth brokering would go untested).
	switch authMode() {
	case "apikey":
		card.SecuritySchemes = map[string]securityScheme{
			"apiKey": {APIKey: &apiKeyScheme{Location: "header", Name: "X-API-Key"}},
		}
		card.SecurityRequirements = []securityRequirement{
			{Schemes: map[string]stringList{"apiKey": {List: []string{}}}},
		}
	case "bearer", "oauth2":
		card.SecuritySchemes = map[string]securityScheme{
			"oauth2": {OAuth2: &oauth2Scheme{Flows: oauthFlows{ClientCredentials: &clientCredentialsFlow{
				TokenURL: envDefault("OAUTH_TOKEN_URL", "https://issuer.example.com/token"),
				Scopes:   map[string]string{"a2a": "invoke A2A tasks"},
			}}}},
		}
		card.SecurityRequirements = []securityRequirement{
			{Schemes: map[string]stringList{"oauth2": {List: []string{"a2a"}}}},
		}
	}
	return card
}

func messageText(m message) string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Text != "" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func headersData(r *http.Request) map[string]any {
	headers := map[string]any{}
	for k, v := range r.Header {
		headers[strings.ToLower(k)] = strings.Join(v, ",")
	}
	return map[string]any{"headers": headers}
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func authMode() string { return strings.ToLower(envDefault("AUTH_MODE", "none")) }

func apiKey() string { return envDefault("API_KEY", "test-api-key") }

// authorized enforces transport-level auth on /a2a per AUTH_MODE.
func authorized(r *http.Request) bool {
	switch authMode() {
	case "apikey":
		return r.Header.Get("X-API-Key") == apiKey()
	case "bearer", "oauth2":
		return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	default:
		return true
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	scheme := "Bearer"
	if authMode() == "apikey" {
		scheme = "ApiKey"
	}
	w.Header().Set("WWW-Authenticate", scheme)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (s *server) serveCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// apikey agents protect the card fetch too, so the broker must present its
	// credentialRef to discover the card (the only place credentialRef is testable).
	if authMode() == "apikey" && r.Header.Get("X-API-Key") != apiKey() {
		writeUnauthorized(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.card); err != nil {
		log.Printf("failed to encode agent card: %v", err)
	}
}

func writeRPC(w http.ResponseWriter, status int, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string) {
	writeRPC(w, http.StatusOK, rpcResponse{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *server) handleA2A(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorized(r) {
		writeUnauthorized(w)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	log.Printf("a2a request method=%s id=%v", req.Method, req.ID)

	switch req.Method {
	case "SendMessage":
		s.handleSend(w, r, req)
	case "SendStreamingMessage":
		s.handleStream(w, r, req)
	case "GetTask":
		s.handleGet(w, req)
	case "CancelTask":
		s.handleCancel(w, req)
	case "SubscribeToTask":
		s.handleResubscribe(w, req)
	default:
		writeRPCError(w, req.ID, -32601, "method not found")
	}
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params messageSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params.Message.Parts) == 0 {
		writeRPCError(w, req.ID, -32602, "invalid params: message with parts required")
		return
	}

	// multi-turn: continue an existing task
	if params.Message.TaskID != "" {
		s.mu.Lock()
		t, ok := s.tasks[params.Message.TaskID]
		var snap task
		if ok {
			params.Message.ContextID = t.ContextID
			t.History = append(t.History, params.Message)
			snap = *t
		}
		s.mu.Unlock()
		if !ok {
			writeRPCError(w, req.ID, -32001, "task not found")
			return
		}
		writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: sendResult{Task: &snap}})
		return
	}

	t := s.createTask(r, params.Message)
	snap := s.snapshot(t)
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: sendResult{Task: &snap}})
}

func (s *server) createTask(r *http.Request, msg message) *task {
	text := messageText(msg)
	t := &task{
		ID:        newID("a2a-task-"),
		ContextID: newID("a2a-ctx-"),
		Status:    taskStatus{State: stateCompleted, Timestamp: now()},
		createdAt: time.Now(),
	}
	msg.TaskID = t.ID
	msg.ContextID = t.ContextID
	t.History = []message{msg}

	switch {
	case strings.Contains(text, "fail"):
		t.Status.State = stateFailed
	case strings.Contains(text, "slow"):
		t.Status.State = stateWorking
	default:
		t.Artifacts = s.buildArtifacts(t.ID, text, r)
	}

	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	// spawn background completion only after the task is in the map, so the
	// goroutine's lookup can't race ahead of the insert and drop the task.
	if t.Status.State == stateWorking {
		go s.completeLater(t.ID, text, r)
	}
	return t
}

func (s *server) buildArtifacts(taskID, text string, r *http.Request) []artifact {
	info := headersData(r)
	info["taskId"] = taskID
	arts := []artifact{
		{
			ArtifactID: newID("a2a-artifact-"),
			Name:       "echo",
			Parts:      []part{{Text: "echo: " + text}},
		},
		{
			ArtifactID: newID("a2a-artifact-"),
			Name:       "request-info",
			Parts:      []part{{Data: info}},
		},
	}
	// heavy multi-modal payload as a single large file part, so the non-streaming
	// (buffered) rewrite path is exercised on a big base64 blob.
	if wantsFile(text) {
		arts = append(arts, fileArtifact(deterministicB64(fileArtifactBytes())))
	}
	return arts
}

func wantsFile(text string) bool {
	return strings.Contains(text, "large") || strings.Contains(text, "image")
}

// fileArtifactBytes is the decoded size of the file part payload, configurable
// via ARTIFACT_BYTES (default 1 MiB). Size it past a naive buffer limit so a
// passthrough that streams survives where one that buffers/decodes would not.
func fileArtifactBytes() int {
	if v := os.Getenv("ARTIFACT_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1 << 20
}

// deterministicB64 returns the base64 of n deterministic bytes, so an e2e test
// can regenerate the exact payload and assert the gateway forwarded it
// byte-for-byte without decoding it.
func deterministicB64(n int) string {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = byte(i % 251) // 251 is prime, avoids byte-alignment patterns
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func fileArtifact(b64 string) artifact {
	return artifact{
		ArtifactID: newID("a2a-artifact-"),
		Name:       "payload",
		Parts: []part{{
			Raw:       b64,
			Filename:  "payload.bin",
			MediaType: "application/octet-stream",
		}},
	}
}

func (s *server) completeLater(taskID, text string, r *http.Request) {
	artifacts := s.buildArtifacts(taskID, text, r)
	time.Sleep(s.taskDelay)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok || t.Status.State != stateWorking {
		return
	}
	t.Status = taskStatus{State: stateCompleted, Timestamp: now()}
	t.Artifacts = artifacts
}

func (s *server) handleStream(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params messageSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params.Message.Parts) == 0 {
		writeRPCError(w, req.ID, -32602, "invalid params: message with parts required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPCError(w, req.ID, -32603, "streaming unsupported")
		return
	}

	text := messageText(params.Message)
	t := &task{
		ID:        newID("a2a-task-"),
		ContextID: newID("a2a-ctx-"),
		Status:    taskStatus{State: stateSubmitted, Timestamp: now()},
		createdAt: time.Now(),
	}
	params.Message.TaskID = t.ID
	params.Message.ContextID = t.ContextID
	t.History = []message{params.Message}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(result streamResult) {
		data, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		if err != nil {
			log.Printf("failed to marshal sse event: %v", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// initial task event, a raw SSE keepalive comment, then three working
	// updates and a terminal state. the keepalive (a non-data: line) must pass
	// through the gateway's data:-only rewriter untouched without being parsed
	// as JSON.
	snap := s.snapshot(t)
	send(streamResult{Task: &snap})
	fmt.Fprint(w, ": ping\n\n")
	flusher.Flush()
	for i := 0; i < 3; i++ {
		time.Sleep(s.streamDelay)
		send(streamResult{StatusUpdate: &statusUpdateEvent{
			TaskID:    t.ID,
			ContextID: t.ContextID,
			Status:    taskStatus{State: stateWorking, Timestamp: now()},
		}})
	}
	time.Sleep(s.streamDelay)

	final := stateCompleted
	if strings.Contains(text, "fail") {
		final = stateFailed
	}
	s.mu.Lock()
	t.Status = taskStatus{State: final, Timestamp: now()}
	if final == stateCompleted {
		t.Artifacts = s.buildArtifacts(t.ID, text, r)
	}
	s.mu.Unlock()

	// stream the heavy file part as chunked artifactUpdate events so the gateway
	// rewrites the task ID in each event envelope while the base64 passes through
	// untouched (chunks split mid-base64 on purpose: a decoder would choke, a
	// passthrough won't).
	if final == stateCompleted && wantsFile(text) {
		streamFileArtifact(send, t, deterministicB64(fileArtifactBytes()))
	}

	// terminal event: v1.0 has no `final` flag, the terminal TaskState is the
	// signal, after which the server closes the stream.
	send(streamResult{StatusUpdate: &statusUpdateEvent{
		TaskID:    t.ID,
		ContextID: t.ContextID,
		Status:    taskStatus{State: final, Timestamp: now()},
	}})
}

func streamFileArtifact(send func(streamResult), t *task, b64 string) {
	const chunks = 3
	artID := newID("a2a-artifact-")
	size := (len(b64) + chunks - 1) / chunks
	for i := 0; i < chunks; i++ {
		start := i * size
		end := start + size
		if end > len(b64) {
			end = len(b64)
		}
		send(streamResult{ArtifactUpdate: &artifactUpdateEvent{
			TaskID:    t.ID,
			ContextID: t.ContextID,
			Artifact: artifact{
				ArtifactID: artID,
				Name:       "payload",
				Parts: []part{{
					Raw:       b64[start:end],
					Filename:  "payload.bin",
					MediaType: "application/octet-stream",
				}},
			},
			Append:    i > 0,
			LastChunk: i == chunks-1,
		}})
	}
}

func (s *server) handleResubscribe(w http.ResponseWriter, req rpcRequest) {
	var params taskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		writeRPCError(w, req.ID, -32602, "invalid params: id required")
		return
	}
	s.mu.Lock()
	t, ok := s.tasks[params.ID]
	s.mu.Unlock()
	if !ok {
		writeRPCError(w, req.ID, -32001, "task not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPCError(w, req.ID, -32603, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(result streamResult) {
		data, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		if err != nil {
			log.Printf("failed to marshal sse event: %v", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// reconnect: SubscribeToTask MUST return the Task as the first event, then
	// observe the task to its terminal state (driven by completeLater) and replay
	// the final event. even an already-terminal task gets an SSE terminal event.
	// resubscribe only observes — it never mutates the task — so it cannot suppress
	// the artifacts completeLater attaches; the poll is bounded so it cannot hang.
	snap := s.snapshot(t)
	send(streamResult{Task: &snap})
	var state string
	for i := 0; i < 30; i++ {
		s.mu.Lock()
		state = t.Status.State
		s.mu.Unlock()
		if isTerminal(state) {
			break
		}
		time.Sleep(s.streamDelay)
		send(streamResult{StatusUpdate: &statusUpdateEvent{
			TaskID:    t.ID,
			ContextID: t.ContextID,
			Status:    taskStatus{State: stateWorking, Timestamp: now()},
		}})
	}
	s.mu.Lock()
	state = t.Status.State
	s.mu.Unlock()
	send(streamResult{StatusUpdate: &statusUpdateEvent{
		TaskID:    t.ID,
		ContextID: t.ContextID,
		Status:    taskStatus{State: state, Timestamp: now()},
	}})
}

func (s *server) handleGet(w http.ResponseWriter, req rpcRequest) {
	var params taskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		writeRPCError(w, req.ID, -32602, "invalid params: id required")
		return
	}
	s.mu.Lock()
	t, ok := s.tasks[params.ID]
	var snap task
	if ok {
		snap = *t
	}
	s.mu.Unlock()
	if !ok {
		writeRPCError(w, req.ID, -32001, "task not found")
		return
	}
	// GetTask returns a bare Task (not wrapped in a oneof).
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: snap})
}

func isTerminal(state string) bool {
	switch state {
	case stateCompleted, stateFailed, stateCanceled:
		return true
	}
	return false
}

func (s *server) handleCancel(w http.ResponseWriter, req rpcRequest) {
	var params taskIDParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		writeRPCError(w, req.ID, -32602, "invalid params: id required")
		return
	}
	s.mu.Lock()
	t, ok := s.tasks[params.ID]
	if ok && !isTerminal(t.Status.State) {
		t.Status = taskStatus{State: stateCanceled, Timestamp: now()}
	} else if ok {
		s.mu.Unlock()
		writeRPCError(w, req.ID, -32002, "task cannot be canceled")
		return
	}
	var snap task
	if ok {
		snap = *t
	}
	s.mu.Unlock()
	if !ok {
		writeRPCError(w, req.ID, -32001, "task not found")
		return
	}
	// CancelTask returns a bare Task (not wrapped in a oneof).
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: snap})
}

func main() {
	srv := &server{
		card:        buildCard(),
		tasks:       map[string]*task{},
		taskDelay:   envMillis("TASK_DURATION_MS", 2000),
		streamDelay: envMillis("STREAM_DELAY_MS", 200),
		taskTTL:     envMillis("TASK_TTL_MS", 600000),
	}
	go srv.sweepLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", srv.serveCard)
	mux.HandleFunc("/a2a", srv.handleA2A)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	port := envDefault("PORT", "9090")
	log.Printf("a2a test server %q listening on :%s with %d skills", srv.card.Name, port, len(srv.card.Skills))
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
