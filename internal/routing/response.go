package routing

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ResponseDecision is the output of response-phase routing logic.
type ResponseDecision struct {
	SetHeaders map[string]string
	StreamBody bool // true if the adapter should switch to streamed response body mode
}

// ResponseInput is the transport-agnostic input for response-phase routing.
type ResponseInput struct {
	StatusCode        string
	GatewaySessionID  string
	ResponseSessionID string
	InitHost          string // mcp-init-host from request (empty for direct client inits)
	Request           *MCPRequest
}

// ResponseHandler processes response-phase routing decisions.
type ResponseHandler interface {
	HandleResponse(ctx context.Context, input *ResponseInput) *ResponseDecision
}

// ResponseHandler202511 handles response-phase logic for the 2025-11-25 protocol.
type ResponseHandler202511 struct {
	RoutingConfig      *atomic.Pointer[config.MCPServersConfig]
	SessionCache       SessionCache
	JWTManager         *session.JWTManager
	ElicitationEnabled bool
	Logger             *slog.Logger
}

// HandleResponse processes response phase routing decisions
func (h *ResponseHandler202511) HandleResponse(ctx context.Context, input *ResponseInput) *ResponseDecision {
	decision := &ResponseDecision{
		SetHeaders: make(map[string]string),
	}

	if input.GatewaySessionID != "" {
		decision.SetHeaders[SessionHeader] = input.GatewaySessionID
	}

	req := input.Request

	// on initialize responses, record whether the client declared elicitation support
	if req != nil && req.Method == "initialize" && req.ClientSupportsElicitation() {
		if input.InitHost == "" {
			if sid := input.ResponseSessionID; sid != "" {
				ttl := time.Duration(0)
				if h.JWTManager != nil {
					if expiresAt, jwtErr := h.JWTManager.GetExpiresIn(sid); jwtErr == nil {
						ttl = time.Until(expiresAt)
					}
				}
				if ttl <= 0 {
					h.Logger.ErrorContext(ctx, "skipping client elicitation flag: session TTL not positive", "sid", internaljwt.LogSafeSessionID(sid))
				} else if err := h.SessionCache.SetClientElicitation(ctx, sid, ttl); err != nil {
					h.Logger.ErrorContext(ctx, "failed to store client elicitation flag", "error", err)
				}
			}
		}
	}

	// intercept 404: backend session invalid, remove from cache
	if input.StatusCode == strconv.Itoa(http.StatusNotFound) && req != nil {
		h.Logger.InfoContext(ctx, "received 404 from backend MCP ", "method", req.Method, "server", req.ServerName)
		if err := h.SessionCache.RemoveServerSession(ctx, req.GetSessionID(), req.ServerName); err != nil {
			h.Logger.ErrorContext(ctx, "failed to remove server session", "server", req.ServerName, "session", internaljwt.LogSafeSessionID(req.GetSessionID()), "error", err)
		}
	}

	// intercept 401: invalidate cached user token for re-elicitation
	if input.StatusCode == strconv.Itoa(http.StatusUnauthorized) && req != nil && h.ElicitationEnabled {
		serverInfo, cfgErr := h.RoutingConfig.Load().GetServerConfigByName(req.ServerName)
		if cfgErr == nil && serverInfo.TokenURLElicitation != nil {
			span := trace.SpanFromContext(ctx)
			span.SetAttributes(
				attribute.String("http.upstream_status", "401"),
				attribute.String("upstream.server", req.ServerName),
				attribute.Bool("token_elicitation.enabled", true),
				attribute.Bool("token_invalidation.attempted", true),
			)
			h.Logger.DebugContext(ctx, "received 401 from upstream, invalidating cached user token", "server", req.ServerName)
			if err := h.SessionCache.DeleteUserToken(ctx, req.GetSessionID(), req.ServerName); err != nil {
				span.SetAttributes(attribute.String("token_invalidation.error", err.Error()))
				h.Logger.ErrorContext(ctx, "failed to delete user token", "server", req.ServerName, "session", internaljwt.LogSafeSessionID(req.GetSessionID()), "error", err)
			} else {
				span.SetAttributes(attribute.Bool("token_invalidation.succeeded", true))
			}
		}
	}

	// enable streamed response body mode for elicitation ID rewriting
	if req != nil && req.IsToolCall() && req.ClientElicitation && input.StatusCode == strconv.Itoa(http.StatusOK) {
		decision.StreamBody = true
	}

	return decision
}
