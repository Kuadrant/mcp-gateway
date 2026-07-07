package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

func TestBuildTrustedHeadersSecrets(t *testing.T) {
	mcpExt := &mcpv1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
		},
		Spec: mcpv1.MCPGatewayExtensionSpec{
			TrustedHeadersKey: &mcpv1.TrustedHeadersKey{
				SecretName: "my-keys",
				Generate:   mcpv1.KeyGenerationEnabled,
			},
		},
	}

	pubSecret, privSecret, err := buildTrustedHeadersSecrets(mcpExt)
	if err != nil {
		t.Fatalf("buildTrustedHeadersSecrets() error: %v", err)
	}

	t.Run("public secret name and namespace", func(t *testing.T) {
		if pubSecret.Name != "my-keys" {
			t.Errorf("public secret name = %q, want %q", pubSecret.Name, "my-keys")
		}
		if pubSecret.Namespace != "test-ns" {
			t.Errorf("public secret namespace = %q, want %q", pubSecret.Namespace, "test-ns")
		}
	})

	t.Run("private secret name and namespace", func(t *testing.T) {
		if privSecret.Name != "my-keys-private" {
			t.Errorf("private secret name = %q, want %q", privSecret.Name, "my-keys-private")
		}
		if privSecret.Namespace != "test-ns" {
			t.Errorf("private secret namespace = %q, want %q", privSecret.Namespace, "test-ns")
		}
	})

	t.Run("public secret has key data entry", func(t *testing.T) {
		if _, ok := pubSecret.Data["key"]; !ok {
			t.Error("public secret missing \"key\" data entry")
		}
		if len(pubSecret.Data["key"]) == 0 {
			t.Error("public secret \"key\" data is empty")
		}
	})

	t.Run("private secret has key data entry", func(t *testing.T) {
		if _, ok := privSecret.Data["key"]; !ok {
			t.Error("private secret missing \"key\" data entry")
		}
		if len(privSecret.Data["key"]) == 0 {
			t.Error("private secret \"key\" data is empty")
		}
	})

	t.Run("managed-by labels", func(t *testing.T) {
		if pubSecret.Labels[labelManagedBy] != labelManagedByValue {
			t.Errorf("public secret managed-by label = %q, want %q", pubSecret.Labels[labelManagedBy], labelManagedByValue)
		}
		if privSecret.Labels[labelManagedBy] != labelManagedByValue {
			t.Errorf("private secret managed-by label = %q, want %q", privSecret.Labels[labelManagedBy], labelManagedByValue)
		}
	})
}

func TestValidateTrustedHeadersSecret(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		wantErr    bool
		wantReason string
	}{
		{
			name: "valid secret",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"key": []byte("some-key-data"),
				},
			},
			wantErr: false,
		},
		{
			name: "missing key entry",
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"other": []byte("data"),
				},
			},
			wantErr:    true,
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "empty data map",
			secret: &corev1.Secret{
				Data: map[string][]byte{},
			},
			wantErr:    true,
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "nil data",
			secret:     &corev1.Secret{},
			wantErr:    true,
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valErr := validateTrustedHeadersSecret(tt.secret, "test-secret")
			if tt.wantErr && valErr == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && valErr != nil {
				t.Errorf("expected no error, got: %v", valErr)
			}
			if tt.wantErr && valErr != nil && valErr.reason != tt.wantReason {
				t.Errorf("error reason = %q, want %q", valErr.reason, tt.wantReason)
			}
		})
	}
}
