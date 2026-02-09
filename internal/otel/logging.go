package otel

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// TracingHandler wraps an slog.Handler to automatically add trace_id and span_id
// from the context when a span is active
type TracingHandler struct {
	handler slog.Handler
}

// NewTracingHandler creates a new TracingHandler that wraps the given handler
func NewTracingHandler(handler slog.Handler) *TracingHandler {
	return &TracingHandler{handler: handler}
}

// Enabled reports whether the handler handles records at the given level
func (h *TracingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle adds trace context to the record and delegates to the wrapped handler
func (h *TracingHandler) Handle(ctx context.Context, record slog.Record) error {
	// extract span from context and add trace attributes if present
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		spanCtx := span.SpanContext()
		if spanCtx.HasTraceID() {
			record.AddAttrs(slog.String("trace_id", spanCtx.TraceID().String()))
		}
		if spanCtx.HasSpanID() {
			record.AddAttrs(slog.String("span_id", spanCtx.SpanID().String()))
		}
	}
	return h.handler.Handle(ctx, record)
}

// WithAttrs returns a new handler with the given attributes
func (h *TracingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TracingHandler{handler: h.handler.WithAttrs(attrs)}
}

// WithGroup returns a new handler with the given group name
func (h *TracingHandler) WithGroup(name string) slog.Handler {
	return &TracingHandler{handler: h.handler.WithGroup(name)}
}

// MultiHandler sends logs to multiple handlers simultaneously.
// This enables writing to both stdout and OTLP collector.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a handler that sends logs to all provided handlers
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

// Enabled returns true if any handler is enabled for the given level
func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle sends the record to all handlers
func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		// ignore errors from individual handlers to ensure all handlers receive the log
		_ = h.Handle(ctx, r.Clone())
	}
	return nil
}

// WithAttrs returns a new MultiHandler with the given attributes added to all handlers
func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

// WithGroup returns a new MultiHandler with the given group added to all handlers
func (m *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}

// NewTracingLogger creates a logger that writes to stdout with trace context.
// If loggerProvider is non-nil, logs are also exported via OTLP to the collector.
// If w is nil, os.Stdout is used.
func NewTracingLogger(w io.Writer, opts *slog.HandlerOptions, jsonFormat bool, loggerProvider *sdklog.LoggerProvider) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	// stdout handler with trace context
	var baseHandler slog.Handler
	if jsonFormat {
		baseHandler = slog.NewJSONHandler(w, opts)
	} else {
		baseHandler = slog.NewTextHandler(w, opts)
	}
	stdoutHandler := NewTracingHandler(baseHandler)

	// if no LoggerProvider, stdout only
	if loggerProvider == nil {
		return slog.New(stdoutHandler)
	}

	// multi-handler: stdout + OTLP
	otelHandler := otelslog.NewHandler("mcp-gateway",
		otelslog.WithLoggerProvider(loggerProvider))

	return slog.New(NewMultiHandler(stdoutHandler, otelHandler))
}
