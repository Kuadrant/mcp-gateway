package otel

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// SetupOTelSDK initializes the OpenTelemetry SDK with tracing, metrics, and logs support.
// Returns a shutdown function and an optional LoggerProvider for slog integration.
//
// The SDK is configured via environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT: Base OTLP endpoint for all signals
//   - OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: Override endpoint for traces
//   - OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: Override endpoint for metrics
//   - OTEL_EXPORTER_OTLP_LOGS_ENDPOINT: Override endpoint for logs
//   - OTEL_EXPORTER_OTLP_INSECURE: Disable TLS (default: false)
//   - OTEL_SERVICE_NAME: Service name in telemetry (default: mcp-gateway)
//   - OTEL_SERVICE_VERSION: Service version (default: build version)
//
// If no endpoint is configured for a signal, that signal is disabled.
// The returned LoggerProvider is nil if logs are not enabled.
func SetupOTelSDK(ctx context.Context, gitSHA, dirty, version string, logger *slog.Logger) (shutdown func(context.Context) error, loggerProvider *sdklog.LoggerProvider, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown combines all cleanup functions into one
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	// create config from environment
	config := NewConfig(gitSHA, dirty, version)

	// set up propagator for distributed tracing (W3C Trace Context)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// set up trace provider if enabled
	if config.TracesEnabled() {
		traceProvider, err := NewProvider(ctx, config)
		if err != nil {
			return shutdown, nil, err
		}
		shutdownFuncs = append(shutdownFuncs, traceProvider.Shutdown)
		otel.SetTracerProvider(traceProvider.TracerProvider())
		logger.Info("OpenTelemetry tracing enabled", "endpoint", config.TracesEndpoint())
	}

	// set up meter provider for metrics if enabled
	if config.MetricsEnabled() {
		metricsProvider, err := NewMetricsProvider(ctx, config)
		if err != nil {
			return shutdown, nil, err
		}
		shutdownFuncs = append(shutdownFuncs, metricsProvider.Shutdown)
		otel.SetMeterProvider(metricsProvider.MeterProvider())
		logger.Info("OpenTelemetry metrics enabled", "endpoint", config.MetricsEndpoint())
	}

	// set up logger provider for logs if enabled
	if config.LogsEnabled() {
		logsProvider, err := NewLogsProvider(ctx, config)
		if err != nil {
			return shutdown, nil, err
		}
		shutdownFuncs = append(shutdownFuncs, logsProvider.Shutdown)
		loggerProvider = logsProvider.LoggerProvider()
		logger.Info("OpenTelemetry logs enabled", "endpoint", config.LogsEndpoint())
	}

	return shutdown, loggerProvider, nil
}
