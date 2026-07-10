package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func generateTestCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

type capturingConfigWriter struct {
	lastCACertPEM string
	writeCalled   bool
}

func (c *capturingConfigWriter) DeleteConfig(_ context.Context, _ types.NamespacedName) error {
	return nil
}
func (c *capturingConfigWriter) EnsureConfigExists(_ context.Context, _ types.NamespacedName) error {
	return nil
}
func (c *capturingConfigWriter) WriteEmptyConfig(_ context.Context, _ types.NamespacedName) error {
	return nil
}
func (c *capturingConfigWriter) WriteCACertBundle(_ context.Context, caCertPEM string, _ types.NamespacedName) error {
	c.lastCACertPEM = caCertPEM
	c.writeCalled = true
	return nil
}

func TestReconcileCACertBundle(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	validPEM := generateTestCACertPEM(t)

	tests := []struct {
		name        string
		bundleRef   *mcpv1.CACertBundleReference
		secrets     []corev1.Secret
		wantErr     bool
		errContains string
		wantPEM     string
	}{
		{
			name:      "nil ref clears config",
			bundleRef: nil,
			wantPEM:   "",
		},
		{
			name:        "secret not found",
			bundleRef:   &mcpv1.CACertBundleReference{Name: "missing"},
			wantErr:     true,
			errContains: "not found",
		},
		{
			name:      "missing label",
			bundleRef: &mcpv1.CACertBundleReference{Name: "no-label"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{Name: "no-label", Namespace: "test-ns"},
				Data:       map[string][]byte{"ca.crt": validPEM},
			}},
			wantErr:     true,
			errContains: "missing required label",
		},
		{
			name:      "missing key",
			bundleRef: &mcpv1.CACertBundleReference{Name: "good-secret", Key: "wrong-key"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "good-secret", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Data: map[string][]byte{"ca.crt": validPEM},
			}},
			wantErr:     true,
			errContains: "missing key wrong-key",
		},
		{
			name:      "invalid PEM",
			bundleRef: &mcpv1.CACertBundleReference{Name: "bad-pem"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bad-pem", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Data: map[string][]byte{"ca.crt": []byte("not-a-cert")},
			}},
			wantErr:     true,
			errContains: "invalid",
		},
		{
			name:      "exceeds size limit",
			bundleRef: &mcpv1.CACertBundleReference{Name: "big-secret"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "big-secret", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Data: map[string][]byte{"ca.crt": make([]byte, maxCACertBundleSize+1)},
			}},
			wantErr:     true,
			errContains: "exceeds maximum size",
		},
		{
			name:      "valid bundle with default key",
			bundleRef: &mcpv1.CACertBundleReference{Name: "valid-ca"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "valid-ca", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Data: map[string][]byte{"ca.crt": validPEM},
			}},
			wantPEM: string(validPEM),
		},
		{
			name:      "valid bundle with custom key",
			bundleRef: &mcpv1.CACertBundleReference{Name: "custom-key", Key: "bundle.pem"},
			secrets: []corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "custom-key", Namespace: "test-ns",
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
				Data: map[string][]byte{"bundle.pem": validPEM},
			}},
			wantPEM: string(validPEM),
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
				DirectAPIReader:     fc,
				ConfigWriterDeleter: writer,
			}

			mcpExt := &mcpv1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns"},
				Spec: mcpv1.MCPGatewayExtensionSpec{
					CACertBundleRef: tt.bundleRef,
				},
			}

			err := r.reconcileCACertBundle(context.Background(), mcpExt)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" {
					msg := err.Error()
					var valErr *validationError
					if errors.As(err, &valErr) {
						msg = valErr.message
					}
					if !strings.Contains(msg, tt.errContains) {
						t.Fatalf("error %q does not contain %q", msg, tt.errContains)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !writer.writeCalled {
				t.Fatal("WriteCACertBundle was not called")
			}
			if writer.lastCACertPEM != tt.wantPEM {
				t.Fatalf("unexpected PEM: got %d bytes, want %d bytes", len(writer.lastCACertPEM), len(tt.wantPEM))
			}
		})
	}
}
