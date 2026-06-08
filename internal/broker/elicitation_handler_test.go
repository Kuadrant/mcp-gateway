package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
)

type stubElicitationLookup struct {
	entries   map[string]elicitation.Entry
	lookupErr error
}

func (s *stubElicitationLookup) Lookup(_ context.Context, id string) (elicitation.Entry, bool, error) {
	if s.lookupErr != nil {
		return elicitation.Entry{}, false, s.lookupErr
	}
	e, ok := s.entries[id]
	return e, ok, nil
}

type stubServerConfig struct {
	servers          map[string]*config.MCPServer
	externalHostname string
}

func (s *stubServerConfig) GetServerConfigByName(name string) (*config.MCPServer, error) {
	srv, ok := s.servers[name]
	if !ok {
		return nil, fmt.Errorf("unknown server")
	}
	return srv, nil
}

func (s *stubServerConfig) GetExternalHostname() string {
	return s.externalHostname
}

func setupElicitationHandler(entry elicitation.Entry, elicitationID string, serverCfg *config.MCPServer, externalHostname string) *ElicitationHandler {
	lookup := &stubElicitationLookup{entries: map[string]elicitation.Entry{elicitationID: entry}}
	servers := map[string]*config.MCPServer{}
	if serverCfg != nil {
		servers[entry.ServerName] = serverCfg
	}
	return &ElicitationHandler{
		ElicitationMap: lookup,
		Config:         &stubServerConfig{servers: servers, externalHostname: externalHostname},
	}
}

func TestElicitationHandler_MissingHeaders(t *testing.T) {
	handler := &ElicitationHandler{
		ElicitationMap: &stubElicitationLookup{},
		Config:         &stubServerConfig{},
	}

	for _, tc := range []struct {
		name      string
		requestID string
		elicitID  string
	}{
		{"both_missing", "", ""},
		{"missing_request_id", "", "eid-123"},
		{"missing_elicitation_id", "42", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
			if tc.requestID != "" {
				req.Header.Set(sharedheaders.ElicitationRequestID, tc.requestID)
			}
			if tc.elicitID != "" {
				req.Header.Set(sharedheaders.ElicitationID, tc.elicitID)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", w.Code)
			}
		})
	}
}

func TestElicitationHandler_InvalidElicitationID(t *testing.T) {
	handler := &ElicitationHandler{
		ElicitationMap: &stubElicitationLookup{entries: map[string]elicitation.Entry{}},
		Config:         &stubServerConfig{},
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
	req.Header.Set(sharedheaders.ElicitationRequestID, "42")
	req.Header.Set(sharedheaders.ElicitationID, "nonexistent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestElicitationHandler_LookupError(t *testing.T) {
	handler := &ElicitationHandler{
		ElicitationMap: &stubElicitationLookup{lookupErr: fmt.Errorf("redis down")},
		Config:         &stubServerConfig{},
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
	req.Header.Set(sharedheaders.ElicitationRequestID, "42")
	req.Header.Set(sharedheaders.ElicitationID, "eid-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestElicitationHandler_ExternalURLConfigured(t *testing.T) {
	eid := "eid-abc"
	handler := setupElicitationHandler(
		elicitation.Entry{SessionID: "sess1", ServerName: "github"},
		eid,
		&config.MCPServer{
			Name:                "github",
			TokenURLElicitation: &config.TokenURLElicitationConfig{URL: "https://custom.example.com/auth"},
		},
		"gateway.example.com",
	)

	req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
	req.Header.Set(sharedheaders.ElicitationRequestID, "42")
	req.Header.Set(sharedheaders.ElicitationID, eid)
	req.Header.Set("Mcp-Session-Id", "session-xyz")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	if sid := w.Header().Get("Mcp-Session-Id"); sid != "session-xyz" {
		t.Fatalf("expected session-xyz, got %q", sid)
	}

	body := w.Body.String()
	assertSSEContains(t, body, "https://custom.example.com/auth?elicitation_id="+eid)
	assertSSEJSON(t, body, 42, -32042)
}

func TestElicitationHandler_XForwardedProto(t *testing.T) {
	eid := "eid-def"
	handler := setupElicitationHandler(
		elicitation.Entry{SessionID: "sess1", ServerName: "github"},
		eid,
		&config.MCPServer{Name: "github"},
		"gateway.example.com",
	)

	req := httptest.NewRequest(http.MethodPost, "http://gateway.example.com/mcp/elicitation", nil)
	req.Header.Set(sharedheaders.ElicitationRequestID, "7")
	req.Header.Set(sharedheaders.ElicitationID, eid)
	req.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	assertSSEContains(t, body, "http://gateway.example.com/tokens?elicitation_id="+eid)
}

func TestElicitationHandler_FallbackHostname(t *testing.T) {
	eid := "eid-ghi"
	handler := setupElicitationHandler(
		elicitation.Entry{SessionID: "sess1", ServerName: "github"},
		eid,
		&config.MCPServer{Name: "github"},
		"fallback.example.com",
	)

	req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
	req.Host = ""
	req.Header.Set(sharedheaders.ElicitationRequestID, "99")
	req.Header.Set(sharedheaders.ElicitationID, eid)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	assertSSEContains(t, body, "https://fallback.example.com/tokens?elicitation_id="+eid)
}

func TestElicitationHandler_NoSessionHeader(t *testing.T) {
	eid := "eid-nosess"
	handler := setupElicitationHandler(
		elicitation.Entry{SessionID: "sess1", ServerName: "github"},
		eid,
		&config.MCPServer{Name: "github"},
		"gw.example.com",
	)

	req := httptest.NewRequest(http.MethodPost, "/mcp/elicitation", nil)
	req.Header.Set(sharedheaders.ElicitationRequestID, "1")
	req.Header.Set(sharedheaders.ElicitationID, eid)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if sid := w.Header().Get("Mcp-Session-Id"); sid != "" {
		t.Fatalf("expected no Mcp-Session-Id header, got %q", sid)
	}
}

// assertSSEContains checks that the SSE body contains the expected URL.
func assertSSEContains(t *testing.T, body, expected string) {
	t.Helper()
	if !strings.Contains(body, expected) {
		t.Fatalf("expected body to contain %q, got:\n%s", expected, body)
	}
}

// assertSSEJSON parses the JSON-RPC payload from the SSE data line and validates id and error code.
func assertSSEJSON(t *testing.T, body string, expectedID, expectedCode int) {
	t.Helper()
	dataPrefix := "data: "
	var dataLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, dataPrefix) {
			dataLine = strings.TrimPrefix(line, dataPrefix)
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("no data line found in SSE body:\n%s", body)
	}

	var msg struct {
		ID    int `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(dataLine), &msg); err != nil {
		t.Fatalf("failed to parse SSE data as JSON: %v\ndata: %s", err, dataLine)
	}
	if msg.ID != expectedID {
		t.Fatalf("expected id=%d, got %d", expectedID, msg.ID)
	}
	if msg.Error.Code != expectedCode {
		t.Fatalf("expected error code=%d, got %d", expectedCode, msg.Error.Code)
	}
}
