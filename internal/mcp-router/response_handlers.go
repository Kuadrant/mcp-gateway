package mcprouter

import (
	"context"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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

	// When Envoy returns 504 because it enforced our per-request rq_timeout (see
	// x-envoy-upstream-rq-timeout-ms), replace the response with a JSON-RPC error.
	// Do not rewrite arbitrary 504s from the upstream app or other timeouts.
	if status == "504" && req != nil && req.isToolCall() && req.toolCallTimeoutMS > 0 &&
		envoyMarkedUpstreamRequestTimeout(responseHeaders.Headers) {
		_, span := tracer().Start(ctx, "mcp-router.tool-call.timeout",
			trace.WithAttributes(
				attribute.String("mcp.server", req.serverName),
				attribute.String("gen_ai.tool.name", req.ToolName()),
				attribute.Int64("mcp.tool.timeout_ms", req.toolCallTimeoutMS),
			),
		)
		span.SetStatus(codes.Error, "tool call timed out")
		span.SetAttributes(attribute.String("error.type", "tool_call_timeout"))
		span.End()
		s.Logger.InfoContext(ctx, "tool call exceeded gateway timeout policy",
			"server", req.serverName,
			"tool", req.ToolName(),
			"timeout_ms", req.toolCallTimeoutMS,
			"session", req.GetSessionID(),
		)
		setHeaders := []*corev3.HeaderValueOption{
			{Header: &corev3.HeaderValue{Key: sessionHeader, RawValue: []byte(gatewaySessionID)}},
			{Header: &corev3.HeaderValue{Key: "content-type", RawValue: []byte("text/event-stream")}},
		}
		body := buildToolTimeoutSSEEvent(req.ID, req.ToolName(), req.toolCallTimeoutMS)
		// Returning HTTP 200 with a JSON-RPC error mirrors how HandleToolCall reports
		// other application-level failures (see "Tool not found" path) and lets
		// existing MCP clients parse the error without special status-code handling.
		return response.WithImmediateJSONRPCResponse(200, setHeaders, string(body)).Build(), nil
	}

	responses := response.WithResponseHeaderResponse(responseHeaderBuilder.Build()).Build()

	// for tool calls where the client supports elicitation, switch response body
	// mode to STREAMED so the ext_proc receives each SSE chunk and can rewrite
	// elicitation request IDs.
	if req != nil && req.isToolCall() && req.clientElicitation && len(responses) > 0 {
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
