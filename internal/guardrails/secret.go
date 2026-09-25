package guardrails

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// SecretTypeNeMo is the Secret type for NeMo Guardrails configuration.
//
//nolint:gosec // not a credential, just a Secret type identifier
const SecretTypeNeMo corev1.SecretType = "guardrails/external/nemo"

// configDataKey is the Secret data key holding the provider config YAML.
const configDataKey = "config.yaml"

// EnsureNeMoConfigData validates a guardrails Secret's type and parses its
// config.yaml into a Config, defaulting failMode to deny.
func EnsureNeMoConfigData(secretType corev1.SecretType, data map[string][]byte) (*Config, error) {
	if secretType != SecretTypeNeMo {
		return nil, fmt.Errorf("unsupported guardrails secret type %q, expected %q", secretType, SecretTypeNeMo)
	}

	raw, ok := data[configDataKey]
	if !ok {
		return nil, fmt.Errorf("missing required key %q", configDataKey)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configDataKey, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", configDataKey, err)
	}
	if cfg.FailMode == "" {
		cfg.FailMode = FailModeDeny
	}

	return cfg, nil
}
