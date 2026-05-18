package accesspolicy

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agenticv1alpha1 "github.com/Kuadrant/mcp-gateway/api/agentic/v1alpha1"
)

func (r *AccessPolicyReconciler) buildAuthPolicy(policy *agenticv1alpha1.AccessPolicy) *AuthPolicy {
	if len(policy.Spec.TargetRefs) == 0 {
		return nil
	}

	authPolicy := &AuthPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kuadrant.io/v1",
			Kind:       "AuthPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ap-generated-%s", policy.Name),
			Namespace: policy.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":         "accesspolicy-controller",
				"accesspolicy.agentic.networking/name": policy.Name,
			},
		},
		Spec: AuthPolicySpec{
			TargetRef: policy.Spec.TargetRefs[0],
			AuthScheme: AuthScheme{
				Identity:      make(map[string]Identity),
				Authorization: make(map[string]AuthorizationRule),
			},
		},
	}

	for i, rule := range policy.Spec.Rules {
		authRule := buildAuthRule(rule)
		authPolicy.Spec.AuthScheme.Authorization[fmt.Sprintf("access-rule-%d", i)] = authRule
	}

	for i, rule := range policy.Spec.Rules {
		if rule.Source.Type == agenticv1alpha1.SourceTypeOIDC && rule.Source.OIDC != nil {
			identityName := fmt.Sprintf("oidc-source-%d", i)
			authPolicy.Spec.AuthScheme.Identity[identityName] = Identity{
				OIDC: &OIDCIdentity{
					Endpoint: rule.Source.OIDC.Issuer,
				},
			}
		}
	}

	return authPolicy
}

func buildAuthRule(rule agenticv1alpha1.AccessRule) AuthorizationRule {
	patterns := []PatternExpression{}

	if len(rule.Tools) > 0 {
		quoted := make([]string, len(rule.Tools))
		for i, t := range rule.Tools {
			quoted[i] = fmt.Sprintf("'%s'", t)
		}
		predicate := fmt.Sprintf(
			"request.headers['x-mcp-toolname'] in [%s]",
			strings.Join(quoted, ", "),
		)
		patterns = append(patterns, PatternExpression{Predicate: predicate})
	}

	if rule.Source.Type == agenticv1alpha1.SourceTypeOIDC && rule.Source.OIDC != nil {
		for claim, value := range rule.Source.OIDC.Claims {
			patterns = append(patterns, PatternExpression{
				Selector: fmt.Sprintf("auth.identity.%s", claim),
				Operator: "eq",
				Value:    value,
			})
		}
	}

	return AuthorizationRule{
		PatternMatching: &PatternMatchingRule{
			Patterns: patterns,
		},
	}
}
