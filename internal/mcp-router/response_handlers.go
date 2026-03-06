package mcprouter

import (
	"context"

	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
)

// HandleResponseHeaders handles response headers for session ID reverse mapping
func (s *ExtProcServer) HandleResponseHeaders(ctx context.Context, responseHeaders *eppb.HttpHeaders, requestHeaders *eppb.HttpHeaders, req *MCPRequest) ([]*eppb.ProcessingResponse, error) {
	response := NewResponse()
	responseHeaderBuilder := NewHeaders()
	s.Logger.DebugContext(ctx, "[EXT-PROC] HandleResponseHeaders response headers for session mapping...", "responseHeaders", responseHeaders)

	s.Logger.DebugContext(ctx, "[EXT-PROC] HandleResponseHeaders ", "mcp-session-id", getSingleValueHeader(responseHeaders.Headers, "mcp-sessionid"))
	//"gateway session id"
	gatewaySessionID := getSingleValueHeader(requestHeaders.Headers, sessionHeader)
	// we always want to respond with the original mcp-session-id to the client
	if gatewaySessionID != "" {
		responseHeaderBuilder.WithMCPSession(gatewaySessionID)
	}

	// on initialize responses, record whether the client declared elicitation support
	if req != nil && req.Method == "initialize" && req.clientSupportsElicitation() {
		if sid := getSingleValueHeader(responseHeaders.Headers, sessionHeader); sid != "" {
			s.clientElicitation.Store(sid, true)
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

	responses := response.WithResponseHeaderResponse(responseHeaderBuilder.Build()).Build()

	// for tool calls, switch response body mode to STREAMED so the ext_proc
	// receives each SSE chunk and can rewrite elicitation request IDs.
	if req != nil && req.isToolCall() && len(responses) > 0 {
		responses[0].ModeOverride = &extprochttp.ProcessingMode{
			RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
			ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
			RequestBodyMode:     extprochttp.ProcessingMode_BUFFERED,
			ResponseBodyMode:    extprochttp.ProcessingMode_STREAMED,
			RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
			ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
		}
	}

	return responses, nil
}
