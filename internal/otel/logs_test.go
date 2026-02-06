package otel

import (
	"context"
	"os"
	"testing"
)

func TestNewLogsProvider_NoEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewLogsProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error when no endpoint configured, got nil")
	}
}

func TestNewLogsProvider_InvalidEndpointScheme(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "ftp://invalid:4318",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewLogsProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for invalid scheme, got nil")
	}
}

func TestNewLogsProvider_ValidHTTPEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "http://localhost:4318",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewLogsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	if provider.LoggerProvider() == nil {
		t.Error("expected LoggerProvider, got nil")
	}

	// cleanup - may fail without collector, that's ok
	_ = provider.Shutdown(context.Background())
}

func TestNewLogsProvider_ValidGRPCEndpoint(t *testing.T) {
	os.Clearenv()
	cfg := &Config{
		Endpoint:       "rpc://localhost:4317",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewLogsProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	// cleanup
	_ = provider.Shutdown(context.Background())
}

func TestLogsProvider_ShutdownNil(t *testing.T) {
	os.Clearenv()
	p := &LogsProvider{loggerProvider: nil}

	err := p.Shutdown(context.Background())
	if err != nil {
		t.Errorf("expected nil error for nil provider, got: %v", err)
	}
}
