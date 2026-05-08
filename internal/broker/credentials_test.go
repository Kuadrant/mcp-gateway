package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
)

type stubTokenCache struct {
	mu     sync.Mutex
	tokens map[string]string
}

func newStubTokenCache() *stubTokenCache {
	return &stubTokenCache{tokens: make(map[string]string)}
}

func (s *stubTokenCache) SetUserToken(_ context.Context, sessionID, serverName, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[sessionID+":"+serverName] = token
	return nil
}

func (s *stubTokenCache) GetUserToken(_ context.Context, sessionID, serverName string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[sessionID+":"+serverName]
	return t, ok
}

func buildBearerJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := fmt.Sprintf(`{"sub":"%s"}`, sub)
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return fmt.Sprintf("Bearer %s.%s.%s", header, payload, sig)
}

func setupHandler(t *testing.T) (*TokenHandler, elicitation.Map, *stubTokenCache) {
	t.Helper()
	eMap, err := elicitation.New()
	if err != nil {
		t.Fatal(err)
	}
	cache := newStubTokenCache()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	handler := NewTokenHandler(cache, eMap, *logger)
	return handler, eMap, cache
}

func TestTokenHandler_GET_RendersForm(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	req := httptest.NewRequest(http.MethodGet, "/tokens?elicitation_id="+id, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "github") {
		t.Fatal("expected server name in form")
	}
	if !strings.Contains(body, id) {
		t.Fatal("expected elicitation_id in form")
	}
	if !strings.Contains(body, "csrf_token") {
		t.Fatal("expected csrf_token hidden field in form")
	}
}

func TestTokenHandler_GET_MissingElicitationID(t *testing.T) {
	handler, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandler_GET_InvalidElicitationID(t *testing.T) {
	handler, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tokens?elicitation_id=bogus", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func getCSRF(t *testing.T, eMap elicitation.Map, id string) string {
	t.Helper()
	entry, ok, err := eMap.Lookup(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("failed to lookup CSRF token: ok=%v err=%v", ok, err)
	}
	return entry.CSRFToken
}

func TestTokenHandler_POST_StoresToken(t *testing.T) {
	handler, eMap, cache := setupHandler(t)
	ctx := context.Background()
	id, _ := eMap.Store(ctx, "sess1", "github", "")
	csrf := getCSRF(t, eMap, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}, "token": {"ghp_secret123"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	token, ok := cache.GetUserToken(ctx, "sess1", "github")
	if !ok || token != "ghp_secret123" {
		t.Fatalf("expected cached token, got ok=%v token=%q", ok, token)
	}

	// elicitation ID should be consumed (single-use)
	_, ok, _ = eMap.Lookup(ctx, id)
	if ok {
		t.Fatal("elicitation entry should have been removed after use")
	}
}

func TestTokenHandler_POST_InvalidElicitationID(t *testing.T) {
	handler, _, _ := setupHandler(t)

	form := url.Values{"elicitation_id": {"bogus"}, "token": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandler_POST_MissingToken(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	form := url.Values{"elicitation_id": {id}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandler_POST_SubMatch(t *testing.T) {
	handler, eMap, cache := setupHandler(t)
	ctx := context.Background()
	id, _ := eMap.Store(ctx, "sess1", "github", "user123")
	csrf := getCSRF(t, eMap, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", buildBearerJWT("user123"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	token, ok := cache.GetUserToken(ctx, "sess1", "github")
	if !ok || token != "ghp_secret" {
		t.Fatal("expected token stored after sub match")
	}
}

func TestTokenHandler_POST_SubMismatch(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "user123")
	csrf := getCSRF(t, eMap, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", buildBearerJWT("attacker"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestTokenHandler_POST_NoSubInEntry_SkipsCheck(t *testing.T) {
	handler, eMap, cache := setupHandler(t)
	ctx := context.Background()
	id, _ := eMap.Store(ctx, "sess1", "github", "")
	csrf := getCSRF(t, eMap, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	token, ok := cache.GetUserToken(ctx, "sess1", "github")
	if !ok || token != "ghp_secret" {
		t.Fatal("expected token stored when no sub in entry")
	}
}

func TestTokenHandler_POST_SubRequiredButNoAuthHeader(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "user123")
	csrf := getCSRF(t, eMap, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// no Authorization header
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTokenHandler_POST_MissingCSRFToken(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	form := url.Values{"elicitation_id": {id}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTokenHandler_POST_WrongCSRFToken(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	form := url.Values{"elicitation_id": {id}, "csrf_token": {"wrong-token"}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTokenHandler_MethodNotAllowed(t *testing.T) {
	handler, _, _ := setupHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/tokens", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestTokenHandler_POST_ErrorResponseIsJSON(t *testing.T) {
	handler, _, _ := setupHandler(t)

	form := url.Values{"elicitation_id": {"bogus"}, "token": {"secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("expected JSON error response, got: %s", w.Body.String())
	}
	if resp["error"] == "" {
		t.Fatal("expected non-empty error field")
	}
}
