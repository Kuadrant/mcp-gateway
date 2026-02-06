package otel

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Provider wraps the OpenTelemetry TracerProvider and manages its lifecycle
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
}

// NewProvider creates a new OpenTelemetry trace provider enabling distributed
// tracing with automatic span creation and log correlation via trace_id
func NewProvider(ctx context.Context, config *Config) (*Provider, error) {
	endpoint := config.TracesEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("traces disabled: no endpoint configured")
	}

	res, err := NewResource(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	exporter, err := newTraceExporter(ctx, endpoint, config.Insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	return &Provider{
		tracerProvider: tracerProvider,
	}, nil
}

// newTraceExporter creates an OTLP exporter based on endpoint URL scheme:
// rpc:// uses gRPC, http/https use HTTP protocol
func newTraceExporter(ctx context.Context, endpoint string, insecure bool) (sdktrace.SpanExporter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	var client otlptrace.Client

	switch u.Scheme {
	case "rpc":
		opts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(u.Host),
		}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		client = otlptracegrpc.NewClient(opts...)

	case "http", "https":
		opts := []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(u.Host),
		}
		if path := u.Path; path != "" {
			opts = append(opts, otlptracehttp.WithURLPath(path))
		}
		if insecure || u.Scheme == "http" {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		client = otlptracehttp.NewClient(opts...)

	default:
		return nil, fmt.Errorf("unsupported endpoint scheme: %s (use 'rpc', 'http', or 'https')", u.Scheme)
	}

	return otlptrace.New(ctx, client)
}

// TracerProvider returns the underlying TracerProvider
func (p *Provider) TracerProvider() *sdktrace.TracerProvider {
	return p.tracerProvider
}

// Shutdown gracefully shuts down the trace provider, flushing pending spans
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.tracerProvider == nil {
		return nil
	}
	return p.tracerProvider.Shutdown(ctx)
}
