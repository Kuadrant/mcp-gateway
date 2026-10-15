package otel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// BrokerTracerName is the OpenTelemetry tracer name for the broker component.
const BrokerTracerName = "mcp-broker"

// SpanError records an error on the span and sets its status to error. the
// exception event is written directly rather than via RecordError so its
// message and the status can be scrubbed of sdk-embedded session IDs.
func SpanError(span trace.Span, err error, msg string) {
	if err != nil {
		span.AddEvent(semconv.ExceptionEventName, trace.WithAttributes(
			semconv.ExceptionType(fmt.Sprintf("%T", err)),
			semconv.ExceptionMessage(scrubSessionIDs(err.Error())),
		))
	}
	span.SetStatus(codes.Error, scrubSessionIDs(msg))
}

// SetupOTelSDK initializes the OpenTelemetry SDK with tracing, logs, and metrics support.
// metricsHandler serves Prometheus metrics and must be mounted by the caller on a dedicated port.
func SetupOTelSDK(ctx context.Context, gitSHA, dirty, version string, logger *slog.Logger) (shutdown func(context.Context) error, loggerProvider *sdklog.LoggerProvider, metricsHandler http.Handler, err error) {
	var shutdownFuncs []func(context.Context) error

	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		return err
	}

	config := NewConfig(gitSHA, dirty, version)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if config.TracesEnabled() {
		traceProvider, err := NewProvider(ctx, config)
		if err != nil {
			return shutdown, nil, nil, err
		}
		shutdownFuncs = append(shutdownFuncs, traceProvider.Shutdown)
		otel.SetTracerProvider(traceProvider.TracerProvider())
		logger.Info("OpenTelemetry tracing enabled", "endpoint", config.TracesEndpoint())
	}

	if config.LogsEnabled() {
		logsProvider, err := NewLogsProvider(ctx, config)
		if err != nil {
			return shutdown, nil, nil, err
		}
		shutdownFuncs = append(shutdownFuncs, logsProvider.Shutdown)
		loggerProvider = logsProvider.LoggerProvider()
		logger.Info("OpenTelemetry logs enabled", "endpoint", config.LogsEndpoint())
	}

	metricsProvider, err := NewMetricsProvider(ctx, config)
	if err != nil {
		return shutdown, nil, nil, err
	}
	shutdownFuncs = append(shutdownFuncs, metricsProvider.Shutdown)
	otel.SetMeterProvider(metricsProvider.MeterProvider())
	metricsHandler = metricsProvider.HTTPHandler()
	logger.Info("OpenTelemetry metrics enabled (Prometheus)")

	return shutdown, loggerProvider, metricsHandler, nil
}
