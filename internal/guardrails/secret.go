// Package guardrails validates and parses the guardrails Secret referenced by
// an MCPGatewayExtension, and checks tools/call requests and responses
// against an external guardrails server.
package guardrails

import (
	"fmt"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// SecretTypeNeMo is the Secret type for NeMo Guardrails configuration.
//
//nolint:gosec // not a credential, just a Secret type identifier
const SecretTypeNeMo corev1.SecretType = "guardrails/external/nemo"

// Fail modes applied when the guardrails server is unreachable or errors.
const (
	FailModeDeny  = "deny"
	FailModeAllow = "allow"
)

// configDataKey is the Secret data key holding the provider config YAML.
const configDataKey = "config.yaml"

// EnsureNeMoConfigData validates a guardrails Secret's type and parses its
// config.yaml into a GuardrailsConfig, defaulting failMode to deny.
func EnsureNeMoConfigData(secretType corev1.SecretType, data map[string][]byte) (*config.GuardrailsConfig, error) {
	if secretType != SecretTypeNeMo {
		return nil, fmt.Errorf("unsupported guardrails secret type %q, expected %q", secretType, SecretTypeNeMo)
	}

	raw, ok := data[configDataKey]
	if !ok {
		return nil, fmt.Errorf("missing required key %q", configDataKey)
	}

	cfg := &config.GuardrailsConfig{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configDataKey, err)
	}

	if cfg.URL == "" {
		return nil, fmt.Errorf("%s: url is required", configDataKey)
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%s: url %q is not a valid absolute URL", configDataKey, cfg.URL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%s: url scheme must be http or https, got %q", configDataKey, parsed.Scheme)
	}

	if cfg.Model == "" {
		return nil, fmt.Errorf("%s: model is required", configDataKey)
	}

	switch cfg.FailMode {
	case "":
		cfg.FailMode = FailModeDeny
	case FailModeDeny, FailModeAllow:
	default:
		return nil, fmt.Errorf("%s: failMode must be %q or %q, got %q", configDataKey, FailModeDeny, FailModeAllow, cfg.FailMode)
	}

	return cfg, nil
}
