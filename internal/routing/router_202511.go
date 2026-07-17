package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/clients"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

// RoutingTableFunc returns the current routing table snapshot.
//
//nolint:revive // package-qualified name is clearer
type RoutingTableFunc func() RoutingTable

// Router202511 implements Router for the 2025-11-25 protocol (stateful, body-based routing).
type Router202511 struct {
	RoutingConfig       *atomic.Pointer[config.MCPServersConfig]
	Table               RoutingTableFunc
	SessionCache        SessionCache
	JWTManager          *session.JWTManager
	InitForClient       InitForClient
	HairpinClientPool   *clients.HairpinClientPool
	ElicitationMap      idmap.Map
	TokenElicitationMap elicitation.Map
	ElicitationEnabled  bool
	Logger              *slog.Logger
	initGroup           singleflight.Group
}

var _ Router = &Router202511{}

// RouteRequest routes body-based mcp request to backend or broker
func (r *Router202511) RouteRequest(ctx context.Context, req *Request) *Decision {
	if req.Parsed == nil {
		return &Decision{Error: &Error{StatusCode: 400, Message: "no parsed request"}}
	}
	mcpReq := req.Parsed
	table := r.Table()

	ctx, span := tracer().Start(ctx, "mcp-router.route-decision",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", mcpReq.Method),
		),
	)
	defer span.End()

	switch {
	case mcpReq.IsElicitationResponse():
		span.SetAttributes(attribute.String("mcp.route", "elicitation-response"))
		return r.routeElicitationResponse(ctx, mcpReq)
	case mcpReq.Method == MethodToolCall:
		span.SetAttributes(attribute.String("mcp.route", "tool-call"))
		return r.routeToolCall(ctx, table, mcpReq)
	case mcpReq.Method == MethodPromptGet:
		span.SetAttributes(attribute.String("mcp.route", "prompt-get"))
		return r.routePromptGet(ctx, table, mcpReq)
	default:
		span.SetAttributes(attribute.String("mcp.route", "broker"))
		return r.routeBrokerPassthrough(ctx, mcpReq)
	}
}

