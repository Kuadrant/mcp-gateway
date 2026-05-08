package broker

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"

	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
)

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
	_, _ = w.Write([]byte(tokenFormHTML(entry.ServerName, elicitationID, entry.CSRFToken)))
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
	_, _ = w.Write([]byte(tokenSuccessHTML(entry.ServerName)))
}

func (h *TokenHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func tokenFormHTML(serverName, elicitationID, csrfToken string) string {
	name := html.EscapeString(serverName)
	id := html.EscapeString(elicitationID)
	csrf := html.EscapeString(csrfToken)
	return `<!DOCTYPE html>
<html><head><title>MCP Gateway - Token Required</title>
<style>body{font-family:system-ui,sans-serif;max-width:480px;margin:40px auto;padding:0 20px}
h1{font-size:1.4em}form{margin-top:20px}label{display:block;margin-bottom:6px;font-weight:600}
input[type=password]{width:100%;padding:8px;box-sizing:border-box;border:1px solid #ccc;border-radius:4px}
button{margin-top:12px;padding:8px 20px;background:#0066cc;color:#fff;border:none;border-radius:4px;cursor:pointer}
button:hover{background:#0052a3}</style></head>
<body><h1>Token Required</h1>
<p>The MCP server <strong>` + name + `</strong> requires a token to proceed.</p>
<form method="POST" action="/tokens">
<input type="hidden" name="elicitation_id" value="` + id + `">
<input type="hidden" name="csrf_token" value="` + csrf + `">
<label for="token">Token or API Key</label>
<input type="password" id="token" name="token" required placeholder="Enter your token or API key">
<button type="submit">Submit</button>
</form></body></html>`
}

func tokenSuccessHTML(serverName string) string {
	name := html.EscapeString(serverName)
	return `<!DOCTYPE html>
<html><head><title>MCP Gateway - Token Stored</title>
<style>body{font-family:system-ui,sans-serif;max-width:480px;margin:40px auto;padding:0 20px}
h1{font-size:1.4em;color:#2e7d32}</style></head>
<body><h1>Token Stored</h1>
<p>Your token for <strong>` + name + `</strong> has been stored. You can close this window and retry the tool call.</p>
</body></html>`
}
