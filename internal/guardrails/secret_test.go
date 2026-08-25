package guardrails

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureNeMoConfigData(t *testing.T) {
	t.Run("rejects a secret whose type isn't the NeMo guardrails type", func(t *testing.T) {
		_, err := EnsureNeMoConfigData("Opaque", map[string][]byte{
			configDataKey: []byte(`
url: https://nemo-guardrails.internal:8080
model: meta/llama-3.1-8b-instruct
`),
		})
		require.Error(t, err)
	})

	t.Run("parses a valid config and defaults failMode to deny", func(t *testing.T) {
		cfg, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: https://nemo-guardrails.internal:8080
configIDs:
  - tool-safety-v1
model: meta/llama-3.1-8b-instruct
`),
		})
		require.NoError(t, err)
		require.Equal(t, "https://nemo-guardrails.internal:8080", cfg.URL)
		require.Equal(t, []string{"tool-safety-v1"}, cfg.ConfigIDs)
		require.Equal(t, "meta/llama-3.1-8b-instruct", cfg.Model)
		require.Equal(t, FailModeDeny, cfg.FailMode)
	})

	t.Run("accepts an explicit failMode: allow", func(t *testing.T) {
		cfg, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: https://nemo-guardrails.internal:8080
model: meta/llama-3.1-8b-instruct
failMode: allow
`),
		})
		require.NoError(t, err)
		require.Equal(t, FailModeAllow, cfg.FailMode)
	})

	t.Run("errors when the config.yaml key is missing", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{})
		require.Error(t, err)
	})

	t.Run("errors when url is missing", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`model: meta/llama-3.1-8b-instruct`),
		})
		require.Error(t, err)
	})

	t.Run("errors when url is not absolute", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: not-a-url
model: meta/llama-3.1-8b-instruct
`),
		})
		require.Error(t, err)
	})

	t.Run("errors when url scheme is not http or https", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: ftp://nemo-guardrails.internal:8080
model: meta/llama-3.1-8b-instruct
`),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "http or https")
	})

	t.Run("accepts an http url", func(t *testing.T) {
		cfg, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: http://nemo-guardrails.internal:8080
model: meta/llama-3.1-8b-instruct
`),
		})
		require.NoError(t, err)
		require.Equal(t, "http://nemo-guardrails.internal:8080", cfg.URL)
	})

	t.Run("errors when model is missing", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`url: https://nemo-guardrails.internal:8080`),
		})
		require.Error(t, err)
	})

	t.Run("errors on an unrecognized failMode", func(t *testing.T) {
		_, err := EnsureNeMoConfigData(SecretTypeNeMo, map[string][]byte{
			configDataKey: []byte(`
url: https://nemo-guardrails.internal:8080
model: meta/llama-3.1-8b-instruct
failMode: retry
`),
		})
		require.Error(t, err)
	})
}
