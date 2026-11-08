package otel

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
)

func TestSpanError_ScrubsSessionIDs(t *testing.T) {
	const backendSID = "7f3c)9a1e-backend"
	sdkErr := fmt.Errorf("calling %q: failed to reconnect (session ID: %v): %w", "tools/list", backendSID, errors.New("EOF"))
	err := fmt.Errorf("list tools: %w", sdkErr)

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	SpanError(span, err, err.Error())
	span.End()

	spans := rec.Ended()
	require.Len(t, spans, 1)
	s := spans[0]
	want := `list tools: calling "tools/list": failed to reconnect (session ID: ` + internaljwt.LogSafeSessionID(backendSID) + `): EOF`

	require.Equal(t, codes.Error, s.Status().Code)
	require.Equal(t, want, s.Status().Description)

	require.Len(t, s.Events(), 1)
	ev := s.Events()[0]
	require.Equal(t, semconv.ExceptionEventName, ev.Name)
	attrs := map[string]string{}
	for _, a := range ev.Attributes {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	require.Equal(t, want, attrs[string(semconv.ExceptionMessageKey)])
	require.Equal(t, fmt.Sprintf("%T", err), attrs[string(semconv.ExceptionTypeKey)])
	for k, v := range attrs {
		require.NotContains(t, v, backendSID, k)
	}
	require.NotContains(t, s.Status().Description, backendSID)
}

func TestSpanError_StaticMessage(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	SpanError(span, errors.New("tool not found: x"), "tool not found")
	span.End()

	s := rec.Ended()[0]
	require.Equal(t, "tool not found", s.Status().Description)
	require.Len(t, s.Events(), 1)
}
