package otel

import "k8s.io/utils/env"

// Config holds configuration for OpenTelemetry tracing
type Config struct {
	Endpoint       string
	Insecure       bool
	ServiceName    string
	ServiceVersion string
	GitSHA         string
	GitDirty       string
}

// NewConfig creates OTel configuration from environment variables.
func NewConfig(gitSHA, dirty, version string) *Config {
	endpoint := env.GetString("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	insecure, _ := env.GetBool("OTEL_EXPORTER_OTLP_INSECURE", false)

	serviceName := env.GetString("OTEL_SERVICE_NAME", "mcp-gateway")
	serviceVersion := env.GetString("OTEL_SERVICE_VERSION", version)

	return &Config{
		Endpoint:       endpoint,
		Insecure:       insecure,
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		GitSHA:         gitSHA,
		GitDirty:       dirty,
	}
}

// TracesEndpoint returns the endpoint for traces, with signal-specific override support.
func (c *Config) TracesEndpoint() string {
	if endpoint := env.GetString("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", ""); endpoint != "" {
		return endpoint
	}
	return c.Endpoint
}

// MetricsEndpoint returns the endpoint for metrics, with signal-specific override support.
func (c *Config) MetricsEndpoint() string {
	if endpoint := env.GetString("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", ""); endpoint != "" {
		return endpoint
	}
	return c.Endpoint
}

// LogsEndpoint returns the endpoint for logs, with signal-specific override support.
func (c *Config) LogsEndpoint() string {
	if endpoint := env.GetString("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", ""); endpoint != "" {
		return endpoint
	}
	return c.Endpoint
}

// TracesEnabled returns true if tracing is enabled (an endpoint is configured)
func (c *Config) TracesEnabled() bool {
	return c.TracesEndpoint() != ""
}

// MetricsEnabled returns true if metrics export is enabled (an endpoint is configured)
func (c *Config) MetricsEnabled() bool {
	return c.MetricsEndpoint() != ""
}

// LogsEnabled returns true if logs export is enabled (an endpoint is configured)
func (c *Config) LogsEnabled() bool {
	return c.LogsEndpoint() != ""
}

// Enabled returns true if any telemetry signal is enabled
func (c *Config) Enabled() bool {
	return c.TracesEnabled() || c.MetricsEnabled() || c.LogsEnabled()
}
