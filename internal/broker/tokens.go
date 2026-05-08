package broker

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
)

//go:embed templates/*.html
var templateFS embed.FS

var tokenTemplates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderTemplate("token_form.html", tokenFormData{
		ServerName:    entry.ServerName,
		ElicitationID: elicitationID,
		CSRFToken:     entry.CSRFToken,
	})))
}

func (h *TokenHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.sendError(w, http.StatusBadRequest, "invalid form data")
		return
	}

	elicitationID := r.FormValue("elicitation_id")
	csrfToken := r.FormValue("csrf_token")
	token := r.FormValue("token")

	if elicitationID == "" {
		h.sendError(w, http.StatusBadRequest, "missing elicitation_id")
		return
	}
	if token == "" {
		h.sendError(w, http.StatusBadRequest, "missing token")
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

	if csrfToken == "" || csrfToken != entry.CSRFToken {
		h.sendError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}

	if entry.Sub != "" {
		reqSub, err := elicitation.ExtractSubClaim(r.Header.Get("Authorization"))
		if err != nil {
			h.sendError(w, http.StatusForbidden, "authorization JWT missing sub claim")
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

func renderTemplate(name string, data any) string {
	var buf bytes.Buffer
	if err := tokenTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		return "internal error rendering page"
	}
	return buf.String()
}
