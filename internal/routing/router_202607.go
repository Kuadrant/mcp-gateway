package routing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Router202607 implements Router for the 2026-07-28 protocol (stateless, header-based routing).
type Router202607 struct {
	Table         RoutingTableFunc
	RoutingConfig *atomic.Pointer[config.MCPServersConfig]
	Logger        *slog.Logger
}

var _ Router = &Router202607{}

// RouteRequest routes header-based mcp request to backend or broker
func (r *Router202607) RouteRequest(ctx context.Context, req *Request) *Decision {
	table := r.Table()

	ctx, span := tracer().Start(ctx, "mcp-router.route-decision",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", req.MCPMethod),
			attribute.String("protocol.version", "2026-07-28"),
		),
	)
	defer span.End()

	if expected := r.RoutingConfig.Load().MCPGatewayExternalHostname; expected != "" && req.Authority != "" && req.Authority != expected {
		r.Logger.ErrorContext(ctx, "authority mismatch", "expected", expected, "got", req.Authority)
		span.SetStatus(codes.Error, "authority mismatch")
		span.SetAttributes(attribute.String("error.type", "authority_mismatch"))
		return &Decision{Error: &Error{StatusCode: 400, Message: "bad request"}}
	}

	switch req.MCPMethod {
	case MethodToolCall:
		span.SetAttributes(attribute.String("mcp.route", "tool-call"))
		return r.routeToolCall(ctx, table, req)
	case MethodPromptGet:
		span.SetAttributes(attribute.String("mcp.route", "prompt-get"))
		return r.routePromptGet(ctx, table, req)
	default:
		span.SetAttributes(attribute.String("mcp.route", "broker"))
		return r.routeBrokerPassthrough(ctx, req)
	}
}

func (r *Router202607) routeToolCall(ctx context.Context, table RoutingTable, req *Request) *Decision {
	toolName := req.MCPName

	ctx, span := tracer().Start(ctx, "mcp-router.tool-call",
		trace.WithAttributes(
			componentAttr,
			attribute.String("gen_ai.tool.name", toolName),
		),
	)
	defer span.End()

	if toolName == "" {
		r.Logger.ErrorContext(ctx, "[EXT-PROC] HandleRequestBody no tool name set in tools/call")
		span.SetStatus(codes.Error, "no tool name set")
		span.SetAttributes(attribute.String("error.type", "missing_tool_name"))
		return &Decision{Error: &Error{StatusCode: 400, Message: "no tool name set"}}
	}

	headers := make(map[string]string)

	route, ok := table.LookupTool(toolName)
	if !ok {
		route, ok = table.LookupPrefix(toolName)
	}
	if !ok {
		if table.IsBrokerTool(toolName) {
			r.Logger.DebugContext(ctx, "routing broker meta-tool to broker", "toolName", toolName)
			span.SetAttributes(attribute.String("mcp.route", "broker-meta-tool"))
			return r.routeBrokerPassthrough(ctx, req)
		}
		// route unknown tools to the broker so it returns a proper JSON-RPC
		// error through its normal handler chain. ext_proc immediate_response
		// bypasses the HTTP response flow and produces responses that SDK
		// clients cannot parse.
		r.Logger.DebugContext(ctx, "unknown tool, routing to broker", "toolName", toolName)
		span.SetAttributes(attribute.String("mcp.route", "broker-unknown-tool"))
		return r.routeBrokerPassthrough(ctx, req)
	}
	serverInfo := routeToMCPServer(route)

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)

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

	headers[MethodHeader] = req.MCPMethod
	upstreamToolName, _ := strings.CutPrefix(toolName, serverInfo.Prefix)
	headers[ToolHeader] = upstreamToolName
	headers[MCPServerNameHeader] = serverInfo.Name
	headers["mcp-name"] = upstreamToolName

	bodyMutation, routerErr := r.validateAndRewriteBody(ctx, span, req, toolName, serverInfo.Prefix, upstreamToolName, true)
	if routerErr != nil {
		return &Decision{Error: routerErr}
	}
	if bodyMutation != nil {
		headers["content-length"] = fmt.Sprintf("%d", len(bodyMutation))
	}

	path, pathErr := serverInfo.Path()
	if pathErr != nil {
		r.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", pathErr)
		span.SetStatus(codes.Error, "path parse failed")
		span.SetAttributes(attribute.String("error.type", "path_parse_error"))
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	return &Decision{
		Authority:    serverInfo.Hostname,
		Path:         path,
		SetHeaders:   headers,
		UnsetHeaders: InternalOnlyHeaders,
		BodyMutation: bodyMutation,
	}
}

