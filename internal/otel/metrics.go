package otel

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	prometheusexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// MetricsProvider wraps the OTel MeterProvider backed by a Prometheus exporter.
type MetricsProvider struct {
	meterProvider *sdkmetric.MeterProvider
	registry      *prometheus.Registry
}

// NewMetricsProvider creates a Prometheus-backed OTel MeterProvider.
// The exporter serves metrics via the returned HTTPHandler — no OTLP endpoint needed.
func NewMetricsProvider(_ context.Context, config *Config) (*MetricsProvider, error) {
	registry := prometheus.NewRegistry()

	exporter, err := prometheusexporter.New(
		prometheusexporter.WithRegisterer(registry),
	)
	if err != nil {
		return nil, err
	}

	res, err := NewResource(context.Background(), config)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(res),
	)

	return &MetricsProvider{
		meterProvider: mp,
		registry:      registry,
	}, nil
}

// MeterProvider returns the underlying MeterProvider for global registration.
func (p *MetricsProvider) MeterProvider() *sdkmetric.MeterProvider {
	return p.meterProvider
}

// HTTPHandler returns an http.Handler that serves Prometheus metrics.
func (p *MetricsProvider) HTTPHandler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// Shutdown flushes and stops the MeterProvider.
func (p *MetricsProvider) Shutdown(ctx context.Context) error {
	return p.meterProvider.Shutdown(ctx)
}
