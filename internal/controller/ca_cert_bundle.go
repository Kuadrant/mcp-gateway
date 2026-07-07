package controller

import (
	"context"
	"fmt"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// bundle may contain multiple CA certs; larger than per-server maxCACertSize
const maxCACertBundleSize = 256 * 1024

// reconcileCACertBundle validates the CA bundle secret referenced by caCertBundleRef
// and writes the PEM data into the config secret.
func (r *MCPGatewayExtensionReconciler) reconcileCACertBundle(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) error {
	ns := config.NamespaceName(mcpExt.Namespace)

	if mcpExt.Spec.CACertBundleRef == nil {
		return r.ConfigWriterDeleter.WriteCACertBundle(ctx, "", ns)
	}

	ref := mcpExt.Spec.CACertBundleRef
	secret := &corev1.Secret{}
	if err := r.DirectAPIReader.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: mcpExt.Namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return newValidationError(mcpv1.ConditionReasonSecretNotFound,
				fmt.Sprintf("CA bundle secret %s not found", ref.Name))
		}
		return fmt.Errorf("failed to get CA bundle secret: %w", err)
	}

	if secret.Labels == nil || secret.Labels[ManagedSecretLabel] != ManagedSecretValue {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("CA bundle secret %s missing required label %s=%s", ref.Name, ManagedSecretLabel, ManagedSecretValue))
	}

	key := ref.Key
	if key == "" {
		key = "ca.crt"
	}
	val, ok := secret.Data[key]
	if !ok {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("CA bundle secret %s missing key %s", ref.Name, key))
	}
	if len(val) > maxCACertBundleSize {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("CA bundle data in secret %s exceeds maximum size (%d bytes)", ref.Name, maxCACertBundleSize))
	}
	if err := validateCACertPEM(val); err != nil {
		return newValidationError(mcpv1.ConditionReasonSecretInvalid,
			fmt.Sprintf("CA bundle in secret %s is invalid: %v", ref.Name, err))
	}

	return r.ConfigWriterDeleter.WriteCACertBundle(ctx, string(val), ns)
}
