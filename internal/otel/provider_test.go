package otel

import (
	"context"
	"testing"
)

func TestNewProvider_NoEndpoint(t *testing.T) {
	cfg := &Config{
		Endpoint:       "",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error when no endpoint configured, got nil")
	}
}

func TestNewProvider_InvalidEndpointScheme(t *testing.T) {
	cfg := &Config{
		Endpoint:       "ftp://invalid:4318",
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	_, err := NewProvider(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for invalid scheme, got nil")
	}
}

func TestNewProvider_ValidHTTPEndpoint(t *testing.T) {
	cfg := &Config{
		Endpoint:       "http://localhost:4318",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	if provider.TracerProvider() == nil {
		t.Error("expected TracerProvider, got nil")
	}

	// cleanup
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestNewProvider_ValidGRPCEndpoint(t *testing.T) {
	cfg := &Config{
		Endpoint:       "rpc://localhost:4317",
		Insecure:       true,
		ServiceName:    "test-service",
		ServiceVersion: "v1.0.0",
	}

	provider, err := NewProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("expected provider, got nil")
	}

	// cleanup
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestProvider_ShutdownNil(t *testing.T) {
	p := &Provider{tracerProvider: nil}

	err := p.Shutdown(context.Background())
	if err != nil {
		t.Errorf("expected nil error for nil provider, got: %v", err)
	}
}
