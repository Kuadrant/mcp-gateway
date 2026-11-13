package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

func TestResolveGuardrails(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	validConfigYAML := `
url: https://nemo-guardrails.internal:8080
configIDs:
  - tool-safety-v1
model: meta/llama-3.1-8b-instruct
`

	tests := []struct {
		name        string
		annotations map[string]string
		secrets     []corev1.Secret
		wantErr     bool
		wantReason  string
		errContains string
		wantConfig  *config.GuardrailsConfig
	}{
		{
			name:        "no guardrails-ref annotation returns nil",
			annotations: nil,
			wantConfig:  nil,
		},
		{
			name:        "secret not found",
			annotations: map[string]string{labelGuardrailsReference: "missing"},
			wantErr:     true,
			wantReason:  mcpv1.GuardrailsSecretNotFound,
			errContains: "guardrails secret missing not found",
		},
		{
			name:        "missing label",
			annotations: map[string]string{labelGuardrailsReference: "no-label"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{Name: "no-label", Namespace: "test-ns"},
				Type:       guardrails.SecretTypeNeMo,
				Data:       map[string][]byte{"config.yaml": []byte(validConfigYAML)},
			}},
			wantErr:     true,
			wantReason:  mcpv1.GuardrailsSecretInvalid,
			errContains: "guardrails secret no-label missing required label " + ManagedSecretLabel + "=" + ManagedSecretValue,
		},
		{
			name:        "invalid config data",
			annotations: map[string]string{labelGuardrailsReference: "bad-config"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bad-config", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Type: guardrails.SecretTypeNeMo,
				Data: map[string][]byte{"config.yaml": []byte("model: only-model")},
			}},
			wantErr:     true,
			wantReason:  mcpv1.GuardrailsSecretInvalid,
			errContains: "is invalid",
		},
		{
			name:        "valid secret returns resolved config",
			annotations: map[string]string{labelGuardrailsReference: "good-config"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "good-config", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Type: guardrails.SecretTypeNeMo,
				Data: map[string][]byte{"config.yaml": []byte(validConfigYAML)},
			}},
			wantConfig: &config.GuardrailsConfig{
				URL:       "https://nemo-guardrails.internal:8080",
				ConfigIDs: []string{"tool-safety-v1"},
				Model:     "meta/llama-3.1-8b-instruct",
				FailMode:  guardrails.FailModeDeny,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, len(tt.secrets))
			for i := range tt.secrets {
				objs[i] = &tt.secrets[i]
			}
			fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()

			writer := &capturingConfigWriter{}
			r := &MCPGatewayExtensionReconciler{
				Client:              fc,
				DirectAPIReader:     fc,
				ConfigWriterDeleter: writer,
			}

			mcpExt := &mcpv1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns", Annotations: tt.annotations},
			}

			got, err := r.resolveGuardrails(context.Background(), mcpExt)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var valErr *validationError
				if !errors.As(err, &valErr) {
					t.Fatalf("expected *validationError, got %T: %v", err, err)
				}
				if valErr.reason != tt.wantReason {
					t.Fatalf("reason = %q, want %q", valErr.reason, tt.wantReason)
				}
				if !strings.Contains(valErr.message, tt.errContains) {
					t.Fatalf("message %q does not contain %q", valErr.message, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := tt.wantConfig
			if (got == nil) != (want == nil) {
				t.Fatalf("got = %+v, want %+v", got, want)
			}
			if got != nil {
				if got.URL != want.URL || got.Model != want.Model || got.FailMode != want.FailMode ||
					strings.Join(got.ConfigIDs, ",") != strings.Join(want.ConfigIDs, ",") {
					t.Fatalf("got = %+v, want %+v", got, want)
				}
			}
		})
	}
}
