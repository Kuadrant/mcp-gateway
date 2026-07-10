package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	sessionSigningKeySecretName = "mcp-gateway-session-signing-key" //nolint:gosec // not a credential
	sessionSigningKeyDataKey    = "key"
	sessionSigningKeyEnvVar     = "GATEWAY_SIGNING_KEY"
	sessionSigningKeyBytes      = 32 // 256-bit key for HS256
)

// reconcileSessionSigningKey ensures a secret containing a random JWT signing
// key exists. Corrupted or misconfigured secrets are repaired in-place.
func (r *MCPGatewayExtensionReconciler) reconcileSessionSigningKey(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) error {
	existing := &corev1.Secret{}
	err := r.DirectAPIReader.Get(ctx, client.ObjectKey{
		Name:      sessionSigningKeySecretName,
		Namespace: mcpExt.Namespace,
	}, existing)
	if err == nil {
		needsUpdate := false

		// ensure owner reference is set
		if !hasOwnerReference(existing, mcpExt) {
			if err := controllerutil.SetControllerReference(mcpExt, existing, r.Scheme); err != nil {
				return fmt.Errorf("failed to set owner reference on session signing key secret: %w", err)
			}
			needsUpdate = true
		}

		// ensure required labels are present
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		if existing.Labels[labelManagedBy] != labelManagedByValue {
			existing.Labels[labelManagedBy] = labelManagedByValue
			needsUpdate = true
		}
		if existing.Labels[ManagedSecretLabel] != ManagedSecretValue {
			existing.Labels[ManagedSecretLabel] = ManagedSecretValue
			needsUpdate = true
		}

		// verify key data
		if existing.Data == nil || len(existing.Data[sessionSigningKeyDataKey]) == 0 {
			keyBytes := make([]byte, sessionSigningKeyBytes)
			if _, err := rand.Read(keyBytes); err != nil {
				return fmt.Errorf("failed to generate session signing key: %w", err)
			}
			existing.Data = map[string][]byte{
				sessionSigningKeyDataKey: []byte(hex.EncodeToString(keyBytes)),
			}
			needsUpdate = true
			r.log.Info("regenerating corrupted session signing key", "name", sessionSigningKeySecretName)
		}

		if needsUpdate {
			if err := r.Update(ctx, existing); err != nil {
				return fmt.Errorf("failed to update session signing key secret: %w", err)
			}
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check session signing key secret: %w", err)
	}

	// generate a random key
	keyBytes := make([]byte, sessionSigningKeyBytes)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("failed to generate session signing key: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionSigningKeySecretName,
			Namespace: mcpExt.Namespace,
			Labels: map[string]string{
				labelManagedBy:     labelManagedByValue,
				ManagedSecretLabel: ManagedSecretValue,
			},
		},
		Data: map[string][]byte{
			sessionSigningKeyDataKey: []byte(hex.EncodeToString(keyBytes)),
		},
	}

	if err := controllerutil.SetControllerReference(mcpExt, secret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on session signing key secret: %w", err)
	}

	r.log.Info("creating session signing key secret", "name", sessionSigningKeySecretName)
	if err := r.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("failed to create session signing key secret: %w", err)
	}

	return nil
}

func hasOwnerReference(secret *corev1.Secret, owner *mcpv1.MCPGatewayExtension) bool {
	for _, ref := range secret.OwnerReferences {
		if ref.UID == owner.UID {
			return true
		}
	}
	return false
}
