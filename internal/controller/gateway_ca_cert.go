package controller

import (
	"context"
	"fmt"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// validateGatewayCACertSecret validates the gateway CA cert secret referenced by gatewayCACertSecretRef.
func (r *MCPGatewayExtensionReconciler) validateGatewayCACertSecret(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) error {
	if mcpExt.Spec.GatewayCACertSecretRef == nil {
		return nil
	}

	ref := mcpExt.Spec.GatewayCACertSecretRef
	secret := &corev1.Secret{}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return newValidationError(mcpv1.ConditionReasonSecretNotFound,
				fmt.Sprintf("gateway CA cert secret %s not found", ref.Name))
		}
		return fmt.Errorf("failed to get gateway CA cert secret: %w", err)
	}

	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("gateway CA cert secret %s missing required label %s=%s", ref.Name, ManagedSecretLabel, ManagedSecretValue))
	}

	key := ref.Key
	if key == "" {
		key = "ca.crt"
	}
	val, ok := secret.Data[key]
	if !ok {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("gateway CA cert secret %s missing key %s", ref.Name, key))
	}
	if err := validateCACertPEM(val); err != nil {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("gateway CA cert in secret %s is invalid: %v", ref.Name, err))
	}

	return nil
}
