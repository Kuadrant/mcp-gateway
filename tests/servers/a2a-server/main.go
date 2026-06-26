// a2a-test-server is an A2A protocol test server used by e2e tests.
// It serves an Agent Card at the v0.3.0 well-known path (plus the legacy
// alias) and handles message/send, message/stream, tasks/get and
// tasks/cancel as JSON-RPC 2.0 over HTTP, with SSE streaming support.
//
// Behaviour is driven by the incoming message text:
//   - text containing "slow" starts the task in "working" state and
//     completes it after TASK_DURATION_MS
//   - text containing "fail" returns a task in "failed" state
//   - anything else returns a "completed" task immediately
//
// Every completed task carries an "echo" artifact with the message text
// and a "request-info" artifact with the received HTTP headers so tests
// can assert on what the server actually saw. Text containing "large" (or
// "image") additionally attaches a FilePart artifact with a deterministic
// base64 payload of ARTIFACT_BYTES bytes (default 1 MiB); on message/stream
// it is delivered as chunked artifact-update events (append/lastChunk) so the
// gateway's SSE passthrough is exercised on a heavy multi-modal payload it must
// forward without decoding.
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
	stateSubmitted = "submitted"
	stateWorking   = "working"
	stateCompleted = "completed"
	stateFailed    = "failed"
	stateCanceled  = "canceled"
)

// a2a protocol types, subset of the v0.3.0 spec

type agentCard struct {
	ProtocolVersion    string            `json:"protocolVersion"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	URL                string            `json:"url"`
	PreferredTransport string            `json:"preferredTransport"`
	Version            string            `json:"version"`
	Capabilities       agentCapabilities `json:"capabilities"`
	DefaultInputModes  []string          `json:"defaultInputModes"`
	DefaultOutputModes []string          `json:"defaultOutputModes"`
	Skills             []agentSkill      `json:"skills"`
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

type part struct {
	Kind string         `json:"kind"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	File *fileContent   `json:"file,omitempty"`
}

type fileContent struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"` // base64-encoded
}

type message struct {
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
	MessageID string `json:"messageId"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
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
	Kind      string     `json:"kind"`
}

type statusUpdateEvent struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Status    taskStatus `json:"status"`
	Final     bool       `json:"final"`
	Kind      string     `json:"kind"`
}

type artifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId"`
	Artifact  artifact `json:"artifact"`
	Append    bool     `json:"append"`
	LastChunk bool     `json:"lastChunk"`
	Kind      string   `json:"kind"`
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
	return agentCard{
		ProtocolVersion:    "0.3.0",
		Name:               envDefault("AGENT_NAME", "a2a-test-agent"),
		Description:        envDefault("AGENT_DESCRIPTION", "A2A test agent for e2e tests"),
		URL:                envDefault("AGENT_URL", "http://localhost:9090/a2a"),
		PreferredTransport: "JSONRPC",
		Version:            "1.0.0",
		Capabilities:       agentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
	}
}

func messageText(m message) string {
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Kind == "text" {
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

func (s *server) serveCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	log.Printf("a2a request method=%s id=%v", req.Method, req.ID)

	switch req.Method {
	case "message/send":
		s.handleSend(w, r, req)
	case "message/stream":
		s.handleStream(w, r, req)
	case "tasks/get":
		s.handleGet(w, req)
	case "tasks/cancel":
		s.handleCancel(w, req)
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
		if ok {
			params.Message.ContextID = t.ContextID
			t.History = append(t.History, params.Message)
		}
		s.mu.Unlock()
		if !ok {
			writeRPCError(w, req.ID, -32001, "task not found")
			return
		}
		writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: t})
		return
	}

	t := s.createTask(r, params.Message)
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: t})
}

func (s *server) createTask(r *http.Request, msg message) *task {
	text := messageText(msg)
	t := &task{
		ID:        newID("a2a-task-"),
		ContextID: newID("a2a-ctx-"),
		Status:    taskStatus{State: stateCompleted, Timestamp: now()},
		Kind:      "task",
	}
	msg.TaskID = t.ID
	msg.ContextID = t.ContextID
	t.History = []message{msg}

	switch {
	case strings.Contains(text, "fail"):
		t.Status.State = stateFailed
	case strings.Contains(text, "slow"):
		t.Status.State = stateWorking
		go s.completeLater(t.ID, text, r)
	default:
		t.Artifacts = s.buildArtifacts(t.ID, text, r)
	}

	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	return t
}

