package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrInvalidRequest is an error for an invalid request
var ErrInvalidRequest = fmt.Errorf("MCP Request is invalid")

// RouterError represents an error with an associated HTTP status code
type RouterError struct {
	StatusCode int32
	Err        error
}

// Error implements the error interface
func (re *RouterError) Error() string {
	if re.Err != nil {
		return re.Err.Error()
	}
	return fmt.Sprintf("router error: status %d", re.StatusCode)
}

// Unwrap implements the errors.Unwrap interface for error wrapping
func (re *RouterError) Unwrap() error {
	return re.Err
}

// Code returns the HTTP status code
func (re *RouterError) Code() int32 {
	return re.StatusCode
}

// NewRouterError creates a new RouterError with the given status code and error
func NewRouterError(code int32, err error) *RouterError {
	return &RouterError{
		StatusCode: code,
		Err:        err,
	}
}

// NewRouterErrorf creates a new RouterError with formatted error message
func NewRouterErrorf(code int32, format string, args ...any) *RouterError {
	return &RouterError{
		StatusCode: code,
		Err:        fmt.Errorf(format, args...),
	}
}

const (
	methodToolCall   = "tools/call"
	methodPromptGet  = "prompts/get"
	methodInitialize = "initialize"

	elicitationResultAction  = "action"
	elicitationActionAccept  = "accept"
	elicitationActionDecline = "decline"
	elicitationActionCancel  = "cancel"
)

// MCPRequest encapsulates a mcp protocol request to the gateway
type MCPRequest struct {
	ID                any               `json:"id"`
	JSONRPC           string            `json:"jsonrpc"`
	Method            string            `json:"method,omitempty"`
	Params            map[string]any    `json:"params,omitempty"`
	Result            map[string]any    `json:"result,omitempty"` // set in elicitation responses (which are a request from the client)
	Headers           *corev3.HeaderMap `json:"-"`
	Streaming         bool              `json:"-"`
	sessionID         string            `json:"-"`
	serverName        string            `json:"-"`
	backendSessionID  string            `json:"-"`
	clientElicitation bool              `json:"-"`
}

// GetSingleHeaderValue returns a single header value
func (mr *MCPRequest) GetSingleHeaderValue(key string) string {
	return getSingleValueHeader(mr.Headers, key)
}

// GetSessionID returns the mcp session id
func (mr *MCPRequest) GetSessionID() string {
	if mr.sessionID == "" {
		mr.sessionID = getSingleValueHeader(mr.Headers, sessionHeader)
	}
	return mr.sessionID
}

// Validate validates the mcp request
func (mr *MCPRequest) Validate() (bool, error) {
	if mr == nil {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("JSON invalid"))
	}
	if mr.JSONRPC != "2.0" {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("json rpc version invalid"))
	}
	// elicitation responses (sent as a request to the server) do not have the method set
	if mr.Method == "" && !mr.isElicitationResponse() {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("no method set in json rpc payload"))
	}
	if mr.ID == nil && !mr.isNotificationRequest() {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("no id set in json rpc payload for none notification method: %s ", mr.Method))
	}

	return true, nil
}

func (mr *MCPRequest) isNotificationRequest() bool {
	return strings.HasPrefix(mr.Method, "notifications")
}

// isToolCall will check if the request is a tool call request
func (mr *MCPRequest) isToolCall() bool {
	return mr.Method == "tools/call"
}

// isInitializeRequest returns true if the method is initialize or initialized
func (mr *MCPRequest) isInitializeRequest() bool {
	return mr.Method == "initialize" || mr.Method == "notifications/initialized"
}

// clientSupportsElicitation checks if an initialize request declares elicitation support
func (mr *MCPRequest) clientSupportsElicitation() bool {
	if mr.Method != methodInitialize || mr.Params == nil {
		return false
	}
	caps, ok := mr.Params["capabilities"]
	if !ok {
		return false
	}
	capsMap, ok := caps.(map[string]any)
	if !ok {
		return false
	}
	_, hasElicitation := capsMap["elicitation"]
	return hasElicitation
}

func (mr *MCPRequest) isElicitationResponse() bool {
	// elicitation responses do not set the method
	if mr.Method != "" || mr.Result == nil {
		return false
	}

	action, ok := mr.Result[elicitationResultAction]
	if !ok {
		return false
	}

	a, ok := action.(string)
	if !ok {
		return false
	}

	return a == elicitationActionAccept || a == elicitationActionDecline || a == elicitationActionCancel
}

// ToolName returns the tool name in a tools/call request
func (mr *MCPRequest) ToolName() string {
	if !mr.isToolCall() {
		return ""
	}
	tool, ok := mr.Params["name"]
	if !ok {
		return ""
	}
	t, ok := tool.(string)
	if !ok {
		return ""
	}
	return t
}

