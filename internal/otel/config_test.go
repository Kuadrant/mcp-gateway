package otel

import (
	"os"
	"testing"
)

func TestNewConfig(t *testing.T) {
	tests := []struct {
		name           string
		envVars        map[string]string
		gitSHA         string
		dirty          string
		version        string
		wantService    string
		wantVersion    string
		wantEndpoint   string
		wantInsecure   bool
	}{
		{
			name:         "defaults when no env vars set",
			envVars:      map[string]string{},
			gitSHA:       "abc123",
			dirty:        "true",
			version:      "v1.0.0",
			wantService:  "mcp-gateway",
			wantVersion:  "v1.0.0",
			wantEndpoint: "",
			wantInsecure: false,
		},
		{
			name: "reads endpoint from env",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
			},
			gitSHA:       "abc123",
			dirty:        "",
			version:      "v1.0.0",
			wantService:  "mcp-gateway",
			wantVersion:  "v1.0.0",
			wantEndpoint: "http://collector:4318",
			wantInsecure: false,
		},
		{
			name: "reads all env vars",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
				"OTEL_EXPORTER_OTLP_INSECURE": "true",
				"OTEL_SERVICE_NAME":           "my-service",
				"OTEL_SERVICE_VERSION":        "v2.0.0",
			},
			gitSHA:       "def456",
			dirty:        "false",
			version:      "v1.0.0",
			wantService:  "my-service",
			wantVersion:  "v2.0.0",
			wantEndpoint: "http://collector:4318",
			wantInsecure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// clear and set env vars
			os.Clearenv()
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			cfg := NewConfig(tt.gitSHA, tt.dirty, tt.version)

			if cfg.ServiceName != tt.wantService {
				t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, tt.wantService)
			}
			if cfg.ServiceVersion != tt.wantVersion {
				t.Errorf("ServiceVersion = %q, want %q", cfg.ServiceVersion, tt.wantVersion)
			}
			if cfg.Endpoint != tt.wantEndpoint {
				t.Errorf("Endpoint = %q, want %q", cfg.Endpoint, tt.wantEndpoint)
			}
			if cfg.Insecure != tt.wantInsecure {
				t.Errorf("Insecure = %v, want %v", cfg.Insecure, tt.wantInsecure)
			}
			if cfg.GitSHA != tt.gitSHA {
				t.Errorf("GitSHA = %q, want %q", cfg.GitSHA, tt.gitSHA)
			}
			if cfg.GitDirty != tt.dirty {
				t.Errorf("GitDirty = %q, want %q", cfg.GitDirty, tt.dirty)
			}
		})
	}
}

func TestTracesEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		wantEndpoint string
	}{
		{
			name:         "returns empty when nothing set",
			envVars:      map[string]string{},
			wantEndpoint: "",
		},
		{
			name: "returns base endpoint when only base set",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
			},
			wantEndpoint: "http://collector:4318",
		},
		{
			name: "signal-specific overrides base",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://collector:4318",
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://tempo:4318",
			},
			wantEndpoint: "http://tempo:4318",
		},
		{
			name: "signal-specific works without base",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://tempo:4318",
			},
			wantEndpoint: "http://tempo:4318",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			cfg := NewConfig("", "", "")
			got := cfg.TracesEndpoint()

			if got != tt.wantEndpoint {
				t.Errorf("TracesEndpoint() = %q, want %q", got, tt.wantEndpoint)
			}
		})
	}
}

func TestMetricsEndpoint(t *testing.T) {
	os.Clearenv()
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	os.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://prometheus:4318")

	cfg := NewConfig("", "", "")

	if got := cfg.MetricsEndpoint(); got != "http://prometheus:4318" {
		t.Errorf("MetricsEndpoint() = %q, want %q", got, "http://prometheus:4318")
	}
}

func TestLogsEndpoint(t *testing.T) {
	os.Clearenv()
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	os.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "http://loki:4318")

	cfg := NewConfig("", "", "")

	if got := cfg.LogsEndpoint(); got != "http://loki:4318" {
		t.Errorf("LogsEndpoint() = %q, want %q", got, "http://loki:4318")
	}
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		want    bool
	}{
		{
			name:    "disabled when no endpoint",
			envVars: map[string]string{},
			want:    false,
		},
		{
			name: "enabled when base endpoint set",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
			},
			want: true,
		},
		{
			name: "enabled when only traces endpoint set",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://tempo:4318",
			},
			want: true,
		},
		{
			name: "enabled when only metrics endpoint set",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://prometheus:4318",
			},
			want: true,
		},
		{
			name: "enabled when only logs endpoint set",
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://loki:4318",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			cfg := NewConfig("", "", "")
			if got := cfg.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