func (r *Router202607) routePromptGet(ctx context.Context, table RoutingTable, req *Request) *Decision {
	promptName := req.MCPName

	ctx, span := tracer().Start(ctx, "mcp-router.prompt-get",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.prompt.name", promptName),
		),
	)
	defer span.End()

	if promptName == "" {
		r.Logger.ErrorContext(ctx, "[EXT-PROC] HandlePromptGet no prompt name set in prompts/get")
		span.SetStatus(codes.Error, "no prompt name set")
		span.SetAttributes(attribute.String("error.type", "missing_prompt_name"))
		return &Decision{Error: &Error{StatusCode: 400, Message: "no prompt name set"}}
	}

	headers := make(map[string]string)
	route, ok := table.LookupPrompt(promptName)
	if !ok {
		r.Logger.DebugContext(ctx, "no server for prompt", "promptName", promptName)
		span.SetStatus(codes.Error, "prompt not found")
		span.SetAttributes(attribute.String("error.type", "prompt_not_found"))
		return &Decision{
			Error: &Error{
				StatusCode:  200,
				JSONRPCErr:  BuildJSONToolError(req.RequestID, "MCP error -32602: Prompt not found"),
				ContentType: "application/json",
			},
		}
	}
	serverInfo := routeToMCPServer(route)

	span.SetAttributes(
		attribute.String("mcp.server", serverInfo.Name),
		attribute.String("mcp.server.hostname", serverInfo.Hostname),
	)

	headers[MethodHeader] = req.MCPMethod
	upstreamPromptName, _ := strings.CutPrefix(promptName, serverInfo.Prefix)
	headers[PromptHeader] = upstreamPromptName
	headers[MCPServerNameHeader] = serverInfo.Name
	headers["mcp-name"] = upstreamPromptName

	bodyMutation, routerErr := r.validateAndRewriteBody(ctx, span, req, promptName, serverInfo.Prefix, upstreamPromptName, false)
	if routerErr != nil {
		return &Decision{Error: routerErr}
	}
	if bodyMutation != nil {
		headers["content-length"] = fmt.Sprintf("%d", len(bodyMutation))
	}

	path, pathErr := serverInfo.Path()
	if pathErr != nil {
		r.Logger.ErrorContext(ctx, "failed to parse url for backend", "error", pathErr)
		span.SetStatus(codes.Error, "path parse failed")
		span.SetAttributes(attribute.String("error.type", "path_parse_error"))
		return &Decision{Error: &Error{StatusCode: 500, Message: "internal error"}}
	}

	return &Decision{
		Authority:    serverInfo.Hostname,
		Path:         path,
		SetHeaders:   headers,
		UnsetHeaders: InternalOnlyHeaders,
		BodyMutation: bodyMutation,
	}
}

func (r *Router202607) routeBrokerPassthrough(ctx context.Context, req *Request) *Decision {
	ctx, span := tracer().Start(ctx, "mcp-router.broker-passthrough",
		trace.WithAttributes(
			componentAttr,
			attribute.String("mcp.method.name", req.MCPMethod),
		),
	)
	defer span.End()

	r.Logger.DebugContext(ctx, "HandleMCPBrokerRequest", "mcp method", req.MCPMethod)

	headers := map[string]string{
		MethodHeader:        req.MCPMethod,
		MCPServerNameHeader: "mcpBroker",
	}

	if req.Parsed != nil {
		for _, name := range InternalOnlyHeaders {
			if v := req.Parsed.GetSingleHeaderValue(name); v != "" {
				headers[name] = v
			}
		}
	}

	return &Decision{
		BrokerPass: true,
		SetHeaders: headers,
	}
}

func (r *Router202607) validateAndRewriteBody(ctx context.Context, span trace.Span, req *Request, headerName, prefix, upstreamName string, isTool bool) ([]byte, *Error) {
	if req.Parsed == nil {
		return nil, nil
	}

	var bodyName string
	if isTool {
		bodyName = req.Parsed.ToolName()
	} else {
		bodyName = req.Parsed.PromptName()
	}

	if bodyName != headerName {
		r.Logger.ErrorContext(ctx, "header-body mismatch", "headerName", headerName, "bodyName", bodyName)
		span.SetStatus(codes.Error, "header-body mismatch")
		span.SetAttributes(attribute.String("error.type", "header_mismatch"))
		return nil, &Error{
			StatusCode:  200,
			JSONRPCErr:  BuildJSONToolError(req.Parsed.ID, "MCP error -32602: HeaderMismatch: Mcp-Name header does not match body"),
			ContentType: "application/json",
		}
	}

	if prefix == "" {
		return nil, nil
	}

	if isTool {
		req.Parsed.ReWriteToolName(upstreamName)
	} else {
		req.Parsed.ReWritePromptName(upstreamName)
	}

	bytes, err := req.Parsed.ToBytes()
	if err != nil {
		r.Logger.ErrorContext(ctx, "failed to marshal body to bytes", "error", err)
		span.SetStatus(codes.Error, "body marshal failed")
		span.SetAttributes(attribute.String("error.type", "marshal_error"))
		return nil, &Error{StatusCode: 500, Message: "internal error"}
	}

	return bytes, nil
}
