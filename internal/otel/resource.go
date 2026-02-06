package otel

import (
	"context"
	"runtime"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// NewResource creates a shared OpenTelemetry resource for all signals.
// The resource represents the service identity and should be consistent across
// all telemetry signals to enable correlation in observability backends.
func NewResource(ctx context.Context, config *Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(config.ServiceName),
		semconv.ServiceVersion(config.ServiceVersion),
	}

	if config.GitSHA != "" {
		attrs = append(attrs, attribute.String("vcs.revision", config.GitSHA))
	}
	if config.GitDirty != "" {
		attrs = append(attrs, attribute.String("vcs.dirty", config.GitDirty))
	}

	attrs = append(attrs, attribute.String("build.go.version", runtime.Version()))

	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
}
