package mcprouter

import (
	"context"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "mcp-router"

// tracer returns the tracer for the mcp-router package
func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// headerCarrier implements propagation.TextMapCarrier for Envoy headers
type headerCarrier struct {
	headers *corev3.HeaderMap
}

// Get returns the value for the given key from Envoy headers
func (c headerCarrier) Get(key string) string {
	if c.headers == nil {
		return ""
	}
	return getSingleValueHeader(c.headers, key)
}

// Set is not implemented as we only need extraction from incoming headers
func (c headerCarrier) Set(key, value string) {
	// not needed for extraction
}

// Keys returns all header keys
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

// extractTraceContext extracts W3C trace context from Envoy headers
func extractTraceContext(ctx context.Context, headers *corev3.HeaderMap) context.Context {
	carrier := headerCarrier{headers: headers}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// spanAttributes returns common span attributes for MCP requests
func spanAttributes(mcpReq *MCPRequest) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", mcpReq.Method),
	}

	if mcpReq.GetSessionID() != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", mcpReq.GetSessionID()))
	}

	if mcpReq.serverName != "" {
		attrs = append(attrs, attribute.String("mcp.server", mcpReq.serverName))
	}

	if toolName := mcpReq.ToolName(); toolName != "" {
		attrs = append(attrs, attribute.String("mcp.tool", toolName))
	}

	return attrs
}

// recordError records an error on the span with router-specific attributes
func recordError(span trace.Span, err error, statusCode int32) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.Bool("error", true),
		attribute.String("error_source", "ext-proc"),
		attribute.Int("http.status_code", int(statusCode)),
	)
}

// mapCarrier implements propagation.TextMapCarrier for map[string]string
type mapCarrier map[string]string

func (c mapCarrier) Get(key string) string {
	return c[key]
}

func (c mapCarrier) Set(key, value string) {
	c[key] = value
}

func (c mapCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// injectTraceContext injects trace context into a map for downstream propagation
func injectTraceContext(ctx context.Context) map[string]string {
	carrier := make(mapCarrier)
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}
