package config

import (
	"context"
	"log/slog"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
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
	logger := slog.New(slog.DiscardHandler)
	return &SecretReaderWriter{
		Client: fakeClient,
		Scheme: scheme,
		Logger: logger,
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
				{Name: "test-server", URL: "http://test.local:8080/mcp", Prefix: "test_", State: string(mcpv1.ServerStateEnabled)},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://test.local:8080/mcp", Prefix: "test_"},
		},
		{
			name: "updates existing server",
			serversToAdd: []MCPServer{
				{Name: "test-server", URL: "http://old.local:8080/mcp", Prefix: "old_", State: string(mcpv1.ServerStateEnabled)},
				{Name: "test-server", URL: "http://new.local:8080/mcp", Prefix: "new_", State: string(mcpv1.ServerStateEnabled)},
			},
			expectedCount:  1,
			expectedServer: MCPServer{Name: "test-server", URL: "http://new.local:8080/mcp", Prefix: "new_"},
		},
		{
			name: "appends new server",
			serversToAdd: []MCPServer{
				{Name: "server1", URL: "http://s1.local/mcp", State: string(mcpv1.ServerStateEnabled)},
				{Name: "server2", URL: "http://s2.local/mcp", State: string(mcpv1.ServerStateEnabled)},
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
				if config.Servers[0].Prefix != tc.expectedServer.Prefix {
					t.Errorf("expected Prefix %q, got %q", tc.expectedServer.Prefix, config.Servers[0].Prefix)
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
	server1 := MCPServer{Name: "server1", URL: "http://s1.local/mcp", State: string(mcpv1.ServerStateEnabled)}
	server2 := MCPServer{Name: "server2", URL: "http://s2.local/mcp", State: string(mcpv1.ServerStateEnabled)}
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
				server := MCPServer{Name: "test", URL: "http://test.local/mcp", State: string(mcpv1.ServerStateEnabled)}
				if err := srw.UpsertMCPServer(ctx, server, namespaceName); err != nil {
					t.Fatalf("UpsertMCPServer failed: %v", err)
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

func TestWriteGatewayConfig(t *testing.T) {
	testCases := []struct {
		name           string
		gwCfg          *GatewayConfig
		wantCA         string
		wantGuardrails *GuardrailsConfig
	}{
		{
			name: "writes both CA and guardrails",
			gwCfg: &GatewayConfig{
				CACertPEM: "-----BEGIN CERTIFICATE-----\ntest-ca-cert\n-----END CERTIFICATE-----",
				Guardrails: &GuardrailsConfig{
					URL:       "https://nemo-guardrails.internal:8080",
					ConfigIDs: []string{"tool-safety-v1"},
					Model:     "meta/llama-3.1-8b-instruct",
					FailMode:  "deny",
				},
			},
			wantCA: "-----BEGIN CERTIFICATE-----\ntest-ca-cert\n-----END CERTIFICATE-----",
			wantGuardrails: &GuardrailsConfig{
				URL:       "https://nemo-guardrails.internal:8080",
				ConfigIDs: []string{"tool-safety-v1"},
				Model:     "meta/llama-3.1-8b-instruct",
				FailMode:  "deny",
			},
		},
		{
			name:           "nil clears both fields",
			gwCfg:          nil,
			wantCA:         "",
			wantGuardrails: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srw := newTestSecretReaderWriter(t)
			ctx := context.Background()
			namespaceName := types.NamespacedName{Namespace: "test-ns", Name: "mcp-gateway-config"}

			seedCA := "seed-ca"
			seedGuardrails := &GuardrailsConfig{URL: "https://seed.internal", Model: "seed-model"}
			if err := srw.WriteGatewayConfig(ctx, &GatewayConfig{CACertPEM: seedCA, Guardrails: seedGuardrails}, namespaceName); err != nil {
				t.Fatalf("seed WriteGatewayConfig failed: %v", err)
			}

			if err := srw.WriteGatewayConfig(ctx, tc.gwCfg, namespaceName); err != nil {
				t.Fatalf("WriteGatewayConfig failed: %v", err)
			}

			secret := &corev1.Secret{}
			if err := srw.Client.Get(ctx, namespaceName, secret); err != nil {
				t.Fatalf("failed to get secret: %v", err)
			}

			configData := secret.StringData[configFileName]
			if configData == "" {
				configData = string(secret.Data[configFileName])
			}
			var cfg BrokerConfig
			if err := yaml.Unmarshal([]byte(configData), &cfg); err != nil {
				t.Fatalf("failed to unmarshal config: %v", err)
			}

			if cfg.GatewayCACertPEM != tc.wantCA {
				t.Fatalf("GatewayCACertPEM = %q, want %q", cfg.GatewayCACertPEM, tc.wantCA)
			}
			if (cfg.GlobalGuardrails == nil) != (tc.wantGuardrails == nil) {
				t.Fatalf("GlobalGuardrails = %+v, want %+v", cfg.GlobalGuardrails, tc.wantGuardrails)
			}
			if tc.wantGuardrails != nil {
				if cfg.GlobalGuardrails.URL != tc.wantGuardrails.URL || cfg.GlobalGuardrails.Model != tc.wantGuardrails.Model {
					t.Fatalf("GlobalGuardrails = %+v, want %+v", cfg.GlobalGuardrails, tc.wantGuardrails)
				}
			}
		})
	}
}
