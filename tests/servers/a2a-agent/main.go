// A minimal A2A agent fixture for design validation.
// Serves an agent card, accepts tasks, returns deterministic responses,
// and streams task status events. No external dependencies.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

var httpAddr = flag.String("http", "0.0.0.0:8090", "listen address")

// --- A2A protocol types ---

type AgentCard struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	URL               string          `json:"url"`
	Version           string          `json:"version"`
	Skills            []Skill         `json:"skills"`
	Capabilities      AgentCapability `json:"capabilities"`
	DefaultInputModes []string        `json:"defaultInputModes"`
	DefaultOutputModes []string       `json:"defaultOutputModes"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	InputModes  []string `json:"inputModes"`
	OutputModes []string `json:"outputModes"`
}

type AgentCapability struct {
	Streaming        bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

type Task struct {
	ID       string      `json:"id"`
	Status   TaskStatus  `json:"status"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

type TaskStatus struct {
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"`
	Message   *Message  `json:"message,omitempty"`
}

type Artifact struct {
	Name  string    `json:"name"`
	Parts []Part    `json:"parts"`
	Index int       `json:"index"`
}

type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Message struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type TaskSendParams struct {
	ID      string  `json:"id"`
	Message Message `json:"message"`
}

type TaskGetParams struct {
	ID string `json:"id"`
}

type TaskCancelParams struct {
	ID string `json:"id"`
}

// --- task store ---

type taskStore struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

func newTaskStore() *taskStore {
	return &taskStore{tasks: make(map[string]*Task)}
}

func (s *taskStore) get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

func (s *taskStore) set(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = t
}

func (s *taskStore) cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	t.Status = TaskStatus{State: "canceled", Timestamp: time.Now()}
	return true
}

// --- agent card ---

func agentCard(baseURL string) AgentCard {
	return AgentCard{
		Name:        "a2a-fixture-agent",
		Description: "local A2A fixture for design validation",
		URL:         baseURL,
		Version:     "0.1.0",
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Capabilities: AgentCapability{
			Streaming:              true,
			PushNotifications:      true,
			StateTransitionHistory: false,
		},
		Skills: []Skill{
			{
				ID:          "echo",
				Name:        "Echo",
				Description: "returns the input text unchanged",
				Tags:        []string{"read-only", "safe"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "reverse",
				Name:        "Reverse",
				Description: "returns the input text reversed",
				Tags:        []string{"read-only", "safe"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
}

// --- handlers ---

func handleAgentCard(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentCard(baseURL))
	}
}

func handleRPC(store *taskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, nil, -32700, "parse error")
			return
		}
		if req.JSONRPC != "2.0" {
			writeError(w, req.ID, -32600, "invalid request")
			return
		}

		switch req.Method {
		case "tasks/send":
			handleTaskSend(w, r, req, store)
		case "tasks/sendSubscribe":
			handleTaskSendSubscribe(w, r, req, store)
		case "tasks/get":
			handleTaskGet(w, req, store)
		case "tasks/cancel":
			handleTaskCancel(w, req, store)
		case "tasks/pushNotificationConfig/set":
			// stub: accept and acknowledge
			writeResult(w, req.ID, map[string]bool{"ok": true})
		default:
			writeError(w, req.ID, -32601, "method not found")
		}
	}
}

func handleTaskSend(w http.ResponseWriter, _ *http.Request, req JSONRPCRequest, store *taskStore) {
	var p TaskSendParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	output := processMessage(p.Message)
	t := &Task{
		ID:     p.ID,
		Status: TaskStatus{State: "completed", Timestamp: time.Now()},
		Artifacts: []Artifact{
			{
				Name:  "result",
				Index: 0,
				Parts: []Part{{Type: "text", Text: output}},
			},
		},
	}
	store.set(t)
	writeResult(w, req.ID, t)
}

func handleTaskSendSubscribe(w http.ResponseWriter, _ *http.Request, req JSONRPCRequest, store *taskStore) {
	var p TaskSendParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	output := processMessage(p.Message)

	events := []TaskStatus{
		{State: "submitted", Timestamp: time.Now()},
		{State: "working", Timestamp: time.Now(), Message: &Message{
			Role:  "agent",
			Parts: []Part{{Type: "text", Text: "processing..."}},
		}},
		{State: "completed", Timestamp: time.Now()},
	}

	for i, status := range events {
		update := map[string]any{
			"id":     p.ID,
			"status": status,
			"final":  i == len(events)-1,
		}
		if i == len(events)-1 {
			update["artifacts"] = []Artifact{
				{
					Name:  "result",
					Index: 0,
					Parts: []Part{{Type: "text", Text: output}},
				},
			}
		}

		data, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  update,
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		if i < len(events)-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	t := &Task{
		ID:     p.ID,
		Status: TaskStatus{State: "completed", Timestamp: time.Now()},
		Artifacts: []Artifact{
			{Name: "result", Index: 0, Parts: []Part{{Type: "text", Text: output}}},
		},
	}
	store.set(t)
}

func handleTaskGet(w http.ResponseWriter, req JSONRPCRequest, store *taskStore) {
	var p TaskGetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}
	t, ok := store.get(p.ID)
	if !ok {
		writeError(w, req.ID, -32001, "task not found")
		return
	}
	writeResult(w, req.ID, t)
}

func handleTaskCancel(w http.ResponseWriter, req JSONRPCRequest, store *taskStore) {
	var p TaskCancelParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}
	if !store.cancel(p.ID) {
		writeError(w, req.ID, -32001, "task not found")
		return
	}
	t, _ := store.get(p.ID)
	writeResult(w, req.ID, t)
}

func processMessage(msg Message) string {
	var parts []string
	for _, p := range msg.Parts {
		if p.Type == "text" {
			parts = append(parts, p.Text)
		}
	}
	text := strings.Join(parts, " ")

	// skill routing by first word or fallback to echo
	if strings.HasPrefix(strings.ToLower(text), "reverse ") {
		runes := []rune(strings.TrimPrefix(text, "reverse "))
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes)
	}
	return text
}

func writeResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
}

func main() {
	flag.Parse()

	baseURL := "http://" + *httpAddr
	store := newTaskStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent.json", handleAgentCard(baseURL))
	mux.HandleFunc("/", handleRPC(store))

	log.Printf("a2a-fixture-agent listening at %s", *httpAddr)
	log.Printf("agent card: %s/.well-known/agent.json", baseURL)

	srv := &http.Server{
		Addr:              *httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
