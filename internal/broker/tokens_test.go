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
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
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

func buildRawJWT(sub string) string {
	return strings.TrimPrefix(buildBearerJWT(sub), "Bearer ")
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

// getCSRFToken performs a GET to the token page and returns the csrf cookie value.
func getCSRFToken(t *testing.T, handler *TokenHandler, elicitationID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/tokens?elicitation_id="+elicitationID, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tokens failed: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "csrf" {
			return c.Value
		}
	}
	t.Fatal("no csrf cookie in GET response")
	return ""
}

// postTokenForm builds a POST request with CSRF cookie and form field.
func postTokenForm(elicitationID, token, csrf string) *http.Request {
	form := url.Values{"elicitation_id": {elicitationID}, "token": {token}, "csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	return req
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

func TestTokenHandler_GET_SetsCSRFCookie(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	req := httptest.NewRequest(http.MethodGet, "/tokens?elicitation_id="+id, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "csrf" {
			found = true
			if c.Path != "/tokens" {
				t.Fatalf("expected csrf cookie path /tokens, got %s", c.Path)
			}
			if !c.HttpOnly {
				t.Fatal("csrf cookie should be HttpOnly")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Fatal("csrf cookie should be SameSite=Strict")
			}
			if len(c.Value) < 32 {
				t.Fatal("csrf token too short")
			}
		}
	}
	if !found {
		t.Fatal("expected csrf cookie in response")
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

func TestTokenHandler_POST_StoresToken(t *testing.T) {
	handler, eMap, cache := setupHandler(t)
	ctx := context.Background()
	id, _ := eMap.Store(ctx, "sess1", "github", "")
	csrf := getCSRFToken(t, handler, id)

	req := postTokenForm(id, "ghp_secret123", csrf)
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

	req := postTokenForm("bogus", "secret", "some-csrf")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// CSRF fails first since the token won't match a real one, but bogus elicitation
	// would also fail. Either 403 (CSRF) or 400 (bad elicitation) is acceptable.
	if w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden {
		t.Fatalf("expected 400 or 403, got %d", w.Code)
	}
}

func TestTokenHandler_POST_MissingToken(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")
	csrf := getCSRFToken(t, handler, id)

	form := url.Values{"elicitation_id": {id}, "csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandler_POST_InvalidTokenCharacters(t *testing.T) {
	handler, eMap, _ := setupHandler(t)

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"newline", "ghp_abc\ndef"},
		{"carriage_return", "ghp_abc\rdef"},
		{"null_byte", "ghp_abc\x00def"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := eMap.Store(context.Background(), "sess1", "github", "")
			csrf := getCSRFToken(t, handler, id)
			req := postTokenForm(id, tc.token, csrf)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", w.Code)
			}
			if !strings.Contains(w.Body.String(), "invalid characters") {
				t.Fatalf("expected invalid characters error, got %s", w.Body.String())
			}
		})
	}
}

func TestTokenHandler_POST_SubMatch(t *testing.T) {
	handler, eMap, cache := setupHandler(t)
	ctx := context.Background()
	id, _ := eMap.Store(ctx, "sess1", "github", "user123")
	csrf := getCSRFToken(t, handler, id)

	req := postTokenForm(id, "ghp_secret", csrf)
	req.Header.Set(sharedheaders.VerifiedSubHeader, "user123")
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
	csrf := getCSRFToken(t, handler, id)

	req := postTokenForm(id, "ghp_secret", csrf)
	req.Header.Set(sharedheaders.VerifiedSubHeader, "attacker")
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
	csrf := getCSRFToken(t, handler, id)

	req := postTokenForm(id, "ghp_secret", csrf)
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

func TestTokenHandler_POST_SubRequiredButNoIdentity(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "user123")
	csrf := getCSRFToken(t, handler, id)

	req := postTokenForm(id, "ghp_secret", csrf)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestTokenHandler_POST_RawJWTNoLongerGrantsIdentity verifies that sending a
// raw Bearer JWT or jwt cookie does NOT satisfy the identity check — only the
// router-injected x-mcp-verified-sub header is trusted. This is the attack
// surface fixed by issue #1083.
func TestTokenHandler_POST_RawJWTNoLongerGrantsIdentity(t *testing.T) {
	for _, name := range []string{"bearer_only", "cookie_only"} {
		t.Run(name, func(t *testing.T) {
			handler, eMap, _ := setupHandler(t)
			id, _ := eMap.Store(context.Background(), "sess1", "github", "user123")
			csrf := getCSRFToken(t, handler, id)

			req := postTokenForm(id, "ghp_secret", csrf)
			switch name {
			case "bearer_only":
				req.Header.Set("Authorization", buildBearerJWT("user123"))
			case "cookie_only":
				req.AddCookie(&http.Cookie{Name: "jwt", Value: buildRawJWT("user123")})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			// without x-mcp-verified-sub the broker has no verified identity → 403
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d — raw JWT must not grant identity", w.Code)
			}
		})
	}
}

func TestTokenHandler_POST_CSRFMissing(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")

	form := url.Values{"elicitation_id": {id}, "token": {"ghp_secret"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// no csrf cookie or form field
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTokenHandler_POST_CSRFMismatch(t *testing.T) {
	handler, eMap, _ := setupHandler(t)
	id, _ := eMap.Store(context.Background(), "sess1", "github", "")
	csrf := getCSRFToken(t, handler, id)

	form := url.Values{"elicitation_id": {id}, "token": {"ghp_secret"}, "csrf_token": {"wrong-token"}}
	req := httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
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
	req.AddCookie(&http.Cookie{Name: "csrf", Value: "test"})
	form.Set("csrf_token", "test")
	// rebuild with csrf_token
	req = httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: "test"})
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
