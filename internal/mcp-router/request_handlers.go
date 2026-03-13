package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
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
	methodToolCall    = "tools/call"
	methodInitialize  = "initialize"
	methodInitialized = "notification/initialized"

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

// ToBytes marshals the data ready to send on
func (mr *MCPRequest) ToBytes() ([]byte, error) {
	return json.Marshal(mr)
}

// HandleRequestHeaders handles request headers minimally.
func (s *ExtProcServer) HandleRequestHeaders(_ *eppb.HttpHeaders) ([]*eppb.ProcessingResponse, error) {
	s.Logger.Info("Request Handler: HandleRequestHeaders called")
	requestHeaders := NewHeaders()
	response := NewResponse()
	requestHeaders.WithAuthority(s.RoutingConfig.MCPGatewayExternalHostname)
	return response.WithRequestHeadersReponse(requestHeaders.Build()).Build(), nil
}

// RouteMCPRequest handles request bodies for MCP requests.
func (s *ExtProcServer) RouteMCPRequest(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	ctx, span := tracer().Start(ctx, "mcp-router.route-decision",
		trace.WithAttributes(
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
		return NewRouterError(404, fmt.Errorf("session no longer valid"))
	}
	return nil
}

// HandleToolCall will handle an MCP Tool Call
func (s *ExtProcServer) HandleToolCall(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	toolName := mcpReq.ToolName()

	ctx, span := tracer().Start(ctx, "mcp-router.tool-call",
		trace.WithAttributes(
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
		span.RecordError(sessionErr)
		span.SetStatus(codes.Error, sessionErr.Error())
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
				attribute.String("gen_ai.tool.name", toolName),
			),
		)
		var infoErr error
		serverInfo, infoErr = s.Broker.GetServerInfo(toolName)
		if infoErr != nil {
			infoSpan.RecordError(infoErr)
			infoSpan.SetStatus(codes.Error, "tool not found")
		}
		infoSpan.End()
		err = infoErr
	}
	if err != nil {
		s.Logger.DebugContext(ctx, "no server for tool", "toolName", toolName)
		span.RecordError(err)
		span.SetStatus(codes.Error, "tool not found")
		span.SetAttributes(attribute.String("error.type", "tool_not_found"))
		calculatedResponse.WithImmediateJSONRPCResponse(200,
			[]*corev3.HeaderValueOption{
				{
					Header: &corev3.HeaderValue{
						Key:   "mcp-session-id",
						Value: mcpReq.GetSessionID(),
					},
				},
			},
			`
event: message
data: {"result":{"content":[{"type":"text","text":"MCP error -32602: Tool not found"}],"isError":true},"jsonrpc":"2.0"}`)
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
	upstreamToolName, _ := strings.CutPrefix(toolName, serverInfo.ToolPrefix)
	headers.WithMCPToolName(upstreamToolName)
	mcpReq.ReWriteToolName(upstreamToolName)
	headers.WithMCPServerName(serverInfo.Name)

	// create a new session with backend mcp if one doesn't exist
	var exists map[string]string
	{
		_, cacheSpan := tracer().Start(ctx, "mcp-router.session-cache.get",
			trace.WithAttributes(
				attribute.String("mcp.session.id", mcpReq.GetSessionID()),
			),
		)
		var cacheErr error
		exists, cacheErr = s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
		if cacheErr != nil {
			cacheSpan.RecordError(cacheErr)
			cacheSpan.SetStatus(codes.Error, "session cache get failed")
		}
		cacheSpan.End()
		err = cacheErr
	}
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to get session from cache", "error", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "session cache error")
		span.SetAttributes(attribute.String("error.type", "session_cache_error"))
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	var remoteMCPSeverSession string
	if id, ok := exists[mcpReq.serverName]; ok {
		s.Logger.DebugContext(ctx, "found session in cache", "session id", mcpReq.GetSessionID(), "for server", serverInfo.Name, "remote session", id)
		remoteMCPSeverSession = id
	}
	if remoteMCPSeverSession == "" {
		id, err := s.initializeMCPSeverSession(ctx, mcpReq)
		if err != nil {
			var routerErr *RouterError
			if errors.As(err, &routerErr) {
				calculatedResponse.WithImmediateResponse(routerErr.Code(), routerErr.Error())
			} else {
				calculatedResponse.WithImmediateResponse(500, "internal error")
			}
			s.Logger.ErrorContext(ctx, "failed to get remote mcp server session id ", "error ", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, "session initialization failed")
			span.SetAttributes(attribute.String("error.type", "session_init_error"))
			return calculatedResponse.Build()
		}
		remoteMCPSeverSession = id
	}
	// store the backend session id so that if an elicitation request is streamed in the response
	// we have the correct session to route back to
	mcpReq.backendSessionID = remoteMCPSeverSession

	headers.WithMCPSession(remoteMCPSeverSession)
	// reset the host name now we have identified the correct tool and backend
	headers.WithAuthority(serverInfo.Hostname)
	// prepare request body for MCP Backend
	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to marshal body to bytes ", "error ", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "body marshal failed")
		span.SetAttributes(attribute.String("error.type", "marshal_error"))
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	path, err := serverInfo.Path()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to parse url for backend ", "error ", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "path parse failed")
		span.SetAttributes(attribute.String("error.type", "path_parse_error"))
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

// HandleElicitationResponse routes an elicitation response from the client to the correct backend server
func (s *ExtProcServer) HandleElicitationResponse(
	ctx context.Context,
	mcpReq *MCPRequest,
) []*eppb.ProcessingResponse {
	response := NewResponse()

	if sessionErr := s.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		response.WithImmediateResponse(sessionErr.Code(), sessionErr.Error())
		return response.Build()
	}

	gatewayID := fmt.Sprint(mcpReq.ID)

	entry, ok, err := s.ElicitationMap.Lookup(ctx, gatewayID)
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to lookup elicitation mapping", "error", err, "gatewayID", gatewayID)
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}
	if !ok {
		s.Logger.ErrorContext(ctx, "elicitation response for unknown gateway ID", "gatewayID", gatewayID)
		response.WithImmediateResponse(400, "unknown elicitation ID")
		return response.Build()
	}
	if entry.GatewaySessionID != mcpReq.GetSessionID() {
		s.Logger.ErrorContext(ctx, "elicitation session mismatch", "gatewayID", gatewayID, "expected", entry.GatewaySessionID, "got", mcpReq.GetSessionID())
		response.WithImmediateResponse(403, "session mismatch")
		return response.Build()
	}

	// restore the id for the request
	mcpReq.ID = entry.BackendID

	mcpServerConfig, err := s.RoutingConfig.GetServerConfigByName(entry.ServerName)
	if err != nil {
		s.Logger.ErrorContext(ctx, "server not found for elicitation response", "server", entry.ServerName)
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
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}
	headers.WithPath(path)

	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to get bytes for elicitation response", "mcpReqID", mcpReq.ID, "serverName", entry.ServerName)
		response.WithImmediateResponse(500, "internal error")
		return response.Build()
	}

	headers.WithContentLength(len(body))
	response.WithRequestBodyHeadersAndBodyReponse(headers.Build(), body)

	// remove the mapping only after the response was successfully built
	s.ElicitationMap.Remove(ctx, gatewayID)

	return response.Build()
}

