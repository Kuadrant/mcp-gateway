package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

// buildTrustedHeadersSecrets generates an ecdsa key pair and returns public/private secrets
func buildTrustedHeadersSecrets(mcpExt *mcpv1.MCPGatewayExtension) (*corev1.Secret, *corev1.Secret, error) {
	pubPEM, privPEM, err := generateECDSAKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	labels := map[string]string{
		labelManagedBy: labelManagedByValue,
	}

	pubSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpExt.Spec.TrustedHeadersKey.SecretName,
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		Data: map[string][]byte{
			"key": pubPEM,
		},
	}

	privSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpExt.Spec.TrustedHeadersKey.SecretName + "-private",
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		Data: map[string][]byte{
			"key": privPEM,
		},
	}

	return pubSecret, privSecret, nil
}

// validateTrustedHeadersSecret checks the secret has the required "key" entry
func validateTrustedHeadersSecret(secret *corev1.Secret, secretName string) *validationError {
	if secret.Data == nil {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("secret %s has no data", secretName))
	}
	if _, ok := secret.Data["key"]; !ok {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("secret %s is missing required data entry \"key\"", secretName))
	}
	return nil
}

// reconcileTrustedHeaders reconciles the trusted-header public key secret;
// generates a key pair or validates the byo secret.
func (r *MCPGatewayExtensionReconciler) reconcileTrustedHeaders(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) error {
	if mcpExt.Spec.TrustedHeadersKey == nil {
		return nil
	}

	if mcpExt.Spec.TrustedHeadersKey.Generate == mcpv1.KeyGenerationEnabled {
		return r.reconcileGeneratedTrustedHeaders(ctx, mcpExt)
	}

	// BYO secret: validate it exists and has the required key
	secretName := mcpExt.Spec.TrustedHeadersKey.SecretName
	secret := &corev1.Secret{}
	// use direct reader to avoid cache and informer setup for secrets
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: secretName, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return newValidationError(mcpv1.ConditionReasonSecretNotFound,
				fmt.Sprintf("secret %s not found in namespace %s", secretName, mcpExt.Namespace))
		}
		return fmt.Errorf("failed to get trusted headers secret: %w", err)
	}

	if valErr := validateTrustedHeadersSecret(secret, secretName); valErr != nil {
		return valErr
	}

	return nil
}

// reconcileGeneratedTrustedHeaders creates the public and private key secrets if either is missing.
// Both secrets are checked to handle partial failures from a previous reconcile.
func (r *MCPGatewayExtensionReconciler) reconcileGeneratedTrustedHeaders(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) error {
	secretName := mcpExt.Spec.TrustedHeadersKey.SecretName
	privSecretName := secretName + "-private"

	pubExists, privExists := false, false
	existing := &corev1.Secret{}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: secretName, Namespace: mcpExt.Namespace}, existing); err == nil {
		pubExists = true
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check trusted headers secret: %w", err)
	}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: privSecretName, Namespace: mcpExt.Namespace}, existing); err == nil {
		privExists = true
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to check trusted headers private secret: %w", err)
	}

	if pubExists && privExists {
		return nil
	}

	// if only one secret exists, delete the orphan so we regenerate a matching pair
	if pubExists && !privExists {
		orphan := &corev1.Secret{}
		if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: secretName, Namespace: mcpExt.Namespace}, orphan); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to get orphaned public key secret: %w", err)
			}
		} else {
			r.log.Info("deleting orphaned public key secret to regenerate matching pair", "secret", secretName)
			if err := r.Delete(ctx, orphan); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete orphaned public key secret: %w", err)
			}
		}
	} else if privExists && !pubExists {
		orphan := &corev1.Secret{}
		if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: privSecretName, Namespace: mcpExt.Namespace}, orphan); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to get orphaned private key secret: %w", err)
			}
		} else {
			r.log.Info("deleting orphaned private key secret to regenerate matching pair", "secret", privSecretName)
			if err := r.Delete(ctx, orphan); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete orphaned private key secret: %w", err)
			}
		}
	}

	pubSecret, privSecret, err := buildTrustedHeadersSecrets(mcpExt)
	if err != nil {
		return err
	}

	if err := controllerutil.SetControllerReference(mcpExt, pubSecret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on public key secret: %w", err)
	}
	if err := controllerutil.SetControllerReference(mcpExt, privSecret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on private key secret: %w", err)
	}

	r.log.Info("creating trusted headers key pair", "public", pubSecret.Name, "private", privSecret.Name)
	if err := r.Create(ctx, pubSecret); err != nil {
		return fmt.Errorf("failed to create public key secret: %w", err)
	}
	if err := r.Create(ctx, privSecret); err != nil {
		return fmt.Errorf("failed to create private key secret: %w", err)
	}

	return nil
}
