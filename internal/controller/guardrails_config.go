package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

// resolveGuardrails validates the guardrails Secret referenced by the
// labelGuardrailsReference annotation and returns the resolved config.
func (r *MCPGatewayExtensionReconciler) resolveGuardrails(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (*config.GuardrailsConfig, error) {
	guardrailsSecretRef := mcpExt.Annotations[labelGuardrailsReference]
	if guardrailsSecretRef == "" {
		return nil, nil //nolint:nilnil
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: guardrailsSecretRef, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, newValidationError(mcpv1.GuardrailsSecretNotFound,
				fmt.Sprintf("guardrails secret %s not found", guardrailsSecretRef))
		}
		return nil, fmt.Errorf("failed to get guardrails secret: %w", err)
	}

	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return nil, newValidationError(mcpv1.GuardrailsSecretInvalid,
			fmt.Sprintf("guardrails secret %s missing required label %s=%s", guardrailsSecretRef, ManagedSecretLabel, ManagedSecretValue))
	}

	guardrailsConfig, err := guardrails.EnsureNeMoConfigData(secret.Type, secret.Data)
	if err != nil {
		return nil, newValidationError(mcpv1.GuardrailsSecretInvalid,
			fmt.Sprintf("guardrails secret %s is invalid: %v", guardrailsSecretRef, err))
	}

	return guardrailsConfig, nil
}

// setGuardrailsResolvedCondition mirrors resolveGuardrails' outcome onto the
// GuardrailsResolved condition and reports whether it changed. The condition
// is removed when no guardrails-ref is set.
func setGuardrailsResolvedCondition(mcpExt *mcpv1.MCPGatewayExtension, valErr *validationError) bool {
	ref := mcpExt.Annotations[labelGuardrailsReference]
	switch {
	case ref == "":
		return meta.RemoveStatusCondition(&mcpExt.Status.Conditions, mcpv1.ConditionTypeGuardrailsResolved)
	case valErr != nil:
		return mcpExt.SetGuardrailsResolvedCondition(metav1.ConditionFalse, valErr.reason, valErr.message)
	default:
		return mcpExt.SetGuardrailsResolvedCondition(metav1.ConditionTrue, mcpv1.ConditionReasonGuardrailsResolved,
			fmt.Sprintf("guardrails secret %s resolved", ref))
	}
}
