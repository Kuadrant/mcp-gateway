package broker

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// connectInMemory wires a client session to the broker's MCP server over an
// in-memory transport.
func connectInMemory(t *testing.T, b *mcpBrokerImpl) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	go func() { _, _ = b.MCPServer().Connect(ctx, st, nil) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// the tracing middleware must create one span per request, end it when the
// request finishes, and pass the span context downstream so handler spans
// nest under it.
func TestTracingMiddleware_SpanPerRequestWithNesting(t *testing.T) {
	exporter := setupTestTracer(t)
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	cs := connectInMemory(t, b)

	_, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	var handle, filter tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "mcp-broker.handle-request":
			if attr, ok := findAttribute(s.Attributes, "mcp.method"); ok && attr.Value.AsString() == "tools/list" {
				handle = s
			}
		case "mcp-broker.tools-list":
			filter = s
		}
	}
	require.NotEmpty(t, handle.Name, "expected a handle-request span for tools/list")
	require.NotEmpty(t, filter.Name, "expected the FilterTools span")

	require.Equal(t, handle.SpanContext.TraceID(), filter.SpanContext.TraceID(),
		"handler spans must share the request trace")
	require.Equal(t, handle.SpanContext.SpanID(), filter.Parent.SpanID(),
		"FilterTools span must nest under the request span")
}

func TestTracingMiddleware_ErrorRecordedOnSpan(t *testing.T) {
	exporter := setupTestTracer(t)
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	cs := connectInMemory(t, b)

	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
	require.Error(t, err)

	var found bool
	for _, s := range exporter.GetSpans() {
		if s.Name != "mcp-broker.handle-request" {
			continue
		}
		if attr, ok := findAttribute(s.Attributes, "mcp.method"); ok && attr.Value.AsString() == "tools/call" {
			if attr, ok := findAttribute(s.Attributes, "error_source"); ok && attr.Value.AsString() == "broker" {
				found = true
			}
		}
	}
	require.True(t, found, "expected the tools/call span to record the broker error")
}
