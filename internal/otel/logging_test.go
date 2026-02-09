package otel

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTracingHandler_NoSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := NewTracingLogger(&buf, nil, true, nil)

	logger.InfoContext(context.Background(), "test message", "key", "value")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("expected log to contain 'test message', got: %s", output)
	}
	if strings.Contains(output, "trace_id") {
		t.Errorf("expected no trace_id when no span active, got: %s", output)
	}
	if strings.Contains(output, "span_id") {
		t.Errorf("expected no span_id when no span active, got: %s", output)
	}
}

func TestTracingHandler_WithSpan(t *testing.T) {
	// create a trace provider
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTracerProvider(tp)

	var buf bytes.Buffer
	logger := NewTracingLogger(&buf, nil, true, nil)

	// create a span
	ctx, span := otel.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()

	logger.InfoContext(ctx, "test message with trace")

	output := buf.String()
	if !strings.Contains(output, "test message with trace") {
		t.Errorf("expected log to contain message, got: %s", output)
	}
	if !strings.Contains(output, "trace_id") {
		t.Errorf("expected trace_id when span active, got: %s", output)
	}
	if !strings.Contains(output, "span_id") {
		t.Errorf("expected span_id when span active, got: %s", output)
	}
}

func TestTracingHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := NewTracingHandler(baseHandler)

	// test WithAttrs returns a new TracingHandler
	newHandler := handler.WithAttrs([]slog.Attr{slog.String("component", "test")})
	if _, ok := newHandler.(*TracingHandler); !ok {
		t.Errorf("WithAttrs should return a TracingHandler")
	}
}

func TestTracingHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := NewTracingHandler(baseHandler)

	// test WithGroup returns a new TracingHandler
	newHandler := handler.WithGroup("mygroup")
	if _, ok := newHandler.(*TracingHandler); !ok {
		t.Errorf("WithGroup should return a TracingHandler")
	}
}

func TestTracingHandler_Enabled(t *testing.T) {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelWarn}
	baseHandler := slog.NewJSONHandler(&buf, opts)
	handler := NewTracingHandler(baseHandler)

	ctx := context.Background()

	if handler.Enabled(ctx, slog.LevelInfo) {
		t.Error("expected Info level to be disabled when handler is set to Warn")
	}
	if !handler.Enabled(ctx, slog.LevelWarn) {
		t.Error("expected Warn level to be enabled")
	}
	if !handler.Enabled(ctx, slog.LevelError) {
		t.Error("expected Error level to be enabled")
	}
}

func TestTracingHandler_NonRecordingSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := NewTracingLogger(&buf, nil, true, nil)

	// create a non-recording span context
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:  trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	logger.InfoContext(ctx, "test with non-recording span")

	output := buf.String()
	// non-recording spans should not add trace context
	if strings.Contains(output, "trace_id") {
		t.Errorf("expected no trace_id for non-recording span, got: %s", output)
	}
}