// ReWriteToolName will allow re-setting the tool name to something different. This is needed to remove prefix
// and set the actual tool name
func (mr *MCPRequest) ReWriteToolName(actualTool string) {
	mr.Params["name"] = actualTool
}

// isPromptGet will check if the request is a prompts/get request
func (mr *MCPRequest) isPromptGet() bool {
	return mr.Method == methodPromptGet
}

// PromptName returns the prompt name in a prompts/get request
func (mr *MCPRequest) PromptName() string {
	if !mr.isPromptGet() {
		return ""
	}
	prompt, ok := mr.Params["name"]
	if !ok {
		return ""
	}
	p, ok := prompt.(string)
	if !ok {
		return ""
	}
	return p
}

// ReWritePromptName will allow re-setting the prompt name to remove prefix
func (mr *MCPRequest) ReWritePromptName(actualPrompt string) {
	mr.Params["name"] = actualPrompt
}

// ToBytes marshals the data ready to send on
func (mr *MCPRequest) ToBytes() ([]byte, error) {
	return json.Marshal(mr)
}

// HandleRequestHeaders handles request headers minimally.
func (s *ExtProcServer) HandleRequestHeaders(ctx context.Context, headers *eppb.HttpHeaders) ([]*eppb.ProcessingResponse, error) {
	s.Logger.DebugContext(ctx, "Request Handler: HandleRequestHeaders called")
	requestHeaders := NewHeaders()
	response := NewResponse()
	requestHeaders.WithAuthority(s.RoutingConfig.MCPGatewayExternalHostname)
	// Trust model: ExtractSubClaim only decodes the JWT payload, it does NOT
	// verify the signature. That is safe here because AuthPolicy validates the
	// Authorization JWT before the request reaches ext_proc, so by this point
	// the token is already trusted. We surface the verified sub as the internal
	// x-mcp-verified-sub header so the broker can bind token submissions to an
	// identity without re-parsing the raw token. The header is in
	// internalOnlyHeaders, so any client-supplied value is stripped first.
	authHeader := getSingleValueHeader(headers.GetHeaders(), authorizationHeader)
	if sub, _ := internaljwt.ExtractSubClaim(authHeader); sub != "" {
		requestHeaders.WithVerifiedSub(sub)
	}
	return response.WithRequestHeadersResponse(requestHeaders.Build(), internalOnlyHeaders...).Build(), nil
}

// RouteMCPRequest handles request bodies for MCP requests.
func (s *ExtProcServer) RouteMCPRequest(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	ctx, span := tracer().Start(ctx, "mcp-router.route-decision",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", mcpReq.Method),
		),
	)
	defer span.End()

	s.Logger.DebugContext(ctx, "HandleMCPRequest ", "session id", mcpReq.GetSessionID())
	switch {
	case mcpReq.isElicitationResponse():
		span.SetAttributes(attribute.String("mcp.route", "elicitation-response"))
		return s.HandleElicitationResponse(ctx, mcpReq)
	case mcpReq.Method == methodToolCall:
		span.SetAttributes(attribute.String("mcp.route", "tool-call"))
		return s.HandleToolCall(ctx, mcpReq)
	case mcpReq.Method == methodPromptGet:
		span.SetAttributes(attribute.String("mcp.route", "prompt-get"))
		return s.HandlePromptGet(ctx, mcpReq)
	default:
		span.SetAttributes(attribute.String("mcp.route", "broker"))
		return s.HandleNoneToolCall(ctx, mcpReq)
	}
}

// validateSession checks for a valid session ID and JWT
func (s *ExtProcServer) validateSession(sessionID string) *RouterError {
	if sessionID == "" {
		return NewRouterError(400, fmt.Errorf("no session ID found"))
	}
	isInvalid, err := s.JWTManager.Validate(sessionID)
	if err != nil || isInvalid {
		return NewRouterError(401, fmt.Errorf("session no longer valid"))
	}
	return nil
}

