package controller

import (
	"context"
	"testing"

	"github.com/Kuadrant/mcp-gateway/pkg/config"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConfigMapWriter_WriteAggregatedConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("creates new secret when not exists", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		writer := NewSecretWriter(k8sClient, scheme)

		brokerConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{
				{
					Name:       "test-server",
					URL:        "http://test.local/mcp",
					ToolPrefix: "test_",
				},
			},
		}

		err := writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)

		// verify the secret was created
		secret := &corev1.Secret{}
		err = k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: "mcp-system",
			Name:      "test-config",
		}, secret)
		require.NoError(t, err)

		// check labels
		require.Equal(t, "mcp-gateway", secret.Labels["app"])
		require.Equal(t, "true", secret.Labels["mcp.kagenti.com/aggregated"])

		// check that config.yaml exists in StringData
		require.Contains(t, secret.StringData, "config.yaml")
		require.Contains(t, secret.StringData["config.yaml"], "test-server")
		require.Contains(t, secret.StringData["config.yaml"], "http://test.local/mcp")
	})

	t.Run("updates existing secret", func(t *testing.T) {
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-config",
				Namespace: "mcp-system",
				Labels: map[string]string{
					"app": "mcp-gateway",
				},
			},
			StringData: map[string]string{
				"config.yaml": "old: data",
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSecret).
			Build()

		writer := NewSecretWriter(k8sClient, scheme)

		brokerConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{
				{
					Name:       "new-server",
					URL:        "http://new.local/mcp",
					ToolPrefix: "new_",
				},
			},
		}

		err := writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)

		// verify the secret was updated
		secret := &corev1.Secret{}
		err = k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: "mcp-system",
			Name:      "test-config",
		}, secret)
		require.NoError(t, err)

		// check that config.yaml contains new data
		require.Contains(t, secret.StringData, "config.yaml")
		require.Contains(t, secret.StringData["config.yaml"], "new-server")
		require.Contains(t, secret.StringData["config.yaml"], "http://new.local/mcp")
	})

	t.Run("no update when content unchanged", func(t *testing.T) {
		brokerConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{
				{
					Name:       "test-server",
					URL:        "http://test.local/mcp",
					ToolPrefix: "test_",
				},
			},
		}

		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()
		writer := NewSecretWriter(k8sClient, scheme)

		// write once
		err := writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)

		// write same config again - should succeed without error
		err = writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)
	})

	t.Run("handles empty config", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		writer := NewSecretWriter(k8sClient, scheme)

		brokerConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{},
		}

		err := writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)

		secret := &corev1.Secret{}
		err = k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: "mcp-system",
			Name:      "test-config",
		}, secret)
		require.NoError(t, err)
		require.Contains(t, secret.StringData, "config.yaml")
	})

	t.Run("handles config with virtual servers", func(t *testing.T) {
		k8sClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		writer := NewSecretWriter(k8sClient, scheme)

		brokerConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{
				{
					Name:       "server1",
					URL:        "http://server1.local/mcp",
					ToolPrefix: "s1_",
				},
			},
			VirtualServers: []config.VirtualServerConfig{
				{
					Name:  "virtual1",
					Tools: []string{"s1_tool1", "s1_tool2"},
				},
			},
		}

		err := writer.WriteAggregatedConfig(context.Background(), "mcp-system", "test-config", brokerConfig)
		require.NoError(t, err)

		secret := &corev1.Secret{}
		err = k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: "mcp-system",
			Name:      "test-config",
		}, secret)
		require.NoError(t, err)

		configYAML := secret.StringData["config.yaml"]
		require.Contains(t, configYAML, "virtual1")
		require.Contains(t, configYAML, "s1_tool1")
	})
}

func TestNewSecretWriter(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	writer := NewSecretWriter(k8sClient, scheme)

	require.NotNil(t, writer)
	require.Equal(t, k8sClient, writer.Client)
	require.Equal(t, scheme, writer.Scheme)
}
