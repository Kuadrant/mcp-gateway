package controller

import (
	"context"
	"fmt"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/redisconfig"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// validateSessionStore checks that the session store secret contains a valid
// Redis configuration and returns its mode, or "" when no store is configured.
func (r *MCPGatewayExtensionReconciler) validateSessionStore(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (string, error) {
	if mcpExt.Spec.SessionStore == nil {
		return "", nil
	}

	secretName := mcpExt.Spec.SessionStore.SecretName
	secret := &corev1.Secret{}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: secretName, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", newValidationError(mcpv1.ConditionReasonSecretNotFound,
				fmt.Sprintf("session store secret %s not found in namespace %s", secretName, mcpExt.Namespace))
		}
		return "", fmt.Errorf("failed to get session store secret: %w", err)
	}

	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return "", newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s is missing required label %s=%s", secretName, ManagedSecretLabel, ManagedSecretValue))
	}
	if secret.Data == nil {
		return "", newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s has no data", secretName))
	}
	// keys the mode ignores are warned about once by the broker-router at startup
	cfg, err := redisconfig.Parse(func(key string) string { return string(secret.Data[key]) })
	if err != nil {
		return "", newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s has invalid Redis configuration: %v", secretName, err))
	}
	return cfg.Mode, nil
}

// enqueueMCPGatewayExtForSecret maps a secret change to MCPGatewayExtension reconcile requests.
// It enqueues extensions that reference the secret via sessionStore, trustedHeadersKey,
// caCertBundleRef, the session signing key, or the guardrails-ref annotation.
func (r *MCPGatewayExtensionReconciler) enqueueMCPGatewayExtForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)

	extList := &mcpv1.MCPGatewayExtensionList{}
	if err := r.List(ctx, extList, client.InNamespace(secret.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, ext := range extList.Items {
		if ext.Spec.SessionStore != nil && ext.Spec.SessionStore.SecretName == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ext.Name, Namespace: ext.Namespace},
			})
			continue
		}
		if ext.Spec.TrustedHeadersKey != nil && ext.Spec.TrustedHeadersKey.SecretName == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ext.Name, Namespace: ext.Namespace},
			})
			continue
		}
		if ext.Spec.CACertBundleRef != nil && ext.Spec.CACertBundleRef.Name == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ext.Name, Namespace: ext.Namespace},
			})
			continue
		}
		if secret.Name == sessionSigningKeySecretName {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ext.Name, Namespace: ext.Namespace},
			})
			continue
		}
		if ref := ext.Annotations[labelGuardrailsReference]; ref != "" && ref == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: ext.Name, Namespace: ext.Namespace},
			})
			continue
		}
	}
	return requests
}
