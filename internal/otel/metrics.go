package otel

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MetricsProvider wraps the OpenTelemetry MeterProvider and manages its lifecycle
type MetricsProvider struct {
	meterProvider *sdkmetric.MeterProvider
}

// NewMetricsProvider creates a new OpenTelemetry metrics provider
func NewMetricsProvider(ctx context.Context, config *Config) (*MetricsProvider, error) {
	endpoint := config.MetricsEndpoint()
	if endpoint == "" {
		return nil, fmt.Errorf("metrics disabled: no endpoint configured")
	}

	res, err := NewResource(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	exporter, err := newMetricExporter(ctx, endpoint, config.Insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(10*time.Second),
		)),
	)

	return &MetricsProvider{
		meterProvider: meterProvider,
	}, nil
}

// newMetricExporter creates an OTLP metric exporter based on endpoint URL scheme
func newMetricExporter(ctx context.Context, endpoint string, insecure bool) (sdkmetric.Exporter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}

	switch u.Scheme {
	case "rpc":
		opts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(u.Host),
		}
		if insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		return otlpmetricgrpc.New(ctx, opts...)

	case "http", "https":
		opts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(u.Host),
		}
		if path := u.Path; path != "" {
			opts = append(opts, otlpmetrichttp.WithURLPath(path))
		}
		if insecure || u.Scheme == "http" {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		return otlpmetrichttp.New(ctx, opts...)

	default:
		return nil, fmt.Errorf("unsupported endpoint scheme: %s (use 'rpc', 'http', or 'https')", u.Scheme)
	}
}

// MeterProvider returns the underlying MeterProvider
func (p *MetricsProvider) MeterProvider() *sdkmetric.MeterProvider {
	return p.meterProvider
}

// Shutdown gracefully shuts down the meter provider, flushing pending metrics
func (p *MetricsProvider) Shutdown(ctx context.Context) error {
	if p.meterProvider == nil {
		return nil
	}
	return p.meterProvider.Shutdown(ctx)
}