// HandleToolCall will handle an MCP Tool Call
func (s *ExtProcServer) HandleToolCall(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	toolName := mcpReq.ToolName()

	ctx, span := tracer().Start(ctx, "mcp-router.tool-call",
		trace.WithAttributes(
			componentAttr,
			attribute.String("gen_ai.tool.name", toolName),
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		),
	)
	defer span.End()

	calculatedResponse := NewResponse()
	// handle tools call
	if toolName == "" {
		s.Logger.ErrorContext(ctx, "[EXT-PROC] HandleRequestBody no tool name set in tools/call")
		span.SetStatus(codes.Error, "no tool name set")
		span.SetAttributes(attribute.String("error.type", "missing_tool_name"))
		calculatedResponse.WithImmediateResponse(400, "no tool name set")
		return calculatedResponse.Build()
	}
	if sessionErr := s.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		s.Logger.ErrorContext(ctx, "session validation failed", "session", mcpReq.GetSessionID(), "error", sessionErr)
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		span.SetAttributes(attribute.String("error.type", "invalid_session"))
		calculatedResponse.WithImmediateResponse(sessionErr.Code(), sessionErr.Error())
		return calculatedResponse.Build()
	}

	// Get tool annotations from broker and set headers
	headers := NewHeaders()
	var serverInfo *config.MCPServer
	var err error
	{
		_, infoSpan := tracer().Start(ctx, "mcp-router.broker.get-server-info",
			trace.WithAttributes(
				componentAttr,
				attribute.String("gen_ai.tool.name", toolName),
			),
		)
		var infoErr error
		serverInfo, infoErr = s.Broker.GetServerInfo(toolName)
		if infoErr != nil {
			mcpotel.SpanError(infoSpan, infoErr, "tool not found")
		}
		infoSpan.End()
		err = infoErr
	}
	if err != nil {
		// broker meta-tools (discover_tools, select_tools) are not upstream tools;
		// route them to the broker like any non-tool-call request
		if s.Broker.IsBrokerToolName(toolName) {
			s.Logger.DebugContext(ctx, "routing broker meta-tool to broker", "toolName", toolName)
			span.SetAttributes(attribute.String("mcp.route", "broker-meta-tool"))
			return s.HandleNoneToolCall(ctx, mcpReq)
		}
		s.Logger.DebugContext(ctx, "no server for tool", "toolName", toolName)
		mcpotel.SpanError(span, err, "tool not found")
		span.SetAttributes(attribute.String("error.type", "tool_not_found"))
		calculatedResponse.WithImmediateJSONRPCResponse(200,
			[]*corev3.HeaderValueOption{
				{
					Header: &corev3.HeaderValue{
						Key:      "mcp-session-id",
						RawValue: []byte(mcpReq.GetSessionID()),
					},
				},
			},
			buildSSEToolError(mcpReq.ID, "MCP error -32602: Tool not found"))
		return calculatedResponse.Build()
	}

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)
	if annotations, hasAnnotations := s.Broker.ToolAnnotations(serverInfo.ID(), toolName); hasAnnotations {
		// build header value (e.g. readOnly=true,destructive=false,openWorld=true)
		var parts []string
		push := func(key string, val *bool) {
			if val == nil {
				parts = append(parts, fmt.Sprintf("%s=unspecified", key))
			} else if *val {
				parts = append(parts, fmt.Sprintf("%s=true", key))
			} else {
				parts = append(parts, fmt.Sprintf("%s=false", key))
			}
		}

		push("readOnly", annotations.ReadOnlyHint)
		push("destructive", annotations.DestructiveHint)
		push("idempotent", annotations.IdempotentHint)
		push("openWorld", annotations.OpenWorldHint)

		hintsHeader := strings.Join(parts, ",")
		headers.WithToolAnnotations(hintsHeader)
	}

	headers.WithMCPMethod(mcpReq.Method)
	mcpReq.serverName = serverInfo.Name
	upstreamToolName, _ := strings.CutPrefix(toolName, serverInfo.Prefix)
	headers.WithMCPToolName(upstreamToolName)
	mcpReq.ReWriteToolName(upstreamToolName)
	headers.WithMCPServerName(serverInfo.Name)

	// token resolution for servers with URL elicitation configured
	if s.ElicitationEnabled && serverInfo.TokenURLElicitation != nil {
		elicitInfo, tokenErr := s.resolveUpstreamToken(ctx, mcpReq, serverInfo, headers)
		if tokenErr != nil {
			mcpotel.SpanError(span, tokenErr, tokenErr.Error())
			var routerErr *RouterError
			if errors.As(tokenErr, &routerErr) {
				span.SetAttributes(attribute.String("error.type", "client_capability"))
				calculatedResponse.WithImmediateJSONRPCResponse(200,
					[]*corev3.HeaderValueOption{
						{
							Header: &corev3.HeaderValue{
								Key:      "mcp-session-id",
								RawValue: []byte(mcpReq.GetSessionID()),
							},
						},
					},
					buildSSEToolError(mcpReq.ID, tokenErr.Error()))
			} else {
				span.SetAttributes(attribute.String("error.type", "token_resolution"))
				s.Logger.ErrorContext(ctx, "resolveUpstreamToken failed", "error", tokenErr)
				calculatedResponse.WithImmediateResponse(500, "internal error")
			}
			return calculatedResponse.Build()
		}
		if elicitInfo != nil {
			// No stored token. Send request to broker to handle URLElicitation
			headers.WithMCPServerName("mcpBroker")
			headers.WithPath("/mcp/elicitation")
			headers.WithCustomHeader(sharedheaders.ElicitationRequestID, elicitInfo.requestID)
			headers.WithCustomHeader(sharedheaders.ElicitationID, elicitInfo.elicitationID)
			calculatedResponse.WithRequestBodyHeadersResponse(headers.Build())
			return calculatedResponse.Build()
		}
	}

	return s.routeToUpstream(ctx, span, mcpReq, serverInfo, headers, calculatedResponse)
}

