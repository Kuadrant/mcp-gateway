package otel

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupOTelSDK_Disabled(t *testing.T) {
	os.Clearenv()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// shutdown should work even when nothing was set up
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestSetupOTelSDK_TracesEnabled(t *testing.T) {
	os.Clearenv()
	// only enable traces, not metrics/logs, to avoid shutdown errors from missing collector
	os.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://localhost:4318")
	os.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, err := SetupOTelSDK(context.Background(), "abc123", "false", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify global tracer provider was set
	tp := otel.GetTracerProvider()
	if tp == nil {
		t.Error("expected global TracerProvider to be set")
	}

	// verify we can create a tracer
	tracer := otel.Tracer("test")
	if tracer == nil {
		t.Error("expected to get a tracer")
	}

	// verify we can create spans
	ctx, span := tracer.Start(context.Background(), "test-span")
	if span == nil {
		t.Error("expected to create a span")
	}
	if ctx == nil {
		t.Error("expected context from span")
	}
	span.End()

	// cleanup - trace exporter logs errors but doesn't fail shutdown
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestSetupOTelSDK_MetricsEnabled(t *testing.T) {
	os.Clearenv()
	os.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://localhost:4318")
	os.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify global meter provider was set
	mp := otel.GetMeterProvider()
	if mp == nil {
		t.Error("expected global MeterProvider to be set")
	}

	// verify we can create a meter
	meter := otel.Meter("test")
	if meter == nil {
		t.Error("expected to get a meter")
	}

	// cleanup - metrics exporter may fail but that's expected without a collector
	_ = shutdown(context.Background())
}

func TestSetupOTelSDK_LogsEnabled(t *testing.T) {
	os.Clearenv()
	os.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://localhost:4318")
	os.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	shutdown, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// cleanup - logs exporter may fail but that's expected without a collector
	_ = shutdown(context.Background())
}

func TestSetupOTelSDK_PropagatorSet(t *testing.T) {
	os.Clearenv()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	_, err := SetupOTelSDK(context.Background(), "", "", "v1.0.0", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify propagator was set (even when tracing is disabled)
	propagator := otel.GetTextMapPropagator()
	if propagator == nil {
		t.Error("expected global TextMapPropagator to be set")
	}

	// verify it can extract/inject (basic sanity check)
	carrier := make(map[string]string)
	carrier["traceparent"] = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

	ctx := propagator.Extract(context.Background(), mapCarrier(carrier))
	if ctx == nil {
		t.Error("expected context from Extract")
	}
}

// mapCarrier implements propagation.TextMapCarrier for map[string]string
type mapCarrier map[string]string

func (c mapCarrier) Get(key string) string {
	return c[key]
}

func (c mapCarrier) Set(key, value string) {
	c[key] = value
}

func (c mapCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