func (r *Router202511) routeToolCall(ctx context.Context, table RoutingTable, mcpReq *MCPRequest) *Decision {
	toolName := mcpReq.ToolName()

	ctx, span := tracer().Start(ctx, "mcp-router.tool-call",
		trace.WithAttributes(
			componentAttr,
			attribute.String("gen_ai.tool.name", toolName),
			attribute.String("mcp.session.id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID())),
		),
	)
	defer span.End()

	if toolName == "" {
		r.Logger.ErrorContext(ctx, "[EXT-PROC] HandleRequestBody no tool name set in tools/call")
		span.SetStatus(codes.Error, "no tool name set")
		span.SetAttributes(attribute.String("error.type", "missing_tool_name"))
		return &Decision{Error: &Error{StatusCode: 400, Message: "no tool name set"}}
	}

	if sessionErr := r.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		r.Logger.ErrorContext(ctx, "session validation failed", "session", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()), "error", sessionErr)
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		span.SetAttributes(attribute.String("error.type", "invalid_session"))
		return &Decision{Error: &Error{StatusCode: int(sessionErr.Code()), Message: sessionErr.Error()}}
	}

	headers := make(map[string]string)

	// lookup server
	route, ok := table.LookupTool(toolName)
	if !ok {
		route, ok = table.LookupPrefix(toolName)
	}
	if !ok {
		if table.IsBrokerTool(toolName) {
			r.Logger.DebugContext(ctx, "routing broker meta-tool to broker", "toolName", toolName)
			span.SetAttributes(attribute.String("mcp.route", "broker-meta-tool"))
			return r.routeBrokerPassthrough(ctx, mcpReq)
		}
		r.Logger.DebugContext(ctx, "no server for tool", "toolName", toolName)
		mcpotel.SpanError(span, fmt.Errorf("tool not found: %s", toolName), "tool not found")
		span.SetAttributes(attribute.String("error.type", "tool_not_found"))
		return &Decision{
			Error: &Error{
				StatusCode: 200,
				JSONRPCErr: BuildSSEToolError(mcpReq.ID, "MCP error -32602: Tool not found"),
			},
			SetHeaders: map[string]string{
				SessionHeader: mcpReq.GetSessionID(),
			},
		}
	}
	serverInfo := routeToMCPServer(route)

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)

	// tool annotations
	if annotations, ok := table.ToolAnnotations(string(serverInfo.ID()), toolName); ok {
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
		headers[ToolAnnotationsHeader] = strings.Join(parts, ",")
	}

	headers[MethodHeader] = mcpReq.Method
	mcpReq.ServerName = serverInfo.Name
	upstreamToolName, _ := strings.CutPrefix(toolName, serverInfo.Prefix)
	headers[ToolHeader] = upstreamToolName
	mcpReq.ReWriteToolName(upstreamToolName)
	headers[MCPServerNameHeader] = serverInfo.Name

	// token resolution for servers with URL elicitation configured
	if r.ElicitationEnabled && serverInfo.TokenURLElicitation != nil {
		elicitInfo, tokenErr := r.resolveUpstreamToken(ctx, mcpReq, serverInfo, headers)
		if tokenErr != nil {
			mcpotel.SpanError(span, tokenErr, tokenErr.Error())
			var routerErr *RouterError
			if errors.As(tokenErr, &routerErr) {
				span.SetAttributes(attribute.String("error.type", "client_capability"))
				return &Decision{
					Error: &Error{
						StatusCode: 200,
						JSONRPCErr: BuildSSEToolError(mcpReq.ID, tokenErr.Error()),
					},
					SetHeaders: map[string]string{
						SessionHeader: mcpReq.GetSessionID(),
					},
				}
			}
			span.SetAttributes(attribute.String("error.type", "token_resolution"))
			r.Logger.ErrorContext(ctx, "resolveUpstreamToken failed", "error", tokenErr)
			return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
		}
		if elicitInfo != nil {
			headers[MCPServerNameHeader] = "mcpBroker"
			headers[sharedheaders.ElicitationRequestID] = elicitInfo.RequestID
			headers[sharedheaders.ElicitationID] = elicitInfo.ElicitationID
			return &Decision{
				Path:       "/mcp/elicitation",
				SetHeaders: headers,
			}
		}
	}

	return r.routeToUpstream(ctx, span, mcpReq, serverInfo, headers)
}

func (r *Router202511) routePromptGet(ctx context.Context, table RoutingTable, mcpReq *MCPRequest) *Decision {
	promptName := mcpReq.PromptName()

	ctx, span := tracer().Start(ctx, "mcp-router.prompt-get",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.prompt.name", promptName),
			attribute.String("mcp.session.id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID())),
		),
	)
	defer span.End()

	if promptName == "" {
		r.Logger.ErrorContext(ctx, "[EXT-PROC] HandlePromptGet no prompt name set in prompts/get")
		span.SetStatus(codes.Error, "no prompt name set")
		span.SetAttributes(attribute.String("error.type", "missing_prompt_name"))
		return &Decision{Error: &Error{StatusCode: 400, Message: "no prompt name set"}}
	}

	if sessionErr := r.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		r.Logger.ErrorContext(ctx, "session validation failed", "session", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()), "error", sessionErr)
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		span.SetAttributes(attribute.String("error.type", "invalid_session"))
		return &Decision{Error: &Error{StatusCode: int(sessionErr.Code()), Message: sessionErr.Error()}}
	}

	headers := make(map[string]string)
	route, ok := table.LookupPrompt(promptName)
	if !ok {
		r.Logger.DebugContext(ctx, "no server for prompt", "promptName", promptName)
		mcpotel.SpanError(span, fmt.Errorf("prompt not found: %s", promptName), "prompt not found")
		span.SetAttributes(attribute.String("error.type", "prompt_not_found"))
		return &Decision{
			Error: &Error{
				StatusCode: 200,
				JSONRPCErr: "\nevent: message\ndata: {\"error\":{\"code\":-32602,\"message\":\"Prompt not found\"},\"jsonrpc\":\"2.0\"}\n\n",
			},
			SetHeaders: map[string]string{
				SessionHeader: mcpReq.GetSessionID(),
			},
		}
	}
	serverInfo := routeToMCPServer(route)

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)

	headers[MethodHeader] = mcpReq.Method
	mcpReq.ServerName = serverInfo.Name
	upstreamPromptName, _ := strings.CutPrefix(promptName, serverInfo.Prefix)
	headers[PromptHeader] = upstreamPromptName
	mcpReq.ReWritePromptName(upstreamPromptName)
	headers[MCPServerNameHeader] = serverInfo.Name

	return r.routeToUpstream(ctx, span, mcpReq, serverInfo, headers)
}

