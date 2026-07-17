package routing

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "mcp-router"

var componentAttr = attribute.String("component", "mcp-router")

func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}
