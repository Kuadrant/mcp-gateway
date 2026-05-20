package broker

import (
	"context"
	"fmt"
	"sync"

	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const brokerTracerName = mcpotel.BrokerTracerName

var brokerComponentAttr = attribute.String("component", "mcp-broker")

func brokerTracer() trace.Tracer {
	return otel.Tracer(brokerTracerName)
}

type requestSpanTracker struct {
	mu    sync.Mutex
	spans map[any]trace.Span
}

func newRequestSpanTracker() *requestSpanTracker {
	return &requestSpanTracker{spans: make(map[any]trace.Span)}
}

func (t *requestSpanTracker) start(ctx context.Context, id any, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	ctx, span := brokerTracer().Start(ctx, spanName, opts...) //nolint:spancheck // span ended by requestSpanTracker.remove caller
	t.mu.Lock()
	t.spans[id] = span
	t.mu.Unlock()
	return ctx, span //nolint:spancheck // span ended by requestSpanTracker.remove caller
}

func (t *requestSpanTracker) remove(id any) (trace.Span, bool) {
	t.mu.Lock()
	span, ok := t.spans[id]
	if ok {
		delete(t.spans, id)
	}
	t.mu.Unlock()
	return span, ok
}

func sessionIDFromContext(ctx context.Context) string {
	if session := server.ClientSessionFromContext(ctx); session != nil {
		return session.SessionID()
	}
	return ""
}

func recordBrokerError(span trace.Span, err error) {
	mcpotel.SpanError(span, err, err.Error())
	span.SetAttributes(
		attribute.String("error.type", fmt.Sprintf("%T", err)),
		attribute.String("error_source", "broker"),
	)
}
