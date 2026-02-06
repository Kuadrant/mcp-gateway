package otel

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// LogsProvider wraps the OpenTelemetry LoggerProvider and manages its lifecycle
type LogsProvider struct {
	loggerProvider *sdklog.LoggerProvider
}

// NewLogsProvider creates a new OpenTelemetry logs provider
func NewLogsProvider(ctx context.Context, config *Config) (*LogsProvider, error) {
	endpoint := config.LogsEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("logs disabled: no endpoint configured")
	}

	res, err := NewResource(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	exporter, err := newLogExporter(ctx, endpoint, config.Insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP log exporter: %w", err)
	}

	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	return &LogsProvider{
		loggerProvider: loggerProvider,
	}, nil
}

// newLogExporter creates an OTLP log exporter based on endpoint URL scheme
func newLogExporter(ctx context.Context, endpoint string, insecure bool) (sdklog.Exporter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	switch u.Scheme {
	case "rpc":
		opts := []otlploggrpc.Option{
			otlploggrpc.WithEndpoint(u.Host),
		}
		if insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		return otlploggrpc.New(ctx, opts...)

	case "http", "https":
		opts := []otlploghttp.Option{
			otlploghttp.WithEndpoint(u.Host),
		}
		if path := u.Path; path != "" {
			opts = append(opts, otlploghttp.WithURLPath(path))
		}
		if insecure || u.Scheme == "http" {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		return otlploghttp.New(ctx, opts...)

	default:
		return nil, fmt.Errorf("unsupported endpoint scheme: %s (use 'rpc', 'http', or 'https')", u.Scheme)
	}
}

// LoggerProvider returns the underlying LoggerProvider
func (p *LogsProvider) LoggerProvider() *sdklog.LoggerProvider {
	return p.loggerProvider
}

// Shutdown gracefully shuts down the logger provider, flushing pending logs
func (p *LogsProvider) Shutdown(ctx context.Context) error {
	if p.loggerProvider == nil {
		return nil
	}
	return p.loggerProvider.Shutdown(ctx)
}