func (r *Router202511) routeToUpstream(ctx context.Context, span trace.Span, mcpReq *MCPRequest, serverInfo *config.MCPServer, headers map[string]string) *Decision {
	exists, cacheErr := r.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
	if cacheErr != nil {
		r.Logger.ErrorContext(ctx, "failed to get session from cache", "error", cacheErr)
		mcpotel.SpanError(span, cacheErr, "session cache error")
		span.SetAttributes(attribute.String("error.type", "session_cache_error"))
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	var remoteMCPServerSession string
	if id, ok := exists[mcpReq.ServerName]; ok {
		r.Logger.DebugContext(ctx, "found session in cache", "session id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()), "for server", serverInfo.Name, "remote session", internaljwt.LogSafeSessionID(id))
		remoteMCPServerSession = id
	}
	if remoteMCPServerSession == "" {
		id, err := r.initializeMCPServerSession(ctx, mcpReq)
		if err != nil {
			var routerErr *RouterError
			if errors.As(err, &routerErr) {
				return &Decision{Error: &Error{StatusCode: int(routerErr.Code()), Message: routerErr.Error()}}
			}
			return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
		}
		remoteMCPServerSession = id
	}
	mcpReq.BackendSessionID = remoteMCPServerSession

	headers[SessionHeader] = remoteMCPServerSession
	body, err := mcpReq.ToBytes()
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to marshal body to bytes", "error", err)
		mcpotel.SpanError(span, err, "body marshal failed")
		span.SetAttributes(attribute.String("error.type", "marshal_error"))
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	path, err := serverInfo.Path()
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", err)
		mcpotel.SpanError(span, err, "path parse failed")
		span.SetAttributes(attribute.String("error.type", "path_parse_error"))
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	headers["content-length"] = fmt.Sprintf("%d", len(body))

	return &Decision{
		Authority:    serverInfo.Hostname,
		Path:         path,
		SetHeaders:   headers,
		UnsetHeaders: InternalOnlyHeaders,
		BodyMutation: body,
	}
}

func (r *Router202511) routeElicitationResponse(ctx context.Context, mcpReq *MCPRequest) *Decision {
	ctx, span := tracer().Start(ctx, "mcp-router.elicitation-response",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.session.id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID())),
		),
	)
	defer span.End()

	if sessionErr := r.validateSession(mcpReq.GetSessionID()); sessionErr != nil {
		mcpotel.SpanError(span, sessionErr, sessionErr.Error())
		return &Decision{Error: &Error{StatusCode: int(sessionErr.Code()), Message: sessionErr.Error()}}
	}

	gatewayID := fmt.Sprint(mcpReq.ID)
	entry, ok, err := r.ElicitationMap.Lookup(ctx, gatewayID)
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to lookup elicitation mapping", "error", err, "gatewayID", gatewayID)
		mcpotel.SpanError(span, err, "elicitation lookup failed")
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}
	if !ok {
		r.Logger.ErrorContext(ctx, "elicitation response for unknown gateway ID", "gatewayID", gatewayID)
		return &Decision{Error: &Error{StatusCode: 400, Message: "unknown elicitation ID"}}
	}
	if entry.GatewaySessionID != mcpReq.GetSessionID() {
		r.Logger.ErrorContext(ctx, "elicitation session mismatch", "gatewayID", gatewayID, "expected", internaljwt.LogSafeSessionID(entry.GatewaySessionID), "got", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()))
		return &Decision{Error: &Error{StatusCode: 403, Message: "session mismatch"}}
	}

	mcpReq.ID = entry.BackendID

	mcpServerConfig, err := r.RoutingConfig.Load().GetServerConfigByName(entry.ServerName)
	if err != nil {
		r.Logger.ErrorContext(ctx, "server not found for elicitation response", "server", entry.ServerName)
		mcpotel.SpanError(span, err, "server not found")
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	path, err := mcpServerConfig.Path()
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", err)
		mcpotel.SpanError(span, err, "path parse failed")
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	body, err := mcpReq.ToBytes()
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to get bytes for elicitation response", "mcpReqID", mcpReq.ID, "serverName", entry.ServerName)
		mcpotel.SpanError(span, err, "marshal failed")
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	r.ElicitationMap.Remove(ctx, gatewayID)

	return &Decision{
		Authority: mcpServerConfig.Hostname,
		Path:      path,
		SetHeaders: map[string]string{
			SessionHeader:       entry.SessionID,
			MCPServerNameHeader: entry.ServerName,
			"content-length":    fmt.Sprintf("%d", len(body)),
		},
		UnsetHeaders: InternalOnlyHeaders,
		BodyMutation: body,
	}
}