// HandlePromptGet handles an MCP prompts/get request by routing to the correct upstream server
func (s *ExtProcServer) HandlePromptGet(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	promptName := mcpReq.PromptName()

	ctx, span := tracer().Start(ctx, "mcp-router.prompt-get",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.prompt.name", promptName),
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		),
	)
	defer span.End()

	calculatedResponse := NewResponse()
	if promptName == "" {
		s.Logger.ErrorContext(ctx, "[EXT-PROC] HandlePromptGet no prompt name set in prompts/get")
		span.SetStatus(codes.Error, "no prompt name set")
		span.SetAttributes(attribute.String("error.type", "missing_prompt_name"))
		calculatedResponse.WithImmediateResponse(400, "no prompt name set")
		return calculatedResponse.Build()
	}
	if sessionErr := s.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		s.Logger.ErrorContext(ctx, "session validation failed", "session", mcpReq.GetSessionID(), "error", sessionErr)
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		span.SetAttributes(attribute.String("error.type", "invalid_session"))
		calculatedResponse.WithImmediateResponse(sessionErr.Code(), sessionErr.Error())
		return calculatedResponse.Build()
	}

	headers := NewHeaders()
	var serverInfo *config.MCPServer
	var err error
	{
		_, infoSpan := tracer().Start(ctx, "mcp-router.broker.get-server-info-by-prompt",
			trace.WithAttributes(
				componentAttr,
				attribute.String("mcp.prompt.name", promptName),
			),
		)
		var infoErr error
		serverInfo, infoErr = s.Broker.GetServerInfoByPrompt(promptName)
		if infoErr != nil {
			mcpotel.SpanError(infoSpan, infoErr, "prompt not found")
		}
		infoSpan.End()
		err = infoErr
	}
	if err != nil {
		s.Logger.DebugContext(ctx, "no server for prompt", "promptName", promptName)
		mcpotel.SpanError(span, err, "prompt not found")
		span.SetAttributes(attribute.String("error.type", "prompt_not_found"))
		calculatedResponse.WithImmediateJSONRPCResponse(200,
			[]*corev3.HeaderValueOption{
				{
					Header: &corev3.HeaderValue{
						Key:      "mcp-session-id",
						RawValue: []byte(mcpReq.GetSessionID()),
					},
				},
			},
			`
event: message
data: {"error":{"code":-32602,"message":"Prompt not found"},"jsonrpc":"2.0"}`)
		return calculatedResponse.Build()
	}

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)

	headers.WithMCPMethod(mcpReq.Method)
	mcpReq.serverName = serverInfo.Name
	upstreamPromptName, _ := strings.CutPrefix(promptName, serverInfo.Prefix)
	headers.WithMCPPromptName(upstreamPromptName)
	mcpReq.ReWritePromptName(upstreamPromptName)
	headers.WithMCPServerName(serverInfo.Name)

	return s.routeToUpstream(ctx, span, mcpReq, serverInfo, headers, calculatedResponse)
}

