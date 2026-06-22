package broker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
)

//go:embed templates/*.html
var templateFS embed.FS

var tokenTemplates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

const maxTokenLength = 2048

// tokenStore is the subset of UserTokenCache that the token page needs.
type tokenStore interface {
	SetUserToken(ctx context.Context, sessionID, serverName, token string) error
}

// TokenHandler handles HTTP requests to the /tokens endpoint
// for per-user token collection via URL elicitation.
type TokenHandler struct {
	tokenCache     tokenStore
	elicitationMap elicitation.Map
	logger         slog.Logger
}

// NewTokenHandler creates a handler for the /tokens endpoint.
func NewTokenHandler(tokenCache tokenStore, elicitationMap elicitation.Map, logger slog.Logger) *TokenHandler {
	return &TokenHandler{
		tokenCache:     tokenCache,
		elicitationMap: elicitationMap,
		logger:         logger,
	}
}

func (h *TokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		h.sendError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *TokenHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	elicitationID := r.URL.Query().Get("elicitation_id")
	if elicitationID == "" {
		h.sendError(w, http.StatusBadRequest, "missing elicitation_id parameter")
		return
	}

	entry, ok, err := h.elicitationMap.Lookup(r.Context(), elicitationID)
	if err != nil {
		h.logger.Error("elicitation lookup failed", "error", err)
		h.sendError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		h.sendError(w, http.StatusBadRequest, "invalid or expired elicitation_id")
		return
	}

	csrf, err := generateCSRFToken()
	if err != nil {
		h.logger.Error("failed to generate csrf token", "error", err)
		h.sendError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf",
		Value:    csrf,
		Path:     "/tokens",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   120,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderTemplate("token_form.html", tokenFormData{
		ServerName:    entry.ServerName,
		ElicitationID: elicitationID,
		CSRFToken:     csrf,
	})))
}

func (h *TokenHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid form data")
		return
	}

	csrfCookie, err := r.Cookie("csrf")
	if err != nil || csrfCookie.Value == "" {
		h.sendError(w, http.StatusForbidden, "missing CSRF token")
		return
	}

	elicitationID := r.FormValue("elicitation_id")
	token := r.FormValue("token")
	csrfForm := r.FormValue("csrf_token")

	if csrfForm == "" || subtle.ConstantTimeCompare([]byte(csrfForm), []byte(csrfCookie.Value)) != 1 {
		h.sendError(w, http.StatusForbidden, "CSRF validation failed")
		return
	}

	if elicitationID == "" {
		h.sendError(w, http.StatusBadRequest, "missing elicitation_id")
		return
	}
	if token == "" {
		h.sendError(w, http.StatusBadRequest, "missing token")
		return
	}
	if len(token) > maxTokenLength {
		h.sendError(w, http.StatusBadRequest, "token exceeds maximum length")
		return
	}
	if !isValidToken(token) {
		h.sendError(w, http.StatusBadRequest, "token contains invalid characters")
		return
	}

	ctx := r.Context()
	entry, ok, err := h.elicitationMap.Claim(ctx, elicitationID)
	if err != nil {
		h.logger.Error("elicitation claim failed", "error", err)
		h.sendError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		h.sendError(w, http.StatusBadRequest, "invalid or expired elicitation_id")
		return
	}

	if entry.Sub != "" {
		reqSub := extractRequestSub(r)
		if reqSub == "" {
			h.sendError(w, http.StatusForbidden, "no identity found in request")
			return
		}
		if reqSub != entry.Sub {
			h.logger.Warn("token page sub mismatch", "expected", entry.Sub, "got", reqSub)
			h.sendError(w, http.StatusForbidden, "identity mismatch")
			return
		}
	}

	if err := h.tokenCache.SetUserToken(ctx, entry.SessionID, entry.ServerName, token); err != nil {
		h.logger.Error("failed to store user token", "error", err)
		h.sendError(w, http.StatusInternalServerError, "failed to store token")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderTemplate("token_success.html", tokenSuccessData{ServerName: entry.ServerName})))
}

func (h *TokenHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

type tokenFormData struct {
	ServerName    string
	ElicitationID string
	CSRFToken     string
}

type tokenSuccessData struct {
	ServerName string
}

// isValidToken rejects tokens with characters that could enable header injection.
func isValidToken(token string) bool {
	for _, c := range token {
		if c == '\r' || c == '\n' || (c < 0x20 && c != '\t') {
			return false
		}
	}
	return true
}

func renderTemplate(name string, data any) string {
	var buf bytes.Buffer
	if err := tokenTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		return "internal error rendering page"
	}
	return buf.String()
}

// extractRequestSub returns the verified JWT sub injected by the router after
// AuthPolicy validation. Returns "" if the header is absent, which causes the
// caller to reject the request — ensuring identity binding is never based on a
// raw, unverified JWT decoded inside the broker.
func extractRequestSub(r *http.Request) string {
	return r.Header.Get(sharedheaders.VerifiedSubHeader)
}

func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