// initializeMCPSeverSession will create a new session and connection with the backend MCP server
// This connection is kept open for the life of the gateway session to ensure the backend session is not closed/invalidated.
// TODO when we receive a 404 from a backend MCP Server we should have a way to close the connection at that point also currently when we receive a 404 we remove the session from cache and will open a new connection. They will all be closed once the gateway session expires or the client sends a delete but it is a source of potential leaks
func (s *ExtProcServer) initializeMCPSeverSession(ctx context.Context, mcpReq *MCPRequest) (string, error) {
	ctx, initSpan := tracer().Start(ctx, "mcp-router.session-init",
		trace.WithAttributes(
			attribute.String("mcp.server", mcpReq.serverName),
			attribute.String("mcp.session.id", mcpReq.GetSessionID()),
		),
	)
	defer initSpan.End()

	mcpServerConfig, err := s.RoutingConfig.GetServerConfigByName(mcpReq.serverName)
	if err != nil {
		return "", NewRouterErrorf(500, "failed check for server: %w", err)
	}
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
		// We don't want to pass through any sudo routing headers :authority, :path etc or the mcp-session-id from the gateway. The mcp-session-id will be
		// set by the client based on the target backend. otherwise pass through everything from the client in case of custom headers
		for _, h := range mcpReq.Headers.Headers {
			if !strings.HasPrefix(strings.ToLower(h.Key), ":") && strings.ToLower(h.Key) != "mcp-session-id" {
				passThroughHeaders[h.Key] = string(h.RawValue)
			}
		}
		// ensure these gateway heades are set
		passThroughHeaders["x-mcp-method"] = mcpReq.Method
		passThroughHeaders["x-mcp-servername"] = mcpReq.serverName
		passThroughHeaders["x-mcp-toolname"] = mcpReq.ToolName()
		passThroughHeaders["user-agent"] = "mcp-router"
	}
	s.Logger.DebugContext(ctx, "initializing target as no mcp-session-id found for client", "server ", mcpReq.serverName, "with passthrough headers", passThroughHeaders)

	// check if the original client declared elicitation support
	if !mcpReq.clientElicitation {
		clientElicitation, elErr := s.SessionCache.GetClientElicitation(ctx, mcpReq.GetSessionID())
		if elErr != nil {
			s.Logger.ErrorContext(ctx, "failed to get client elicitation flag", "error", elErr, "session", mcpReq.GetSessionID())
			return "", NewRouterErrorf(500, "failed to read client elicitation flag: %w", elErr)
		}
		mcpReq.clientElicitation = clientElicitation
	}

	clientHandle, err := s.InitForClient(ctx, s.RoutingConfig.MCPGatewayInternalHostname, s.RoutingConfig.RouterAPIKey, mcpServerConfig, passThroughHeaders, mcpReq.clientElicitation)
	if err != nil {
		s.Logger.ErrorContext(ctx, "failed to get remote session ", "error", err)
		initSpan.RecordError(err)
		initSpan.SetStatus(codes.Error, "failed to initialize backend session")
		return "", NewRouterErrorf(500, "failed to create session for mcp server: %w", err)
	}
	var sessionCloser = func() {
		s.Logger.DebugContext(ctx, "gateway session expired closing client", "Session ", mcpReq.GetSessionID())
		if err := clientHandle.Close(); err != nil {
			s.Logger.DebugContext(ctx, "failed to close client connection", "err", err)
		}
		if err := s.SessionCache.DeleteSessions(ctx, mcpReq.GetSessionID()); err != nil {
			s.Logger.DebugContext(ctx, "failed to delete session", "session", mcpReq.GetSessionID(), "err", err)
		}
	}
	// close connection with remote backend and delete any sessions when our gateway session expires
	expiresAt, err := s.JWTManager.GetExpiresIn(mcpReq.GetSessionID())
	if err != nil {
		// this err would be caused by an invalid token so force a re-initialize
		s.Logger.ErrorContext(ctx, "failed to get expires in value. Forcing session reset", "err", err)
		sessionCloser()
		return "", NewRouterError(404, fmt.Errorf("invalid session"))
	}
	time.AfterFunc(time.Until(expiresAt), sessionCloser)
	remoteSessionID := clientHandle.GetSessionId()
	s.Logger.DebugContext(ctx, "got remote session id ", "mcp server", mcpServerConfig.Name, "session", remoteSessionID)
	{
		_, storeSpan := tracer().Start(ctx, "mcp-router.session-cache.store",
			trace.WithAttributes(
				attribute.String("mcp.session.id", mcpReq.GetSessionID()),
				attribute.String("mcp.server", mcpServerConfig.Name),
			),
		)
		_, storeErr := s.SessionCache.AddSession(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name, remoteSessionID)
		if storeErr != nil {
			storeSpan.RecordError(storeErr)
			storeSpan.SetStatus(codes.Error, "session cache store failed")
		}
		storeSpan.End()
		if storeErr != nil {
			s.Logger.ErrorContext(ctx, "failed to add remote session to cache", "error", storeErr)
			// again if this fails it is likely terminal due to a network connection error
			return "", NewRouterError(500, fmt.Errorf("internal error"))
		}
	}
	return remoteSessionID, nil

}

// HandleNoneToolCall handles none tools calls such as initialize. The majority of these requests will be forwarded to the broker
func (s *ExtProcServer) HandleNoneToolCall(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	ctx, span := tracer().Start(ctx, "mcp-router.broker-passthrough",
		trace.WithAttributes(
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
			// TODO look to use a signed key possible the JWT session key
			key := mcpReq.GetSingleHeaderValue(RoutingKey)
			if key != s.RoutingConfig.RouterAPIKey {
				s.Logger.WarnContext(ctx, "unexpected remote initialize request. Key does not match. Rejecting", "sent headers", mcpReq.Headers)
				return response.WithImmediateResponse(400, "bad request").Build()
			}

			s.Logger.DebugContext(ctx, "HandleMCPBrokerRequest initialize request", "target", remoteInitializeTarget, "call", mcpReq.Method)
			headers.WithAuthority(remoteInitializeTarget)
			// ensure we unset the router specific headers so they are not sent to the backend
			return response.WithRequestBodySetUnsetHeadersResponse(headers.Build(), []string{"mcp-init-host", RoutingKey}).Build()
		}

	}
	headers.WithMCPServerName("mcpBroker")
	// none tool call set headers
	return response.WithRequestBodyHeadersResponse(headers.Build()).Build()

}