func (s *ExtProcServer) routeToUpstream(ctx context.Context, span trace.Span, mcpReq *MCPRequest, serverInfo *config.MCPServer, headers *HeadersBuilder, calculatedResponse *ResponseBuilder) []*eppb.ProcessingResponse {
	var exists map[string]string
	{
		_, cacheSpan := tracer().Start(ctx, "mcp-router.session-cache.get",
			trace.WithAttributes(
				componentAttr,
				attribute.String("mcp.session.id", mcpReq.GetSessionID()),
			),
		)
		var cacheErr error
		exists, cacheErr = s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
		if cacheErr != nil {
			mcpotel.SpanError(cacheSpan, cacheErr, "session cache get failed")
		}
		cacheSpan.End()
		if cacheErr != nil {
			s.Logger.ErrorContext(ctx, "failed to get session from cache", "error", cacheErr)
			mcpotel.SpanError(span, cacheErr, "session cache error")
			span.SetAttributes(attribute.String("error.type", "session_cache_error"))
			calculatedResponse.WithImmediateResponse(500, "internal error")
			return calculatedResponse.Build()
		}
	}
	var remoteMCPServerSession string
	if id, ok := exists[mcpReq.serverName]; ok {
		s.Logger.DebugContext(ctx, "found session in cache", "session id", mcpReq.GetSessionID(), "for server", serverInfo.Name, "remote session", id)
		remoteMCPServerSession = id
	}
	if remoteMCPServerSession == "" {
		id, err := s.initializeMCPServerSession(ctx, mcpReq)
		if err != nil {
			var routerErr *RouterError
			if errors.As(err, &routerErr) {
				calculatedResponse.WithImmediateResponse(routerErr.Code(), routerErr.Error())
			} else {
				calculatedResponse.WithImmediateResponse(500, "internal error")
			}
			s.Logger.ErrorContext(ctx, "failed to get remote mcp server session id", "error", err)
			mcpotel.SpanError(span, err, "session initialization failed")
			span.SetAttributes(attribute.String("error.type", "session_init_error"))
			return calculatedResponse.Build()
		}
		remoteMCPServerSession = id
	}
	mcpReq.backendSessionID = remoteMCPServerSession

	headers.WithMCPSession(remoteMCPServerSession)
	headers.WithAuthority(serverInfo.Hostname)
	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to marshal body to bytes", "error", err)
		mcpotel.SpanError(span, err, "body marshal failed")
		span.SetAttributes(attribute.String("error.type", "marshal_error"))
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	path, err := serverInfo.Path()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", err)
		mcpotel.SpanError(span, err, "path parse failed")
		span.SetAttributes(attribute.String("error.type", "path_parse_error"))
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	headers.WithPath(path)
	headers.WithContentLength(len(body))
	if mcpReq.Streaming {
		s.Logger.DebugContext(ctx, "returning streaming response")
		calculatedResponse.WithStreamingResponse(headers.Build(), body, internalOnlyHeaders...)
		return calculatedResponse.Build()
	}
	calculatedResponse.WithRequestBodyHeadersAndBodyResponse(headers.Build(), body, internalOnlyHeaders...)
	return calculatedResponse.Build()
}

// HandleElicitationResponse routes an elicitation response from the client to the correct backend server
func (s *ExtProcServer) HandleElicitationResponse(
	ctx context.Context,
	mcpReq *MCPRequest,
) []*eppb.ProcessingResponse {
	ctx, span := tracer().Start(ctx, "mcp-router.elicitation-response",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		),
	)
	defer span.End()

	response := NewResponse()

	if sessionErr := s.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		response.WithImmediateResponse(sessionErr.Code(), sessionErr.Error())
		return response.Build()
	}

	gatewayID := fmt.Sprint(mcpReq.ID)

	entry, ok, err := s.ElicitationMap.Lookup(ctx, gatewayID)
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to lookup elicitation mapping", "error", err, "gatewayID", gatewayID)
		mcpotel.SpanError(span, err, "elicitation lookup failed")
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}
	if !ok {
		lookupErr := fmt.Errorf("elicitation response for unknown gateway ID: %s", gatewayID)
		s.Logger.ErrorContext(ctx, "elicitation response for unknown gateway ID", "gatewayID", gatewayID)
		mcpotel.SpanError(span, lookupErr, "unknown elicitation ID")
		response.WithImmediateResponse(400, "unknown elicitation ID")
		return response.Build()
	}
	if entry.GatewaySessionID != mcpReq.GetSessionID() {
		mismatchErr := fmt.Errorf("elicitation session mismatch: expected %s, got %s", entry.GatewaySessionID, mcpReq.GetSessionID())
		s.Logger.ErrorContext(ctx, "elicitation session mismatch", "gatewayID", gatewayID, "expected", entry.GatewaySessionID, "got", mcpReq.GetSessionID())
		mcpotel.SpanError(span, mismatchErr, "session mismatch")
		response.WithImmediateResponse(403, "session mismatch")
		return response.Build()
	}

	// restore the id for the request
	mcpReq.ID = entry.BackendID

	mcpServerConfig, err := s.RoutingConfig.GetServerConfigByName(entry.ServerName)
	if err != nil {
		s.Logger.ErrorContext(ctx, "server not found for elicitation response", "server", entry.ServerName)
		mcpotel.SpanError(span, err, "server not found")
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}

	headers := NewHeaders()
	headers.WithAuthority(mcpServerConfig.Hostname)
	headers.WithMCPSession(entry.SessionID) // entry.SessionID contains the backend session id from when the elicitation request was made
	headers.WithMCPServerName(entry.ServerName)
	path, err := mcpServerConfig.Path()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", err)
		mcpotel.SpanError(span, err, "path parse failed")
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}
	headers.WithPath(path)

	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to get bytes for elicitation response", "mcpReqID", mcpReq.ID, "serverName", entry.ServerName)
		mcpotel.SpanError(span, err, "marshal failed")
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}

	headers.WithContentLength(len(body))
	response.WithRequestBodyHeadersAndBodyResponse(headers.Build(), body)

	// remove the mapping only after the response was successfully built
	s.ElicitationMap.Remove(ctx, gatewayID)

	return response.Build()
}

