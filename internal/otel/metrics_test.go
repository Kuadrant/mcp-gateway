package otel

import (
	"context"
	"os"
	"testing"
)

func TestNewMetricsProvider_NoEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewMetricsProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error when no endpoint configured, got nil")
	}
}

func TestNewMetricsProvider_InvalidEndpointScheme(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "ftp://invalid:4318",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewMetricsProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for invalid scheme, got nil")
	}
}

func TestNewMetricsProvider_ValidHTTPEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "http://localhost:4318",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewMetricsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	if provider.MeterProvider() == nil {
		t.Error("expected MeterProvider, got nil")
	}

	// cleanup - may fail without collector, that's ok
	_ = provider.Shutdown(context.Background())
}

func TestNewMetricsProvider_ValidGRPCEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "rpc://localhost:4317",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewMetricsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	// cleanup
	_ = provider.Shutdown(context.Background())
}

func TestMetricsProvider_ShutdownNil(t *testing.T) {
	os.Clearenv()
	p := &MetricsProvider{meterProvider: nil}

	err := p.Shutdown(context.Background())
	if err != nil {
		t.Errorf("expected nil error for nil provider, got: %v", err)
	}
}
