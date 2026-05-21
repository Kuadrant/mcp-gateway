package broker

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setupTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	return exporter
}

func findAttribute(attrs []attribute.KeyValue, key string) (attribute.KeyValue, bool) {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr, true
		}
	}
	return attribute.KeyValue{}, false
}

func TestBrokerTracer(t *testing.T) {
	tr := brokerTracer()
	require.NotNil(t, tr)
}

func TestRecordBrokerError(t *testing.T) {
	exporter := setupTestTracer(t)
	_, span := brokerTracer().Start(context.Background(), "test-span")
	testErr := fmt.Errorf("test broker error")
	recordBrokerError(span, testErr)
	span.End()
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]
	require.Equal(t, "test-span", s.Name)
	require.NotEmpty(t, s.Events)
	attr, found := findAttribute(s.Attributes, "error_source")
	require.True(t, found, "expected error_source attribute")
	require.Equal(t, "broker", attr.Value.AsString())
}

func TestRequestSpanTracker(t *testing.T) {
	exporter := setupTestTracer(t)
	tracker := newRequestSpanTracker()
	tracker.start(context.Background(), "req-1", "mcp-broker.handle-request",
		attribute.String("mcp.method", "tools/list"),
	)
	span, ok := tracker.remove("req-1")
	require.True(t, ok)
	require.NotNil(t, span)
	span.End()
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "mcp-broker.handle-request", spans[0].Name)
	attr, found := findAttribute(spans[0].Attributes, "mcp.method")
	require.True(t, found)
	require.Equal(t, "tools/list", attr.Value.AsString())
}

func TestRequestSpanTrackerEndMissing(t *testing.T) {
	_ = setupTestTracer(t)
	tracker := newRequestSpanTracker()
	span, ok := tracker.remove("nonexistent")
	require.False(t, ok)
	require.Nil(t, span)
}
