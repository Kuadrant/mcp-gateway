package mcprouter

import (
	"context"
	"fmt"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "mcp-router"

func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

type headerCarrier struct {
	headers *corev3.HeaderMap
}

func (c headerCarrier) Get(key string) string {
	if c.headers == nil {
		return ""
	}
	return getSingleValueHeader(c.headers, key)
}

func (c headerCarrier) Set(_, _ string) {}

func (c headerCarrier) Keys() []string {
	if c.headers == nil {
		return nil
	}
	keys := make([]string, 0, len(c.headers.Headers))
	for _, h := range c.headers.Headers {
		keys = append(keys, h.Key)
	}
	return keys
}

func extractTraceContext(ctx context.Context, headers *corev3.HeaderMap) context.Context {
	carrier := headerCarrier{headers: headers}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func spanAttributes(mcpReq *MCPRequest) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", mcpReq.Method),
		attribute.String("jsonrpc.protocol.version", mcpReq.JSONRPC),
	}

	if mcpReq.ID != nil {
		attrs = append(attrs, attribute.String("jsonrpc.request.id", fmt.Sprint(mcpReq.ID)))
	}

	if mcpReq.GetSessionID() != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", mcpReq.GetSessionID()))
	}

	if mcpReq.serverName != "" {
		attrs = append(attrs, attribute.String("mcp.server", mcpReq.serverName))
	}

	if toolName := mcpReq.ToolName(); toolName != "" {
		attrs = append(attrs, attribute.String("gen_ai.tool.name", toolName))
	}

	attrs = append(attrs, attribute.String("gen_ai.operation.name", mcpReq.Method))

	if mcpReq.Headers != nil {
		if addr := getSingleValueHeader(mcpReq.Headers, "x-forwarded-for"); addr != "" {
			attrs = append(attrs, attribute.String("client.address", addr))
		}
	}

	return attrs
}

func recordError(span trace.Span, err error, statusCode int32) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error_source", "ext-proc"),
		attribute.Int("http.status_code", int(statusCode)),
	)
}