// initializeMCPServerSession will create a new session and connection with the backend MCP server
// This connection is kept open for the life of the gateway session to ensure the backend session is not closed/invalidated.
// TODO when we receive a 404 from a backend MCP Server we should have a way to close the connection at that point also currently when we receive a 404 we remove the session from cache and will open a new connection. They will all be closed once the gateway session expires or the client sends a delete but it is a source of potential leaks
func (s *ExtProcServer) initializeMCPServerSession(ctx context.Context, mcpReq *MCPRequest) (string, error) {
	ctx, initSpan := tracer().Start(ctx, "mcp-router.session-init",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.server", mcpReq.serverName),
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		),
	)
	defer initSpan.End()

	mcpServerConfig, err := s.RoutingConfig.GetServerConfigByName(mcpReq.serverName)
	if err != nil {
		return "", NewRouterErrorf(500, "failed check for server: %w", err)
	}

	// Serialize concurrent initialization for the same (gateway session, backend server) pair.
	// Without this, N concurrent tool calls that all see an empty cache would each call
	// clients.Initialize, creating N backend sessions and leaking N-1 connections until JWT expiry.
	groupKey := mcpReq.GetSessionID() + "/" + mcpReq.serverName
	result, err, _ := s.initGroup.Do(groupKey, func() (any, error) {
		// Re-check the cache inside the singleflight: a previous call may have already
		// stored a session while the current goroutine was waiting for the group lock.
		exists, err := s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
		if err != nil {
			return "", NewRouterErrorf(500, "failed to check for existing session: %w", err)
		}
		if id, ok := exists[mcpReq.serverName]; ok {
			s.Logger.DebugContext(ctx, "found session in cache", "session id", mcpReq.GetSessionID(), "for server", mcpServerConfig.Name, "remote session", id)
			return id, nil
		}
		passThroughHeaders := map[string]string{}
		if mcpReq.Headers != nil {
			// We don't want to pass through any pseudo routing headers (:authority,
			// :path, etc.), the gateway-bound mcp-session-id, or the router-internal
			// headers (mcp-init-host, router-key) which we set ourselves below.
			// Dropping the router-internal headers here is defense-in-depth so a
			// client-supplied value can never reach the hairpin request.
			// Everything else is passed through for custom headers.
			for _, h := range mcpReq.Headers.Headers {
				key := strings.ToLower(h.Key)
				if strings.HasPrefix(key, ":") ||
					key == "mcp-session-id" ||
					key == "mcp-init-host" ||
					key == RoutingKey ||
					key == mcpAuthorizedHeader ||
					key == mcpVirtualServerHeader {
					continue
				}
				passThroughHeaders[h.Key] = string(h.RawValue)
			}
			// ensure these gateway headers are set
			passThroughHeaders["x-mcp-method"] = mcpReq.Method
			passThroughHeaders["x-mcp-servername"] = mcpReq.serverName
			if toolName := mcpReq.ToolName(); toolName != "" {
				passThroughHeaders["x-mcp-toolname"] = toolName
			}
			if promptName := mcpReq.PromptName(); promptName != "" {
				passThroughHeaders["x-mcp-promptname"] = promptName
			}
			passThroughHeaders["user-agent"] = "mcp-router"
		}
		// inject cached user token so the hairpin initialize reaches the backend
		if s.ElicitationEnabled && mcpServerConfig.TokenURLElicitation != nil {
			if userToken, ok, _ := s.SessionCache.GetUserToken(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name); ok {
				passThroughHeaders["authorization"] = userToken
			}
		}

		s.Logger.DebugContext(ctx, "initializing target as no mcp-session-id found for client", "server", mcpReq.serverName, "passthrough header count", len(passThroughHeaders))

		// check if the original client declared elicitation support
		if !mcpReq.clientElicitation {
			clientElicitation, elErr := s.SessionCache.GetClientElicitation(ctx, mcpReq.GetSessionID())
			if elErr != nil {
				s.Logger.ErrorContext(ctx, "failed to get client elicitation flag", "error", elErr, "session", mcpReq.GetSessionID())
				return "", NewRouterErrorf(500, "failed to read client elicitation flag: %w", elErr)
			}
			mcpReq.clientElicitation = clientElicitation
		}

		// mint a short-lived JWT bound to the target hostname; the router validates
		// this token when the hairpin request re-enters the gateway in
		// HandleNoneToolCall so we can never be tricked into routing to an
		// attacker-controlled host by a forged `mcp-init-host` header.
		initToken, err := s.JWTManager.GenerateBackendInitToken(mcpServerConfig.Hostname)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to generate backend-init token", "error", err)
			mcpotel.SpanError(initSpan, err, "failed to generate backend-init token")
			return "", NewRouterErrorf(500, "failed to generate backend-init token: %w", err)
		}
		passThroughHeaders[RoutingKey] = initToken
		passThroughHeaders["mcp-init-host"] = mcpServerConfig.Hostname
		clientHandle, err := s.InitForClient(ctx, s.RoutingConfig.MCPGatewayInternalHostname, mcpServerConfig, passThroughHeaders, mcpReq.clientElicitation, s.HairpinClientPool)
		if err != nil {
			s.Logger.ErrorContext(ctx, "failed to get remote session ", "error", err)
			mcpotel.SpanError(initSpan, err, "failed to initialize backend session")
			return "", NewRouterErrorf(500, "failed to create session for mcp server: %w", err)
		}
		var sessionCloser = func() {
			// use a fresh context: the request-scoped ctx is canceled long before this fires
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			s.Logger.DebugContext(cleanupCtx, "gateway session expired closing client", "Session ", mcpReq.GetSessionID())
			if err := clientHandle.Close(); err != nil {
				s.Logger.DebugContext(cleanupCtx, "failed to close client connection", "err", err)
			}
			if err := s.SessionCache.DeleteSessions(cleanupCtx, mcpReq.GetSessionID()); err != nil {
				s.Logger.DebugContext(cleanupCtx, "failed to delete session", "session", mcpReq.GetSessionID(), "err", err)
			}
		}
		// close connection with remote backend and delete any sessions when our gateway session expires
		expiresAt, err := s.JWTManager.GetExpiresIn(mcpReq.GetSessionID())
		if err != nil {
			// this err would be caused by an invalid token so force a re-initialize
			s.Logger.ErrorContext(ctx, "failed to get expires in value. Forcing session reset", "err", err)
			sessionCloser()
			return "", NewRouterError(401, fmt.Errorf("invalid session"))
		}
		// compute once: reuse for both the Redis TTL and the cleanup timer so they
		// are always in sync, and guard against a near-zero/negative value which
		// would make the Redis write non-expiring.
		ttl := time.Until(expiresAt)
		if ttl <= 0 {
			s.Logger.ErrorContext(ctx, "session already expired, forcing reset", "session", mcpReq.GetSessionID())
			sessionCloser()
			return "", NewRouterError(401, fmt.Errorf("invalid session"))
		}
		remoteSessionID := clientHandle.GetSessionId()
		s.Logger.DebugContext(ctx, "got remote session id ", "mcp server", mcpServerConfig.Name, "session", remoteSessionID)
		{
			_, storeSpan := tracer().Start(ctx, "mcp-router.session-cache.store",
				trace.WithAttributes(
					attribute.String("mcp.session.id", mcpReq.GetSessionID()),
					attribute.String("mcp.server", mcpServerConfig.Name),
				),
			)
			_, storeErr := s.SessionCache.AddSession(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name, remoteSessionID, ttl)
			if storeErr != nil {
				mcpotel.SpanError(storeSpan, storeErr, "session cache store failed")
			}
			storeSpan.End()
			if storeErr != nil {
				s.Logger.ErrorContext(ctx, "failed to add remote session to cache", "error", storeErr)
				// close the handle immediately; the timer is not yet armed so this is the only cleanup path
				if closeErr := clientHandle.Close(); closeErr != nil {
					s.Logger.DebugContext(ctx, "failed to close client connection on store error", "err", closeErr)
				}
				return "", NewRouterError(500, fmt.Errorf("internal error"))
			}
		}
		// arm the cleanup timer only after the session is safely recorded in the cache
		time.AfterFunc(ttl, sessionCloser)
		return remoteSessionID, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// HandleNoneToolCall handles none tools calls such as initialize. The majority of these requests will be forwarded to the broker
func (s *ExtProcServer) HandleNoneToolCall(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	ctx, span := tracer().Start(ctx, "mcp-router.broker-passthrough",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", mcpReq.Method),
		),
	)
	defer span.End()

	s.Logger.DebugContext(ctx, "HandleMCPBrokerRequest", "HTTP Method", mcpReq.GetSingleHeaderValue(":method"), "mcp method", mcpReq.Method, "session", mcpReq.sessionID)
	headers := NewHeaders().WithMCPMethod(mcpReq.Method)
	response := NewResponse()
	if mcpReq.isInitializeRequest() {
		remoteInitializeTarget := mcpReq.GetSingleHeaderValue("mcp-init-host")
		if remoteInitializeTarget != "" {
			// validate the backend-init JWT bound to the target host. Any
			// caller-controlled value here is rejected because they cannot
			// produce a valid signature against the gateway's HMAC key.
			token := mcpReq.GetSingleHeaderValue(RoutingKey)
			if s.JWTManager == nil {
				s.Logger.ErrorContext(ctx, "jwt manager not configured; rejecting initialize hairpin")
				return response.WithImmediateResponse(500, "internal error").Build()
			}
			if err := s.JWTManager.ValidateBackendInitToken(token, remoteInitializeTarget); err != nil {
				s.Logger.WarnContext(ctx, "rejecting initialize hairpin: invalid backend-init token", "error", err, "target", remoteInitializeTarget)
				return response.WithImmediateResponse(400, "bad request").Build()
			}

			s.Logger.DebugContext(ctx, "HandleMCPBrokerRequest initialize request", "target", remoteInitializeTarget, "call", mcpReq.Method)
			headers.WithAuthority(remoteInitializeTarget)
			// ensure we unset the router specific headers so they are not sent to the backend
			return response.WithRequestBodySetUnsetHeadersResponse(headers.Build(), append([]string{"mcp-init-host", RoutingKey}, internalOnlyHeaders...)).Build()
		}

	}
	headers.WithMCPServerName("mcpBroker")
	// re-inject internal headers stripped in the headers phase so the broker can use them for filtering
	for _, name := range internalOnlyHeaders {
		if v := mcpReq.GetSingleHeaderValue(name); v != "" {
			headers.WithCustomHeader(name, v)
		}
	}
	return response.WithRequestBodyHeadersResponse(headers.Build()).Build()

}

