package controller

import (
	"context"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValidateGatewayCACertSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	validPEM := generateTestCACertPEM(t)

	tests := []struct {
		name      string
		ref       *mcpv1.CACertSecretReference
		secrets   []runtime.Object
		wantErr   bool
		errReason string
	}{
		{
			name:    "nil ref",
			ref:     nil,
			wantErr: false,
		},
		{
			name:      "secret not found",
			ref:       &mcpv1.CACertSecretReference{Name: "missing"},
			wantErr:   true,
			errReason: "not found",
		},
		{
			name: "missing label",
			ref:  &mcpv1.CACertSecretReference{Name: "no-label"},
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "no-label", Namespace: "default"},
					Data:       map[string][]byte{"ca.crt": validPEM},
				},
			},
			wantErr:   true,
			errReason: "missing required label",
		},
		{
			name: "missing key",
			ref:  &mcpv1.CACertSecretReference{Name: "wrong-key", Key: "custom.crt"},
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "wrong-key",
						Namespace: "default",
						Labels:    map[string]string{ManagedSecretLabel: ManagedSecretValue},
					},
					Data: map[string][]byte{"ca.crt": validPEM},
				},
			},
			wantErr:   true,
			errReason: "missing key",
		},
		{
			name: "invalid pem",
			ref:  &mcpv1.CACertSecretReference{Name: "invalid-pem"},
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "invalid-pem",
						Namespace: "default",
						Labels:    map[string]string{ManagedSecretLabel: ManagedSecretValue},
					},
					Data: map[string][]byte{"ca.crt": []byte("not-a-certificate")},
				},
			},
			wantErr:   true,
			errReason: "is invalid",
		},
		{
			name: "exceeds size limit",
			ref:  &mcpv1.CACertSecretReference{Name: "big-secret"},
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "big-secret",
						Namespace: "default",
						Labels:    map[string]string{ManagedSecretLabel: ManagedSecretValue},
					},
					Data: map[string][]byte{"ca.crt": make([]byte, maxCACertBundleSize+1)},
				},
			},
			wantErr:   true,
			errReason: "exceeds maximum size",
		},
		{
			name: "valid ca cert",
			ref:  &mcpv1.CACertSecretReference{Name: "valid-ca"},
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-ca",
						Namespace: "default",
						Labels:    map[string]string{ManagedSecretLabel: ManagedSecretValue},
					},
					Data: map[string][]byte{"ca.crt": validPEM},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tt.secrets...).Build()
			r := &MCPGatewayExtensionReconciler{
				Client:          client,
				DirectAPIReader: client,
			}

			mcpExt := &mcpv1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: mcpv1.MCPGatewayExtensionSpec{
					GatewayCACertSecretRef: tt.ref,
				},
			}

			err := r.validateGatewayCACertSecret(context.Background(), mcpExt)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errReason != "" {
					assert.Contains(t, err.Error(), tt.errReason)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
