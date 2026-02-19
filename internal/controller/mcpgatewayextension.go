package controller

import (
	"context"
	"fmt"
	"log/slog"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// MCPGatewayExtensionValidator finds and validates MCPGatewayExtensions
type MCPGatewayExtensionValidator struct {
	client.Client
	DirectAPIReader client.Reader // uncached reader
	Logger          *slog.Logger
}

// HasValidReferenceGrant checks if a valid ReferenceGrant exists that allows the MCPGatewayExtension
// to reference a Gateway in a different namespace
func (r *MCPGatewayExtensionValidator) HasValidReferenceGrant(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (bool, error) {
	// list ReferenceGrants in the target Gateway's namespace
	refGrantList := &gatewayv1beta1.ReferenceGrantList{}
	if err := r.List(ctx, refGrantList, client.InNamespace(mcpExt.Spec.TargetRef.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants: %w", err)
	}
	r.Logger.Debug("HasValidReferenceGrant found reference grants ", "len", len(refGrantList.Items))
	for _, rg := range refGrantList.Items {
		r.Logger.Debug("HasValidReferenceGrant checking reference grant ", "grant", rg.Name)
		if r.referenceGrantAllows(&rg, mcpExt) {
			return true, nil
		}
	}
	return false, nil
}

// referenceGrantAllows checks if a ReferenceGrant permits the MCPGatewayExtension to reference a Gateway
func (r *MCPGatewayExtensionValidator) referenceGrantAllows(rg *gatewayv1beta1.ReferenceGrant, mcpExt *mcpv1alpha1.MCPGatewayExtension) bool {
	fromAllowed := false
	toAllowed := false

	// check if 'from' allows MCPGatewayExtension from its namespace
	for _, from := range rg.Spec.From {
		if string(from.Group) == mcpv1alpha1.GroupVersion.Group &&
			string(from.Kind) == "MCPGatewayExtension" &&
			string(from.Namespace) == mcpExt.Namespace {
			fromAllowed = true
			break
		}
	}

	if !fromAllowed {
		return false
	}

	// check if 'to' allows Gateway references
	for _, to := range rg.Spec.To {
		// empty group means core, but Gateway is in gateway.networking.k8s.io
		if string(to.Group) == gatewayv1.GroupVersion.Group {
			// empty kind means all kinds in the group, or specific Gateway kind
			if to.Kind == "" || string(to.Kind) == "Gateway" {
				// if name is specified, it must match; empty means all
				if to.Name == nil || *to.Name == "" || string(*to.Name) == mcpExt.Spec.TargetRef.Name {
					toAllowed = true
					break
				}
			}
		}
	}

	return toAllowed
}

// FindValidMCPGatewayExtsForGateway will find all MCPGatewayExtensions indexed against passed Gateway instance
func (r *MCPGatewayExtensionValidator) FindValidMCPGatewayExtsForGateway(ctx context.Context, g *gatewayv1.Gateway) ([]*mcpv1alpha1.MCPGatewayExtension, error) {
	logger := logf.FromContext(ctx).WithName("findValidMCPGatewayExtsForGateway")
	validExtensions := []*mcpv1alpha1.MCPGatewayExtension{}
	mcpGatewayExtList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{gatewayIndexKey: gatewayToMCPExtIndexValue(*g)},
	); err != nil {
		return validExtensions, err
	}
	logger.V(1).Info("found mcpgatewayextensions", "total", len(mcpGatewayExtList.Items))
	for _, mg := range mcpGatewayExtList.Items {
		if mg.DeletionTimestamp != nil {
			logger.V(1).Info("found deleting mcpgatewayextensions not including as not valid", "name", mg.Name)
			continue
		}

		if mg.Namespace == g.Namespace {
			validExtensions = append(validExtensions, &mg)
			continue
		}
		has, err := r.HasValidReferenceGrant(ctx, &mg)
		if err != nil {
			// we have to exit here
			return validExtensions, fmt.Errorf("failed to check if mcpgatewayextension is valid %w", err)
		}
		if has && meta.IsStatusConditionTrue(mg.Status.Conditions, mcpv1alpha1.ConditionTypeReady) {
			validExtensions = append(validExtensions, &mg)
		}
	}
	return validExtensions, nil
}

// MCPGatewayExtensionFinderValidator finds and validates MCPGatewayExtensions
type MCPGatewayExtensionFinderValidator interface {
	HasValidReferenceGrant(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (bool, error)
	FindValidMCPGatewayExtsForGateway(ctx context.Context, g *gatewayv1.Gateway) ([]*mcpv1alpha1.MCPGatewayExtension, error)
}