// elicitationInfo holds the data needed to route an elicitation request to the broker.
type elicitationInfo struct {
	requestID     string // JSON-serialized JSON-RPC request ID
	elicitationID string // unique ID for this elicitation round-trip
}

// resolveUpstreamToken checks for a cached upstream token and injects it, or signals that
// URL elicitation is needed. Returns (nil, nil) when a cached token was found and injected
// into headers. Returns (*elicitationInfo, nil) when elicitation is required. Returns
// (nil, error) on infrastructure failures.
func (s *ExtProcServer) resolveUpstreamToken(ctx context.Context, mcpReq *MCPRequest, serverInfo *config.MCPServer, headers *HeadersBuilder) (*elicitationInfo, error) {
	sessionID := mcpReq.GetSessionID()

	token, ok, err := s.SessionCache.GetUserToken(ctx, sessionID, serverInfo.Name)
	if err != nil {
		s.Logger.ErrorContext(ctx, "user token cache lookup failed", "error", err)
		return nil, fmt.Errorf("user token lookup: %w", err)
	}
	if ok {
		s.Logger.DebugContext(ctx, "found cached user token", "server", serverInfo.Name)
		headers.WithAuth(token)
		return nil, nil //nolint:nilnil // nil info = token injected, nil error = success
	}

	authHeader := mcpReq.GetSingleHeaderValue(authorizationHeader)
	sub, subErr := internaljwt.ExtractSubClaim(authHeader)
	if subErr != nil {
		s.Logger.ErrorContext(ctx, "authorization JWT missing sub claim", "error", subErr)
		return nil, &RouterError{StatusCode: 400, Err: fmt.Errorf("authorization token missing sub claim: %w", subErr)}
	}

	clientElicitation, elErr := s.SessionCache.GetClientElicitation(ctx, sessionID)
	if elErr != nil {
		s.Logger.ErrorContext(ctx, "failed to check client elicitation", "error", elErr)
		return nil, fmt.Errorf("client elicitation check: %w", elErr)
	}
	if !clientElicitation {
		return nil, &RouterError{StatusCode: 400, Err: fmt.Errorf("upstream server requires a per-user token but client does not support elicitation")}
	}

	elicitationID, storeErr := s.TokenElicitationMap.Store(ctx, sessionID, serverInfo.Name, sub)
	if storeErr != nil {
		s.Logger.ErrorContext(ctx, "failed to store elicitation entry", "error", storeErr)
		return nil, fmt.Errorf("elicitation store: %w", storeErr)
	}

	idBytes, _ := json.Marshal(mcpReq.ID)
	s.Logger.DebugContext(ctx, "elicitation required", "elicitationID", elicitationID)
	return &elicitationInfo{requestID: string(idBytes), elicitationID: elicitationID}, nil
}

// sseJSONRPC writes the SSE envelope and JSON-RPC preamble, then calls
// writeBody to append the response-specific payload. The caller must NOT
// write the closing "\n\n" — sseJSONRPC appends it.
func sseJSONRPC(requestID any, writeBody func(b *strings.Builder)) string {
	var b strings.Builder
	b.WriteString("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":")
	idBytes, _ := json.Marshal(requestID)
	b.Write(idBytes)
	writeBody(&b)
	b.WriteString("\n\n")
	return b.String()
}

func buildSSEToolError(requestID any, message string) string {
	return sseJSONRPC(requestID, func(b *strings.Builder) {
		b.WriteString(",\"result\":{\"content\":[{\"type\":\"text\",\"text\":")
		b.WriteString(strconv.Quote(message))
		b.WriteString("}],\"isError\":true}}")
	})
}
