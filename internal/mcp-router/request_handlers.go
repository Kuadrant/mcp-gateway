package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
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
)

// MCPRequest encapsulates a mcp protocol request to the gateway
type MCPRequest struct {
	ID         *int              `json:"id"`
	JSONRPC    string            `json:"jsonrpc"`
	Method     string            `json:"method"`
	Params     map[string]any    `json:"params"`
	Headers    *corev3.HeaderMap `json:"-"`
	Streaming  bool              `json:"-"`
	sessionID  string            `json:"-"`
	serverName string            `json:"-"`
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
	if mr.JSONRPC != "2.0" {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("json rpc version invalid"))
	}
	if mr.Method == "" {
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
	s.Logger.Debug("HandleMCPRequest ", "session id", mcpReq.GetSessionID())
	switch mcpReq.Method {
	case methodToolCall:
		return s.HandleToolCall(ctx, mcpReq)
	default:
		return s.HandleNoneToolCall(mcpReq)
	}
}

// HandleToolCall will handle an MCP Tool Call
func (s *ExtProcServer) HandleToolCall(ctx context.Context, mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	calculatedResponse := NewResponse()
	// handle tools call
	toolName := mcpReq.ToolName()
	if toolName == "" {
		s.Logger.Error("[EXT-PROC] HandleRequestBody no tool name set in tools/call")
		calculatedResponse.WithImmediateResponse(400, "no tool name set")
		return calculatedResponse.Build()
	}
	if mcpReq.GetSessionID() == "" {
		s.Logger.Info("No mcp-session-id found in headers")
		calculatedResponse.WithImmediateResponse(400, "no session ID found")
		return calculatedResponse.Build()
	}
	// This request wont go through the broker so needs to be validated
	isInvalidSession, err := s.JWTManager.Validate(mcpReq.GetSessionID())
	if err != nil {
		s.Logger.Error("failed to validate session", "session", mcpReq.GetSessionID(), "error ", err)
		calculatedResponse.WithImmediateResponse(404, "session no longer valid")
		return calculatedResponse.Build()
	}
	if isInvalidSession {
		s.Logger.Debug("invalid session ", "session", mcpReq.GetSessionID())
		calculatedResponse.WithImmediateResponse(404, "session no longer valid")
		return calculatedResponse.Build()
	}

	// Get tool annotations from broker and set headers
	headers := NewHeaders()
	serverInfo, err := s.Broker.GetServerInfo(toolName)
	if err != nil {
		// For unknown tool, the spec says to return a JSON RPC error response,
		// and the error is not an HTTP error, so we return a 200 status code.
		// See https://modelcontextprotocol.io/specification/2025-06-18/server/tools#error-handling
		s.Logger.Debug("no server for tool", "toolName", toolName)
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
	exists, err := s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
	if err != nil {
		s.Logger.Error("failed to get session from cache", "error", err)
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	var remoteMCPSeverSession string
	if id, ok := exists[mcpReq.serverName]; ok {
		s.Logger.Debug("found session in cache", "session id", mcpReq.GetSessionID(), "for server", serverInfo.Name, "remote session", id)
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
			s.Logger.Error("failed to get remote mcp server session id ", "error ", err)
			return calculatedResponse.Build()
		}
		remoteMCPSeverSession = id
	}
	headers.WithMCPSession(remoteMCPSeverSession)
	// reset the host name now we have identified the correct tool and backend
	headers.WithAuthority(serverInfo.Hostname)
	// prepare request body for MCP Backend
	body, err := mcpReq.ToBytes()
	if err != nil {
		s.Logger.Error("failed to marshal body to bytes ", "error ", err)
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	path, err := serverInfo.Path()
	if err != nil {
		s.Logger.Error("failed to parse url for backend ", "error ", err)
		calculatedResponse.WithImmediateResponse(500, "internal error")
		return calculatedResponse.Build()
	}
	headers.WithPath(path)
	headers.WithContentLength(len(body))
	if mcpReq.Streaming {
		s.Logger.Debug("returning streaming response")
		calculatedResponse.WithStreamingResponse(headers.Build(), body)
		return calculatedResponse.Build()
	}
	calculatedResponse.WithRequestBodyHeadersAndBodyReponse(headers.Build(), body)
	return calculatedResponse.Build()
}

// initializeMCPSeverSession will create a new session and connection with the backend MCP server
// This connection is kept open for the life of the gateway session.
// TODO when we receive a 404 from a backend MCP Server we should have a way to close the connection at that point also currently when we receive a 404 we remove the session from cache and will open a new connection. They will all be closed once the gateway session expires or the client sends a delete but it is a source of potential leaks
func (s *ExtProcServer) initializeMCPSeverSession(ctx context.Context, mcpReq *MCPRequest) (string, error) {
	mcpServerConfig := s.RoutingConfig.GetServerConfigByName(mcpReq.serverName)
	exists, err := s.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
	if err != nil {
		return "", NewRouterErrorf(500, "failed to check for existing session: %w", err)
	}
	if id, ok := exists[mcpReq.serverName]; ok {
		s.Logger.Debug("found session in cache", "session id", mcpReq.GetSessionID(), "for server", mcpServerConfig.Name, "remote session", id)
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
	s.Logger.Debug("initializing target as no mcp-session-id found for client", "server ", mcpReq.serverName, "with passthrough headers", passThroughHeaders)

	clientHandle, err := s.InitForClient(ctx, s.RoutingConfig.MCPGatewayInternalHostname, s.RoutingConfig.RouterAPIKey, mcpServerConfig, passThroughHeaders)
	if err != nil {
		s.Logger.Error("failed to get remote session ", "error", err)
		return "", NewRouterErrorf(500, "failed to create session for mcp server: %w", err)
	}
	var sessionCloser = func() {
		s.Logger.Debug("gateway session expired closing client", "Session ", mcpReq.GetSessionID())
		if err := clientHandle.Close(); err != nil {
			s.Logger.Debug("failed to close client connection", "err", err)
		}
		if err := s.SessionCache.DeleteSessions(ctx, mcpReq.GetSessionID()); err != nil {
			s.Logger.Debug("failed to delete session", "session", mcpReq.GetSessionID(), "err", err)
		}
	}
	// close connection with remote backend and delete any sessions when our gateway session expires
	expiresAt, err := s.JWTManager.GetExpiresIn(mcpReq.GetSessionID())
	if err != nil {
		// this err would be caused by an invalid token so force a re-initialize
		s.Logger.Error("failed to get expires in value. Forcing session reset", "err", err)
		sessionCloser()
		return "", NewRouterError(404, fmt.Errorf("invalid session"))
	}
	time.AfterFunc(time.Until(expiresAt), sessionCloser)
	remoteSessionID := clientHandle.GetSessionId()
	s.Logger.Debug("got remote session id ", "mcp server", mcpServerConfig.Name, "session", remoteSessionID)
	if _, err := s.SessionCache.AddSession(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name, remoteSessionID); err != nil {
		s.Logger.Error("failed to add remote session to cache", "error", err)
		// again if this fails it is likely terminal due to a network connection error
		return "", NewRouterError(500, fmt.Errorf("internal error"))
	}
	return remoteSessionID, nil

}

// HandleNoneToolCall handles none tools calls such as initialize. The majority of these requests will be forwarded to the broker
func (s *ExtProcServer) HandleNoneToolCall(mcpReq *MCPRequest) []*eppb.ProcessingResponse {
	s.Logger.Debug("HandleMCPBrokerRequest", "HTTP Method", mcpReq.GetSingleHeaderValue(":method"), "mcp method", mcpReq.Method, "session", mcpReq.sessionID)
	headers := NewHeaders().WithMCPMethod(mcpReq.Method)
	response := NewResponse()
	if mcpReq.isInitializeRequest() {
		remoteInitializeTarget := mcpReq.GetSingleHeaderValue("mcp-init-host")
		if remoteInitializeTarget != "" {
			// TODO look to use a signed key possible the JWT session key
			key := mcpReq.GetSingleHeaderValue(RoutingKey)
			if key != s.RoutingConfig.RouterAPIKey {
				s.Logger.Warn("unexpected remote initialize request. Key does not match. Rejecting", "sent headers", mcpReq.Headers)
				return response.WithImmediateResponse(400, "bad request").Build()
			}

			s.Logger.Debug("HandleMCPBrokerRequest initialize request", "target", remoteInitializeTarget, "call", mcpReq.Method)
			headers.WithAuthority(remoteInitializeTarget)
			// ensure we unset the router specific headers so they are not sent to the backend
			return response.WithRequestBodySetUnsetHeadersResponse(headers.Build(), []string{"mcp-init-host", RoutingKey}).Build()
		}

	}
	headers.WithMCPServerName("mcpBroker")
	// none tool call set headers
	return response.WithRequestBodyHeadersResponse(headers.Build()).Build()

}
