package controller

import (
	"context"
	"fmt"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// validateSessionStore checks that the session store secret exists and contains the REDIS_URL key.
func (r *MCPGatewayExtensionReconciler) validateSessionStore(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	if mcpExt.Spec.SessionStore == nil {
		return nil
	}

	secretName := mcpExt.Spec.SessionStore.SecretName
	secret := &corev1.Secret{}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: secretName, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return newValidationError(mcpv1alpha1.ConditionReasonSecretNotFound,
				fmt.Sprintf("session store secret %s not found in namespace %s", secretName, mcpExt.Namespace))
		}
		return fmt.Errorf("failed to get session store secret: %w", err)
	}

	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return newValidationError(mcpv1alpha1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s is missing required label %s=%s", secretName, ManagedSecretLabel, ManagedSecretValue))
	}
	if secret.Data == nil {
		return newValidationError(mcpv1alpha1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s has no data", secretName))
	}
	if _, ok := secret.Data["REDIS_URL"]; !ok {
		return newValidationError(mcpv1alpha1.ConditionReasonSecretInvalid,
			fmt.Sprintf("session store secret %s is missing required data entry \"REDIS_URL\"", secretName))
	}

	return nil
}

// enqueueMCPGatewayExtForSecret maps a secret change to MCPGatewayExtension reconcile requests.
// It enqueues extensions that reference the secret via trustedHeadersKey or sessionStore.
func (r *MCPGatewayExtensionReconciler) enqueueMCPGatewayExtForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)

	extList := &mcpv1alpha1.MCPGatewayExtensionList{}
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
		}
	}
	return requests
}