func (r *Router202511) routeBrokerPassthrough(ctx context.Context, mcpReq *MCPRequest) *Decision {
	ctx, span := tracer().Start(ctx, "mcp-router.broker-passthrough",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", mcpReq.Method),
		),
	)
	defer span.End()

	r.Logger.DebugContext(ctx, "HandleMCPBrokerRequest", "HTTP Method", mcpReq.GetSingleHeaderValue(":method"), "mcp method", mcpReq.Method, "session", internaljwt.LogSafeSessionID(mcpReq.SessionID))

	headers := map[string]string{
		MethodHeader: mcpReq.Method,
	}

	if mcpReq.IsInitializeRequest() {
		remoteInitializeTarget := mcpReq.GetSingleHeaderValue("mcp-init-host")
		if remoteInitializeTarget != "" {
			token := mcpReq.GetSingleHeaderValue(RoutingKey)
			if r.JWTManager == nil {
				r.Logger.ErrorContext(ctx, "jwt manager not configured; rejecting initialize hairpin")
				return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
			}
			if err := r.JWTManager.ValidateBackendInitToken(token, remoteInitializeTarget); err != nil {
				r.Logger.WarnContext(ctx, "rejecting initialize hairpin: invalid backend-init token", "error", err, "target", remoteInitializeTarget)
				return &Decision{Error: &Error{StatusCode: 400, Message: "bad request"}}
			}

			r.Logger.DebugContext(ctx, "HandleMCPBrokerRequest initialize request", "target", remoteInitializeTarget, "call", mcpReq.Method)
			return &Decision{
				Authority:    remoteInitializeTarget,
				SetHeaders:   headers,
				UnsetHeaders: append([]string{"mcp-init-host", RoutingKey}, InternalOnlyHeaders...),
			}
		}
	}

	headers[MCPServerNameHeader] = "mcpBroker"
	// re-inject internal headers stripped in the headers phase so the broker can use them for filtering
	for _, name := range InternalOnlyHeaders {
		if v := mcpReq.GetSingleHeaderValue(name); v != "" {
			headers[name] = v
		}
	}
	return &Decision{
		BrokerPass: true,
		SetHeaders: headers,
	}
}

func (r *Router202511) validateSession(sessionID string) *RouterError {
	if sessionID == "" {
		return NewRouterError(400, fmt.Errorf("no session ID found"))
	}
	isInvalid, err := r.JWTManager.Validate(sessionID)
	if err != nil || isInvalid {
		return NewRouterError(401, fmt.Errorf("session no longer valid"))
	}
	return nil
}

