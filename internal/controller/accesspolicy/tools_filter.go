package accesspolicy

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/Kuadrant/mcp-gateway/api/agentic/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
)

type CallerIdentity struct {
	Issuer         string
	Claims         map[string]string
	ServiceAccount string
	Namespace      string
}

type ToolsFilter struct {
	client    client.Client
	namespace string
}

func NewToolsFilter(client client.Client, namespace string) *ToolsFilter {
	return &ToolsFilter{
		client:    client,
		namespace: namespace,
	}
}

func (f *ToolsFilter) FilterToolsList(
	ctx context.Context,
	allTools []mcp.Tool,
	callerIdentity CallerIdentity,
	backendName string,
) ([]mcp.Tool, error) {
	var policyList agenticv1alpha1.AccessPolicyList
	if err := f.client.List(ctx, &policyList, client.InNamespace(f.namespace)); err != nil {
		return nil, fmt.Errorf("listing access policies: %w", err)
	}

	allowedTools := map[string]bool{}
	hasPolicy := false

	for _, policy := range policyList.Items {
		if !targetsBackend(policy, backendName) {
			continue
		}
		hasPolicy = true
		for _, rule := range policy.Spec.Rules {
			if matchesIdentity(rule.Source, callerIdentity) {
				if len(rule.Tools) == 0 {
					return allTools, nil
				}
				for _, tool := range rule.Tools {
					allowedTools[tool] = true
				}
			}
		}
	}

	if !hasPolicy {
		return allTools, nil
	}

	filtered := make([]mcp.Tool, 0, len(allowedTools))
	for _, tool := range allTools {
		if allowedTools[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered, nil
}

func targetsBackend(policy agenticv1alpha1.AccessPolicy, backendName string) bool {
	for _, ref := range policy.Spec.TargetRefs {
		if string(ref.Name) == backendName {
			return true
		}
	}
	return false
}

func matchesIdentity(source agenticv1alpha1.Source, caller CallerIdentity) bool {
	switch source.Type {
	case agenticv1alpha1.SourceTypeOIDC:
		if source.OIDC == nil || source.OIDC.Issuer != caller.Issuer {
			return false
		}
		for claim, expected := range source.OIDC.Claims {
			if caller.Claims[claim] != expected {
				return false
			}
		}
		return true
	case agenticv1alpha1.SourceTypeServiceAccount:
		if source.ServiceAccount == nil {
			return false
		}
		return caller.ServiceAccount == source.ServiceAccount.Name &&
			caller.Namespace == source.ServiceAccount.Namespace
	default:
		return false
	}
}
