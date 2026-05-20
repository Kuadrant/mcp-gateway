package mcprouter

import (
	"context"

	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HandleResponseHeaders handles response headers for session ID reverse mapping
func (s *ExtProcServer) HandleResponseHeaders(ctx context.Context, responseHeaders *eppb.HttpHeaders, requestHeaders *eppb.HttpHeaders, req *MCPRequest) ([]*eppb.ProcessingResponse, error) {
	response := NewResponse()
	responseHeaderBuilder := NewHeaders()
	s.Logger.DebugContext(ctx, "[EXT-PROC] HandleResponseHeaders response headers for session mapping...", "responseHeaders", responseHeaders)

	s.Logger.DebugContext(ctx, "[EXT-PROC] HandleResponseHeaders ", "mcp-session-id", getSingleValueHeader(responseHeaders.Headers, sessionHeader))
	//"gateway session id"
	gatewaySessionID := getSingleValueHeader(requestHeaders.Headers, sessionHeader)
	// we always want to respond with the original mcp-session-id to the client
	if gatewaySessionID != "" {
		responseHeaderBuilder.WithMCPSession(gatewaySessionID)
	}

	// on initialize responses, record whether the client declared elicitation support.
	// only store for direct client inits (no mcp-init-host), not hairpin backend inits.
	if req != nil && req.Method == "initialize" && req.clientSupportsElicitation() {
		initHost := getSingleValueHeader(requestHeaders.Headers, "mcp-init-host")
		if initHost == "" {
			if sid := getSingleValueHeader(responseHeaders.Headers, sessionHeader); sid != "" {
				if err := s.SessionCache.SetClientElicitation(ctx, sid); err != nil {
					s.Logger.ErrorContext(ctx, "failed to store client elicitation flag", "error", err)
				}
			}
		}
	}

	// intercept 404 from backend MCP Server as this means the clients mcp-session-id is invalid. We remove the session. The client can re-initialize with the gateway or they could re-invoke the tool as we will then lazily acquire a new session
	status := getSingleValueHeader(responseHeaders.Headers, ":status")

	if status == "404" && req != nil {
		s.Logger.InfoContext(ctx, "received 404 from backend MCP ", "method", req.Method, "server", req.serverName)
		if err := s.SessionCache.RemoveServerSession(ctx, req.GetSessionID(), req.serverName); err != nil {
			// not much we can do here log and continue
			s.Logger.ErrorContext(ctx, "failed to remove server session ", "server", req.serverName, "session", req.GetSessionID())
		}
	}

	// intercept 401 from backend: if the server uses URL token elicitation,
	// delete the cached user token so the next tool call triggers re-elicitation
	if status == "401" && req != nil && s.ElicitationEnabled {
		serverInfo, cfgErr := s.RoutingConfig.GetServerConfigByName(req.serverName)
		if cfgErr == nil && serverInfo.TokenURLElicitation != nil {
			span := trace.SpanFromContext(ctx)
			span.SetAttributes(
				attribute.String("http.upstream_status", "401"),
				attribute.String("upstream.server", req.serverName),
				attribute.Bool("token_elicitation.enabled", true),
				attribute.Bool("token_invalidation.attempted", true),
			)
			s.Logger.DebugContext(ctx, "received 401 from upstream, invalidating cached user token", "server", req.serverName)
			if err := s.SessionCache.DeleteUserToken(ctx, req.GetSessionID(), req.serverName); err != nil {
				span.SetAttributes(attribute.String("token_invalidation.error", err.Error()))
				s.Logger.ErrorContext(ctx, "failed to delete user token", "server", req.serverName, "session", req.GetSessionID(), "error", err)
			} else {
				span.SetAttributes(attribute.Bool("token_invalidation.succeeded", true))
			}
		}
	}

	responses := response.WithResponseHeaderResponse(responseHeaderBuilder.Build()).Build()

	// for tool calls where the client supports elicitation, switch response body
	// mode to STREAMED so the ext_proc receives each SSE chunk and can rewrite
	// elicitation request IDs.
	if req != nil && req.isToolCall() && req.clientElicitation && status == "200" && len(responses) > 0 {
		responses[0].ModeOverride = &extprochttp.ProcessingMode{
			RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
			ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
			RequestBodyMode:     extprochttp.ProcessingMode_STREAMED,
			ResponseBodyMode:    extprochttp.ProcessingMode_STREAMED,
			RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
			ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
		}
	}

	return responses, nil
}
