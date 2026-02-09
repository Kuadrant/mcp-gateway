package broker

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "mcp-broker"

// tracer returns the tracer for the mcp-broker package
func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// startSpan creates a new span with common broker attributes
func startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// recordError records an error on the span with broker-specific attributes
func recordError(span trace.Span, err error, source string) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(
		attribute.Bool("error", true),
		attribute.String("error_source", source),
	)
}

// TracingMiddleware wraps an HTTP handler with OpenTelemetry tracing
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// extract trace context from incoming headers
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// start span for this request
		ctx, span := tracer().Start(ctx, "mcp-broker.http",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.path", r.URL.Path),
				attribute.String("http.host", r.Host),
			),
		)
		defer span.End()

		// extract session ID from header if present
		if sessionID := r.Header.Get("mcp-session-id"); sessionID != "" {
			span.SetAttributes(attribute.String("mcp.session_id", sessionID))
		}

		// wrap response writer to capture status code
		wrapped := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// serve with updated context
		next.ServeHTTP(wrapped, r.WithContext(ctx))

		// record status code
		span.SetAttributes(attribute.Int("http.status_code", wrapped.statusCode))
		if wrapped.statusCode >= 400 {
			span.SetStatus(codes.Error, http.StatusText(wrapped.statusCode))
		}
	})
}

// statusResponseWriter wraps http.ResponseWriter to capture status code
type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}