func (r *Router202511) initializeMCPServerSession(ctx context.Context, mcpReq *MCPRequest) (string, error) {
	ctx, initSpan := tracer().Start(ctx, "mcp-router.session-init",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.server", mcpReq.ServerName),
			attribute.String("mcp.session.id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID())),
		),
	)
	defer initSpan.End()

	routingCfg := r.RoutingConfig.Load()
	mcpServerConfig, err := routingCfg.GetServerConfigByName(mcpReq.ServerName)
	if err != nil {
		return "", NewRouterErrorf(500, "failed check for server: %w", err)
	}

	groupKey := mcpReq.GetSessionID() + "/" + mcpReq.ServerName
	result, err, _ := r.initGroup.Do(groupKey, func() (any, error) {
		exists, err := r.SessionCache.GetSession(ctx, mcpReq.GetSessionID())
		if err != nil {
			return "", NewRouterErrorf(500, "failed to check for existing session: %w", err)
		}
		if id, ok := exists[mcpReq.ServerName]; ok {
			r.Logger.DebugContext(ctx, "found session in cache", "session id", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()), "for server", mcpServerConfig.Name, "remote session", internaljwt.LogSafeSessionID(id))
			return id, nil
		}
		passThroughHeaders := map[string]string{}
		for key, val := range mcpReq.Headers {
			k := strings.ToLower(key)
			if strings.HasPrefix(k, ":") ||
				k == SessionHeader ||
				k == "mcp-init-host" ||
				k == RoutingKey ||
				k == MCPAuthorizedHeader ||
				k == MCPVirtualServerHeader {
				continue
			}
			passThroughHeaders[key] = val
		}
		passThroughHeaders["x-mcp-method"] = mcpReq.Method
		passThroughHeaders["x-mcp-servername"] = mcpReq.ServerName
		if toolName := mcpReq.ToolName(); toolName != "" {
			passThroughHeaders["x-mcp-toolname"] = toolName
		}
		if promptName := mcpReq.PromptName(); promptName != "" {
			passThroughHeaders["x-mcp-promptname"] = promptName
		}
		passThroughHeaders["user-agent"] = "mcp-router"
		if r.ElicitationEnabled && mcpServerConfig.TokenURLElicitation != nil {
			if userToken, ok, _ := r.SessionCache.GetUserToken(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name); ok {
				passThroughHeaders["authorization"] = userToken
			}
		}

		r.Logger.DebugContext(ctx, "initializing target as no mcp-session-id found for client", "server", mcpReq.ServerName, "passthrough header count", len(passThroughHeaders))

		if !mcpReq.ClientElicitation {
			clientElicitation, elErr := r.SessionCache.GetClientElicitation(ctx, mcpReq.GetSessionID())
			if elErr != nil {
				r.Logger.ErrorContext(ctx, "failed to get client elicitation flag", "error", elErr, "session", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()))
				return "", NewRouterErrorf(500, "failed to read client elicitation flag: %w", elErr)
			}
			mcpReq.ClientElicitation = clientElicitation
		}

		initToken, err := r.JWTManager.GenerateBackendInitToken(mcpServerConfig.Hostname)
		if err != nil {
			r.Logger.ErrorContext(ctx, "failed to generate backend-init token", "error", err)
			mcpotel.SpanError(initSpan, err, "failed to generate backend-init token")
			return "", NewRouterErrorf(500, "failed to generate backend-init token: %w", err)
		}
		passThroughHeaders[RoutingKey] = initToken
		passThroughHeaders["mcp-init-host"] = mcpServerConfig.Hostname
		clientHandle, err := r.InitForClient(ctx, routingCfg.MCPGatewayInternalHostname, mcpServerConfig, passThroughHeaders, mcpReq.ClientElicitation, r.HairpinClientPool)
		if err != nil {
			r.Logger.ErrorContext(ctx, "failed to get remote session ", "error", err)
			mcpotel.SpanError(initSpan, err, "failed to initialize backend session")
			return "", NewRouterErrorf(500, "failed to create session for mcp server: %w", err)
		}
		var sessionCloser = func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			r.Logger.DebugContext(cleanupCtx, "gateway session expired closing client", "Session ", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()))
			if err := clientHandle.Close(); err != nil {
				r.Logger.DebugContext(cleanupCtx, "failed to close client connection", "err", err)
			}
			if err := r.SessionCache.DeleteSessions(cleanupCtx, mcpReq.GetSessionID()); err != nil {
				r.Logger.DebugContext(cleanupCtx, "failed to delete session", "session", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()), "err", err)
			}
		}
		expiresAt, err := r.JWTManager.GetExpiresIn(mcpReq.GetSessionID())
		if err != nil {
			r.Logger.ErrorContext(ctx, "failed to get expires in value. Forcing session reset", "err", err)
			sessionCloser()
			return "", NewRouterError(401, fmt.Errorf("invalid session"))
		}
		ttl := time.Until(expiresAt)
		if ttl <= 0 {
			r.Logger.ErrorContext(ctx, "session already expired, forcing reset", "session", internaljwt.LogSafeSessionID(mcpReq.GetSessionID()))
			sessionCloser()
			return "", NewRouterError(401, fmt.Errorf("invalid session"))
		}
		remoteSessionID := clientHandle.ID()
		r.Logger.DebugContext(ctx, "got remote session id ", "mcp server", mcpServerConfig.Name, "session", internaljwt.LogSafeSessionID(remoteSessionID))
		_, storeErr := r.SessionCache.AddSession(ctx, mcpReq.GetSessionID(), mcpServerConfig.Name, remoteSessionID, ttl)
		if storeErr != nil {
			r.Logger.ErrorContext(ctx, "failed to add remote session to cache", "error", storeErr)
			if closeErr := clientHandle.Close(); closeErr != nil {
				r.Logger.DebugContext(ctx, "failed to close client connection on store error", "err", closeErr)
			}
			return "", NewRouterError(500, fmt.Errorf("internal error"))
		}
		time.AfterFunc(ttl, sessionCloser)
		return remoteSessionID, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (r *Router202511) resolveUpstreamToken(ctx context.Context, mcpReq *MCPRequest, serverInfo *config.MCPServer, headers map[string]string) (*ElicitationInfo, error) {
	sessionID := mcpReq.GetSessionID()

	token, ok, err := r.SessionCache.GetUserToken(ctx, sessionID, serverInfo.Name)
	if err != nil {
		r.Logger.ErrorContext(ctx, "user token cache lookup failed", "error", err)
		return nil, fmt.Errorf("user token lookup: %w", err)
	}
	if ok {
		r.Logger.DebugContext(ctx, "found cached user token", "server", serverInfo.Name)
		headers[AuthorizationHeader] = token
		return nil, nil //nolint:nilnil // nil info = token injected, nil error = success
	}

	authHeader := mcpReq.GetSingleHeaderValue(AuthorizationHeader)
	sub, subErr := internaljwt.ExtractSubClaim(authHeader)
	if subErr != nil {
		r.Logger.ErrorContext(ctx, "authorization JWT missing sub claim", "error", subErr)
		return nil, &RouterError{StatusCode: 400, Err: fmt.Errorf("authorization token missing sub claim: %w", subErr)}
	}

	clientElicitation, elErr := r.SessionCache.GetClientElicitation(ctx, sessionID)
	if elErr != nil {
		r.Logger.ErrorContext(ctx, "failed to check client elicitation", "error", elErr)
		return nil, fmt.Errorf("client elicitation check: %w", elErr)
	}
	if !clientElicitation {
		return nil, &RouterError{StatusCode: 400, Err: fmt.Errorf("upstream server requires a per-user token but client does not support elicitation")}
	}

	elicitationID, storeErr := r.TokenElicitationMap.Store(ctx, sessionID, serverInfo.Name, sub)
	if storeErr != nil {
		r.Logger.ErrorContext(ctx, "failed to store elicitation entry", "error", storeErr)
		return nil, fmt.Errorf("elicitation store: %w", storeErr)
	}

	idBytes, _ := json.Marshal(mcpReq.ID)
	r.Logger.DebugContext(ctx, "elicitation required", "elicitationID", elicitationID)
	return &ElicitationInfo{RequestID: string(idBytes), ElicitationID: elicitationID}, nil
}

// routeToMCPServer converts a ServerRoute to a config.MCPServer for
// compatibility with code that still needs config.MCPServer (e.g. session init).
func routeToMCPServer(route *ServerRoute) *config.MCPServer {
	svr := &config.MCPServer{
		Name:             route.Name,
		Hostname:         route.Host,
		Prefix:           route.Prefix,
		URL:              route.URL,
		UserSpecificList: route.UserSpecificList,
	}
	if route.TokenURLElicitation != nil {
		svr.TokenURLElicitation = &config.TokenURLElicitationConfig{
			URL: route.TokenURLElicitation.URL,
		}
	}
	return svr
}
