package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// HandleResourceRead routes a resources/read request to the upstream that owns
// the federated URI. The handler is structurally analogous to HandleToolCall —
// session validation, backend lookup, hairpinned initialize, header injection,
// body rewrite — but keys the lookup off params.uri instead of params.name and
// strips the "<prefix>+" scheme marker before forwarding the request upstream.
//
// See docs/design/resource-federation.md for the URI namespacing scheme.
func (s *ExtProcServer) HandleResourceRead(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	federatedURI := mcpReq.ResourceURI()

	ctx, span := tracer().Start(ctx, "mcp-router.resource-read")
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("mcp.resource.uri", federatedURI),
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		)
	}

	calculatedResponse := NewResponse()
	if federatedURI == "" {
		s.Logger.ErrorContext(ctx, "[EXT-PROC] HandleRequestBody no uri set in resources/read")
		span.SetStatus(codes.Error, "no resource uri set")
		if span.IsRecording() {
			span.SetAttributes(attribute.String("error.type", "missing_resource_uri"))
		}
		calculatedResponse.WithImmediateResponse(400, "no resource uri set")
		return calculatedResponse.Build()
	}
	if sessionErr := s.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		s.Logger.ErrorContext(ctx, "session validation failed", "session", mcpReq.GetSessionID(), "error", sessionErr)
		span.RecordError(sessionErr)
		span.SetStatus(codes.Error, sessionErr.Error())
		if span.IsRecording() {
			span.SetAttributes(attribute.String("error.type", "invalid_session"))
		}
		calculatedResponse.WithImmediateResponse(sessionErr.Code(), sessionErr.Error())
		return calculatedResponse.Build()
	}

	headers := NewHeaders()
	var serverInfo *config.MCPServer
	{
		_, infoSpan := tracer().Start(ctx, "mcp-router.broker.get-server-info-by-resource")
		if infoSpan.IsRecording() {
			infoSpan.SetAttributes(attribute.String("mcp.resource.uri", federatedURI))
		}
		var infoErr error
		serverInfo, infoErr = s.Broker.GetServerInfoByResourceURI(federatedURI)
		if infoErr != nil {
			infoSpan.RecordError(infoErr)
			infoSpan.SetStatus(codes.Error, "resource not found")
		}
		infoSpan.End()
		if infoErr != nil {
			s.Logger.DebugContext(ctx, "no server for resource", "uri", federatedURI)
			span.RecordError(infoErr)
			span.SetStatus(codes.Error, "resource not found")
			if span.IsRecording() {
				span.SetAttributes(attribute.String("error.type", "resource_not_found"))
			}
			sseBody := buildResourceNotFoundSSEBody(mcpReq.ID)
			calculatedResponse.WithImmediateJSONRPCResponse(200,
				[]*corev3.HeaderValueOption{
					{
						Header: &corev3.HeaderValue{
							Key:      "mcp-session-id",
							RawValue: []byte(mcpReq.GetSessionID()),
						},
					},
				},
				sseBody)
			return calculatedResponse.Build()
		}
	}

	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("mcp.server", serverInfo.Name),
			attribute.String("mcp.server.hostname", serverInfo.Hostname),
		)
	}

	// strip "<prefix>+" from the URI scheme. CutPrefix on "<prefix>+" works
	// because prefixedURI in the broker (see internal/broker/upstream/manager.go)
	// only mutates the scheme portion of the URI, leaving the rest untouched.
	upstreamURI := federatedURI
	if serverInfo.ToolPrefix != "" {
		marker := serverInfo.ToolPrefix + "+"
		if colon := strings.Index(federatedURI, ":"); colon > 0 {
			scheme := federatedURI[:colon]
			if rest, ok := strings.CutPrefix(scheme, marker); ok {
				upstreamURI = rest + federatedURI[colon:]
			}
		}
	}
	mcpReq.ReWriteResourceURI(upstreamURI)

	headers.WithMCPMethod(mcpReq.Method)
	headers.WithMCPResourceURI(upstreamURI)
	headers.WithMCPServerName(serverInfo.Name)
	mcpReq.serverName = serverInfo.Name

	// resolve or create the backend session for this gateway session.
	exists, cacheErr := s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
	if cacheErr != nil {
		s.Logger.ErrorContext(ctx, "failed to get session from cache", "error", cacheErr)
		span.RecordError(cacheErr)
		span.SetStatus(codes.Error, "session cache error")
		if span.IsRecording() {
			span.SetAttributes(attribute.String("error.type", "session_cache_error"))
		}
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	var remoteMCPSeverSession string
	if id, ok := exists[mcpReq.serverName]; ok {
		s.Logger.DebugContext(ctx, "found session in cache", "session id", mcpReq.GetSessionID(), "for server", serverInfo.Name, "remote session", id)
		remoteMCPSeverSession = id
	}
	if remoteMCPSeverSession == "" {
		id, initErr := s.initializeMCPSeverSession(ctx, mcpReq)
		if initErr != nil {
			var routerErr *RouterError
			if errors.As(initErr, &routerErr) {
				calculatedResponse.WithImmediateResponse(routerErr.Code(), routerErr.Error())
			} else {
				calculatedResponse.WithImmediateResponse(500, "internal error")
			}
			s.Logger.ErrorContext(ctx, "failed to get remote mcp server session id ", "error ", initErr)
			span.RecordError(initErr)
			span.SetStatus(codes.Error, "session initialization failed")
			if span.IsRecording() {
				span.SetAttributes(attribute.String("error.type", "session_init_error"))
			}
			return calculatedResponse.Build()
		}
		remoteMCPSeverSession = id
	}
	mcpReq.backendSessionID = remoteMCPSeverSession
	headers.WithMCPSession(remoteMCPSeverSession)
	headers.WithAuthority(serverInfo.Hostname)

	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to marshal body to bytes ", "error ", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "body marshal failed")
		if span.IsRecording() {
			span.SetAttributes(attribute.String("error.type", "marshal_error"))
		}
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	path, err := serverInfo.Path()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to parse url for backend ", "error ", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "path parse failed")
		if span.IsRecording() {
			span.SetAttributes(attribute.String("error.type", "path_parse_error"))
		}
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	headers.WithPath(path)
	headers.WithContentLength(len(body))
	if mcpReq.Streaming {
		s.Logger.DebugContext(ctx, "returning streaming response")
		calculatedResponse.WithStreamingResponse(headers.Build(), body)
		return calculatedResponse.Build()
	}
	calculatedResponse.WithRequestBodyHeadersAndBodyReponse(headers.Build(), body)
	return calculatedResponse.Build()
}

func buildResourceNotFoundSSEBody(id any) string {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32002,
			"message": "MCP error -32002: Resource not found",
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "event: message\ndata: {\"jsonrpc\":\"2.0\",\"error\":{\"code\":-32002,\"message\":\"MCP error -32002: Resource not found\"}}\n\n"
	}
	return "event: message\ndata: " + string(b) + "\n\n"
}
