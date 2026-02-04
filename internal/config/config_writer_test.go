package config

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func newTestSecretReaderWriter(t *testing.T) *SecretReaderWriter {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()
	logger := logr.Discard()
	return &SecretReaderWriter{
		Client: fakeClient,
		Scheme: scheme,
		Logger: &logger,
	}
}

func TestUpsertMCPServer(t *testing.T) {
	testCases := []struct {
		name           string
		serversToAdd   []MCPServer
		expectedCount  int
		expectedServer MCPServer // checks first server expectedCount == 1
	}{
		{
			name: "creates secret if not exists",
			serversToAdd: []MCPServer{
				{Name: "test-server", URL: "http://test.local:8080/mcp", ToolPrefix: "test_", Enabled: true},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://test.local:8080/mcp", ToolPrefix: "test_"},
		},
		{
			name: "updates existing server",
			serversToAdd: []MCPServer{
				{Name: "test-server", URL: "http://old.local:8080/mcp", ToolPrefix: "old_", Enabled: true},
				{Name: "test-server", URL: "http://new.local:8080/mcp", ToolPrefix: "new_", Enabled: true},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://new.local:8080/mcp", ToolPrefix: "new_"},
		},
		{
			name: "appends new server",
			serversToAdd: []MCPServer{
				{Name: "server1", URL: "http://s1.local/mcp", Enabled: true},
				{Name: "server2", URL: "http://s2.local/mcp", Enabled: true},
			},
			expectedCount: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srw := newTestSecretReaderWriter(t)
			ctx := context.Background()
			namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

			for i, server := range tc.serversToAdd {
				if err := srw.UpsertMCPServer(ctx, server, namespaceName); err != nil {
					t.Fatalf("UpsertMCPServer[%d] failed: %v", i, err)
				}
			}

			secret := &corev1.Secret{}
			if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
				t.Fatalf("failed to get secret: %v", err)
			}

			configData := secret.StringData[configFileName]
			if configData == "" {
				configData = string(secret.Data[configFileName])
			}
			var config BrokerConfig
			if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
				t.Fatalf("failed to unmarshal config: %v", err)
			}

			if len(config.Servers) != tc.expectedCount {
				t.Fatalf("expected %d server(s), got %d", tc.expectedCount, len(config.Servers))
			}

			if tc.expectedCount == 1 && tc.expectedServer.Name != "" {
				if config.Servers[0].Name != tc.expectedServer.Name {
					t.Errorf("expected name %q, got %q", tc.expectedServer.Name, config.Servers[0].Name)
				}
				if config.Servers[0].URL != tc.expectedServer.URL {
					t.Errorf("expected URL %q, got %q", tc.expectedServer.URL, config.Servers[0].URL)
				}
				if config.Servers[0].ToolPrefix != tc.expectedServer.ToolPrefix {
					t.Errorf("expected ToolPrefix %q, got %q", tc.expectedServer.ToolPrefix, config.Servers[0].ToolPrefix)
				}
			}
		})
	}
}

func TestRemoveMCPServer_RemovesFromConfig(t *testing.T) {
	srw := newTestSecretReaderWriter(t)
	ctx := context.Background()
	namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

	// insert two servers
	server1 := MCPServer{Name: "server1", URL: "http://s1.local/mcp", Enabled: true}
	server2 := MCPServer{Name: "server2", URL: "http://s2.local/mcp", Enabled: true}
	if err := srw.UpsertMCPServer(ctx, server1, namespaceName); err != nil {
		t.Fatalf("UpsertMCPServer server1 failed: %v", err)
	}
	if err := srw.UpsertMCPServer(ctx, server2, namespaceName); err != nil {
		t.Fatalf("UpsertMCPServer server2 failed: %v", err)
	}

	// remove server1
	if err := srw.RemoveMCPServer(ctx, "server1"); err != nil {
		t.Fatalf("RemoveMCPServer failed: %v", err)
	}

	// verify only server2 remains
	secret := &corev1.Secret{}
	if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}

	configData := secret.StringData[configFileName]
	if configData == "" {
		configData = string(secret.Data[configFileName])
	}
	var config BrokerConfig
	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	if len(config.Servers) != 1 {
		t.Fatalf("expected 1 server after removal, got %d", len(config.Servers))
	}
	if config.Servers[0].Name != "server2" {
		t.Fatalf("expected server2 to remain, got '%s'", config.Servers[0].Name)
	}
}

func TestEnsureConfigExists_CreatesSecretIfNotExists(t *testing.T) {
	srw := newTestSecretReaderWriter(t)
	ctx := context.Background()
	namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

	if err := srw.EnsureConfigExists(ctx, namespaceName); err != nil {
		t.Fatalf("EnsureConfigExists failed: %v", err)
	}

	// verify secret was created
	secret := &corev1.Secret{}
	if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
		t.Fatalf("failed to get created secret: %v", err)
	}

	// verify it has the correct labels
	if secret.Labels["mcp.kagenti.com/aggregated"] != "true" {
		t.Fatal("secret missing aggregated label")
	}
}

func TestDeleteConfig(t *testing.T) {
	testCases := []struct {
		name         string
		createFirst  bool
		secretName   string
		expectExists bool
	}{
		{
			name:         "deletes existing secret",
			createFirst:  true,
			secretName:   "mcp-gateway-config",
			expectExists: false,
		},
		{
			name:         "no error if secret does not exist",
			createFirst:  false,
			secretName:   "nonexistent",
			expectExists: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srw := newTestSecretReaderWriter(t)
			ctx := context.Background()
			namespaceName := types.NamespacedName{Namespace: "test-ns", Name: tc.secretName}

			if tc.createFirst {
				if err := srw.EnsureConfigExists(ctx, namespaceName); err != nil {
					t.Fatalf("EnsureConfigExists failed: %v", err)
				}
			}

			if err := srw.DeleteConfig(ctx, namespaceName); err != nil {
				t.Fatalf("DeleteConfig failed: %v", err)
			}

			secret := &corev1.Secret{}
			err := srw.Client.Get(ctx, namespaceName, secret)
			exists := err == nil

			if exists != tc.expectExists {
				t.Fatalf("expected exists=%v, got exists=%v", tc.expectExists, exists)
			}
		})
	}
}
