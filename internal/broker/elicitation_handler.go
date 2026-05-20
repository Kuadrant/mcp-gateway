package broker

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
)

const urlElicitationRequiredCode = -32042

// elicitationLookup is the read-only subset of elicitation.Map needed by ElicitationHandler.
type elicitationLookup interface {
	Lookup(ctx context.Context, elicitationID string) (elicitation.Entry, bool, error)
}

// serverConfigLookup is the read-only subset of config needed by ElicitationHandler.
type serverConfigLookup interface {
	GetServerConfigByName(serverName string) (*config.MCPServer, error)
	GetExternalHostname() string
}

// ElicitationHandler handles requests routed by the ext-proc router when
// a tool call requires URL-based token elicitation. It looks up the
// elicitation entry, builds the token page URL from server config, and
// returns the -32042 SSE error response per the MCP spec (2025-11-25).
type ElicitationHandler struct {
	ElicitationMap elicitationLookup
	Config         serverConfigLookup
}

func (h *ElicitationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	requestID := r.Header.Get(sharedheaders.ElicitationRequestID)
	elicitationID := r.Header.Get(sharedheaders.ElicitationID)
	sessionID := r.Header.Get("Mcp-Session-Id")

	if requestID == "" || elicitationID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	entry, ok, err := h.ElicitationMap.Lookup(r.Context(), elicitationID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	elicitURL := h.buildElicitationURL(entry.ServerName, elicitationID, r)

	var b strings.Builder
	b.WriteString("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":")
	b.WriteString(requestID)
	b.WriteString(",\"error\":{\"code\":")
	b.WriteString(strconv.Itoa(urlElicitationRequiredCode))
	b.WriteString(",\"message\":\"URL elicitation required\",\"data\":{\"elicitations\":[{\"mode\":\"url\",\"elicitationId\":")
	b.WriteString(strconv.Quote(elicitationID))
	b.WriteString(",\"url\":")
	b.WriteString(strconv.Quote(elicitURL))
	b.WriteString(",\"message\":\"Authorization is required to access this service.\"}]}}}\n\n")

	w.Header().Set("Content-Type", "text/event-stream")
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func (h *ElicitationHandler) buildElicitationURL(serverName, elicitationID string, r *http.Request) string {
	escapedID := url.QueryEscape(elicitationID)
	serverConfig, err := h.Config.GetServerConfigByName(serverName)
	if err == nil && serverConfig.TokenURLElicitation != nil && serverConfig.TokenURLElicitation.URL != "" {
		return serverConfig.TokenURLElicitation.URL + "?elicitation_id=" + escapedID
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + h.Config.GetExternalHostname() + "/tokens?elicitation_id=" + escapedID
}