func (s *server) buildArtifacts(taskID, text string, r *http.Request) []artifact {
	info := headersData(r)
	info["taskId"] = taskID
	arts := []artifact{
		{
			ArtifactID: newID("a2a-artifact-"),
			Name:       "echo",
			Parts:      []part{{Kind: "text", Text: "echo: " + text}},
		},
		{
			ArtifactID: newID("a2a-artifact-"),
			Name:       "request-info",
			Parts:      []part{{Kind: "data", Data: info}},
		},
	}
	// heavy multi-modal payload as a single large FilePart, so the non-streaming
	// (buffered) rewrite path is exercised on a big base64 blob.
	if wantsFile(text) {
		arts = append(arts, fileArtifact(deterministicB64(fileArtifactBytes())))
	}
	return arts
}

func wantsFile(text string) bool {
	return strings.Contains(text, "large") || strings.Contains(text, "image")
}

// fileArtifactBytes is the decoded size of the FilePart payload, configurable
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
			Kind: "file",
			File: &fileContent{Name: "payload.bin", MimeType: "application/octet-stream", Bytes: b64},
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
		Kind:      "task",
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

	send := func(result any) {
		data, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
		if err != nil {
			log.Printf("failed to marshal sse event: %v", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// initial task event, then three working updates, then terminal state
	send(t)
	for i := 0; i < 3; i++ {
		time.Sleep(s.streamDelay)
		send(statusUpdateEvent{
			TaskID:    t.ID,
			ContextID: t.ContextID,
			Status:    taskStatus{State: stateWorking, Timestamp: now()},
			Kind:      "status-update",
		})
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

	// stream the heavy FilePart as chunked artifact-update events so the gateway
	// rewrites the task ID in each event envelope while the base64 passes through
	// untouched (chunks split mid-base64 on purpose: a decoder would choke, a
	// passthrough won't).
	if final == stateCompleted && wantsFile(text) {
		streamFileArtifact(send, t, deterministicB64(fileArtifactBytes()))
	}

	send(statusUpdateEvent{
		TaskID:    t.ID,
		ContextID: t.ContextID,
		Status:    taskStatus{State: final, Timestamp: now()},
		Final:     true,
		Kind:      "status-update",
	})
}

func streamFileArtifact(send func(any), t *task, b64 string) {
	const chunks = 3
	artID := newID("a2a-artifact-")
	size := (len(b64) + chunks - 1) / chunks
	for i := 0; i < chunks; i++ {
		start := i * size
		end := start + size
		if end > len(b64) {
			end = len(b64)
		}
		send(artifactUpdateEvent{
			TaskID:    t.ID,
			ContextID: t.ContextID,
			Artifact: artifact{
				ArtifactID: artID,
				Name:       "payload",
				Parts: []part{{
					Kind: "file",
					File: &fileContent{Name: "payload.bin", MimeType: "application/octet-stream", Bytes: b64[start:end]},
				}},
			},
			Append:    i > 0,
			LastChunk: i == chunks-1,
			Kind:      "artifact-update",
		})
	}
}

func (s *server) handleGet(w http.ResponseWriter, req rpcRequest) {
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
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: t})
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
	s.mu.Unlock()
	if !ok {
		writeRPCError(w, req.ID, -32001, "task not found")
		return
	}
	writeRPC(w, http.StatusOK, rpcResponse{ID: req.ID, Result: t})
}

func main() {
	srv := &server{
		card:        buildCard(),
		tasks:       map[string]*task{},
		taskDelay:   envMillis("TASK_DURATION_MS", 2000),
		streamDelay: envMillis("STREAM_DELAY_MS", 200),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", srv.serveCard)
	// legacy pre-v0.3.0 path, kept for backward compatibility
	mux.HandleFunc("/.well-known/agent.json", srv.serveCard)
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
