package otel

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestSetupOTelSDK_Disabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, loggerProvider, metricsHandler, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loggerProvider != nil {
		t.Error("expected loggerProvider to be nil when disabled")
	}

	if metricsHandler == nil {
		t.Error("expected metricsHandler to be non-nil")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestSetupOTelSDK_TracesEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, _, _, err := SetupOTelSDK(context.Background(), "abc123", "false", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tp := otel.GetTracerProvider()
	if tp == nil {
		t.Error("expected global TracerProvider to be set")
	}

	tracer := otel.Tracer("test")
	if tracer == nil {
		t.Error("expected to get a tracer")
	}

	ctx, span := tracer.Start(context.Background(), "test-span")
	if span == nil {
		t.Error("expected to create a span")
	}
	if ctx == nil {
		t.Error("expected context from span")
	}
	span.End()

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestSetupOTelSDK_LogsEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, loggerProvider, _, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if loggerProvider == nil {
		t.Error("expected loggerProvider to be non-nil when logs are enabled")
	}

	_ = shutdown(context.Background())
}

func TestSetupOTelSDK_PropagatorSet(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	_, _, _, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	propagator := otel.GetTextMapPropagator()
	if propagator == nil {
		t.Error("expected global TextMapPropagator to be set")
	}

	carrier := make(testCarrier)
	carrier["traceparent"] = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	ctx := propagator.Extract(context.Background(), carrier)
	if ctx == nil {
		t.Error("expected context from Extract")
	}
}

func TestSetupOTelSDK_RegistersMeterProvider(t *testing.T) {
	// reset to noop so we can detect the change
	otel.SetMeterProvider(noop.NewMeterProvider())

	ctx := context.Background()
	shutdown, _, metricsHandler, err := SetupOTelSDK(ctx, "sha", "", "v0.0.1", noopLogger())
	require.NoError(t, err)
	require.NotNil(t, metricsHandler)
	defer shutdown(ctx) //nolint:errcheck

	// the global meter provider must no longer be the noop one we set above
	mp := otel.GetMeterProvider()
	_, isNoop := mp.(noop.MeterProvider)
	require.False(t, isNoop, "expected a real MeterProvider, got noop")
}

func noopLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

type testCarrier map[string]string

func (c testCarrier) Get(key string) string {
	return c[key]
}

func (c testCarrier) Set(key, value string) {
	c[key] = value
}

func (c testCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
