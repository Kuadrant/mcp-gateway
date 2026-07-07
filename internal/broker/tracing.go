package broker

import (
	"fmt"

	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const brokerTracerName = mcpotel.BrokerTracerName

var brokerComponentAttr = attribute.String("component", "mcp-broker")

func brokerTracer() trace.Tracer {
	return otel.Tracer(brokerTracerName)
}

func recordBrokerError(span trace.Span, err error) {
	mcpotel.SpanError(span, err, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error_source", "broker"),
	)
}
