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

// TracingHandler wraps an slog.Handler to add trace_id and span_id from context.
// when tracing is not configured, it passes through to the underlying handler directly.
type TracingHandler struct {
	handler       slog.Handler
	tracingActive bool
}

// NewTracingHandler creates a new TracingHandler wrapping the given handler
func NewTracingHandler(handler slog.Handler, tracingActive bool) *TracingHandler {
	return &TracingHandler{handler: handler, tracingActive: tracingActive}
}

// Enabled reports whether the handler handles records at the given level
func (h *TracingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

// Handle adds trace context to the record and delegates to the wrapped handler.
// skips span context extraction entirely when tracing is not configured.
func (h *TracingHandler) Handle(ctx context.Context, record slog.Record) error {
	if !h.tracingActive {
		return h.handler.Handle(ctx, record)
	}
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
	return &TracingHandler{handler: h.handler.WithAttrs(attrs), tracingActive: h.tracingActive}
}

// WithGroup returns a new handler with the given group name
func (h *TracingHandler) WithGroup(name string) slog.Handler {
	return &TracingHandler{handler: h.handler.WithGroup(name), tracingActive: h.tracingActive}
}

// NewTracingLogger creates a logger with trace context and optional OTLP export
func NewTracingLogger(w io.Writer, opts *slog.HandlerOptions, jsonFormat bool, loggerProvider *sdklog.LoggerProvider) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	var baseHandler slog.Handler
	if jsonFormat {
		baseHandler = slog.NewJSONHandler(w, opts)
	} else {
		baseHandler = slog.NewTextHandler(w, opts)
	}
	tracingActive := loggerProvider != nil
	stdoutHandler := NewTracingHandler(baseHandler, tracingActive)

	if loggerProvider == nil {
		return slog.New(newSessionScrubHandler(stdoutHandler))
	}

	otelHandler := otelslog.NewHandler("mcp-gateway",
		otelslog.WithLoggerProvider(loggerProvider))

	var level slog.Leveler = slog.LevelInfo
	if opts != nil && opts.Level != nil {
		level = opts.Level
	}
	return slog.New(newSessionScrubHandler(slog.NewMultiHandler(stdoutHandler, &levelHandler{Handler: otelHandler, level: level})))
}

// levelHandler applies the configured level to the OTLP bridge, which ignores
// slog.HandlerOptions and would otherwise export debug records at any level.
type levelHandler struct {
	slog.Handler
	level slog.Leveler
}

func (h *levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level.Level() && h.Handler.Enabled(ctx, level)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithGroup(name), level: h.level}
}
