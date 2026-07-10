package controller

import (
	"context"
	"log/slog"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testReconciler(objs ...client.Object) *MCPGatewayExtensionReconciler {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	return &MCPGatewayExtensionReconciler{
		Client:          fakeClient,
		Scheme:          scheme,
		DirectAPIReader: fakeClient,
		log:             slog.Default(),
	}
}

func testMCPExt() *mcpv1.MCPGatewayExtension {
	return &mcpv1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
			UID:       types.UID("test-uid"),
		},
	}
}

func TestReconcileSessionSigningKey_CreatesSecret(t *testing.T) {
	r := testReconciler()
	mcpExt := testMCPExt()

	if err := r.reconcileSessionSigningKey(context.Background(), mcpExt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secret := &corev1.Secret{}
	err := r.Get(context.Background(), client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: "test-ns",
	}, secret)
	if err != nil {
		t.Fatalf("expected secret to be created: %v", err)
	}

	key, ok := secret.Data[sessionSigningKeyDataKey]
	if !ok {
		t.Fatal("secret missing key data entry")
	}
	if len(key) == 0 {
		t.Fatal("secret key data is empty")
	}
	// hex-encoded 32 bytes = 64 characters
	if len(key) != 64 {
		t.Errorf("expected 64-char hex key, got %d chars", len(key))
	}

	if secret.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("expected managed-by label %q, got %q", labelManagedByValue, secret.Labels[labelManagedBy])
	}
	if secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		t.Errorf("expected managed secret label %q=%q, got %q", ManagedSecretLabel, ManagedSecretValue, secret.Labels[ManagedSecretLabel])
	}

	// verify owner reference
	if len(secret.OwnerReferences) == 0 {
		t.Fatal("expected owner reference to be set")
	}
	if secret.OwnerReferences[0].UID != mcpExt.UID {
		t.Errorf("expected owner UID %q, got %q", mcpExt.UID, secret.OwnerReferences[0].UID)
	}
}

func TestReconcileSessionSigningKey_ExistingSecretIsNoop(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionSigningKeySecretName,
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			sessionSigningKeyDataKey: []byte("existing-key-value"),
		},
	}

	r := testReconciler(existing)
	mcpExt := testMCPExt()

	if err := r.reconcileSessionSigningKey(context.Background(), mcpExt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// verify the key was not changed
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: "test-ns",
	}, secret); err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if string(secret.Data[sessionSigningKeyDataKey]) != "existing-key-value" {
		t.Errorf("expected key to remain unchanged, got %q", secret.Data[sessionSigningKeyDataKey])
	}
}

func TestReconcileSessionSigningKey_EmptyKeyRegenerates(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionSigningKeySecretName,
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			sessionSigningKeyDataKey: []byte(""),
		},
	}

	r := testReconciler(existing)
	mcpExt := testMCPExt()

	if err := r.reconcileSessionSigningKey(context.Background(), mcpExt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: "test-ns",
	}, secret); err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if len(secret.Data[sessionSigningKeyDataKey]) != 64 {
		t.Errorf("expected regenerated 64-char hex key, got %d chars", len(secret.Data[sessionSigningKeyDataKey]))
	}
}

func TestReconcileSessionSigningKey_NilDataRegenerates(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionSigningKeySecretName,
			Namespace: "test-ns",
		},
	}

	r := testReconciler(existing)
	mcpExt := testMCPExt()

	if err := r.reconcileSessionSigningKey(context.Background(), mcpExt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: "test-ns",
	}, secret); err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}
	if len(secret.Data[sessionSigningKeyDataKey]) != 64 {
		t.Errorf("expected regenerated 64-char hex key, got %d chars", len(secret.Data[sessionSigningKeyDataKey]))
	}
}

func TestReconcileSessionSigningKey_RepairsMissingLabels(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionSigningKeySecretName,
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			sessionSigningKeyDataKey: []byte("existing-key-value-that-is-valid"),
		},
	}

	r := testReconciler(existing)
	mcpExt := testMCPExt()

	if err := r.reconcileSessionSigningKey(context.Background(), mcpExt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: "test-ns",
	}, secret); err != nil {
		t.Fatalf("expected secret to exist: %v", err)
	}

	// key should be unchanged
	if string(secret.Data[sessionSigningKeyDataKey]) != "existing-key-value-that-is-valid" {
		t.Errorf("expected key unchanged, got %q", secret.Data[sessionSigningKeyDataKey])
	}

	// labels should be repaired
	if secret.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("expected managed-by label repaired, got %q", secret.Labels[labelManagedBy])
	}
	if secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		t.Errorf("expected managed secret label repaired, got %q", secret.Labels[ManagedSecretLabel])
	}

	// owner reference should be added
	if len(secret.OwnerReferences) == 0 {
		t.Fatal("expected owner reference to be set on repair")
	}
	if secret.OwnerReferences[0].UID != mcpExt.UID {
		t.Errorf("expected owner UID %q, got %q", mcpExt.UID, secret.OwnerReferences[0].UID)
	}
}
