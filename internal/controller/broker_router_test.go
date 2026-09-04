package controller

import (
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func baseMCPExt() *mcpv1.MCPGatewayExtension {
	return &mcpv1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "default",
		},
		Spec: mcpv1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1.MCPGatewayExtensionTargetReference{
				Name:        "test-gateway",
				Namespace:   "default",
				SectionName: "https",
			},
		},
	}
}

func TestBuildGatewayHTTPRoute_RuleNames(t *testing.T) {
	tests := []struct {
		name              string
		supportsRuleNames bool
		wantRuleNames     bool
	}{
		{
			name:              "rule names set when supported",
			supportsRuleNames: true,
			wantRuleNames:     true,
		},
		{
			name:              "rule names omitted when not supported",
			supportsRuleNames: false,
			wantRuleNames:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				Scheme:            runtime.NewScheme(),
				supportsRuleNames: tt.supportsRuleNames,
			}
			route := r.buildGatewayHTTPRoute(baseMCPExt(), "mcp.example.com")

			if len(route.Spec.Rules) != 3 {
				t.Fatalf("expected 3 rules, got %d", len(route.Spec.Rules))
			}
			for i, rule := range route.Spec.Rules {
				hasName := rule.Name != nil
				if hasName != tt.wantRuleNames {
					t.Errorf("rule[%d].Name: got hasName=%v, want %v", i, hasName, tt.wantRuleNames)
				}
			}
		})
	}
}

func TestBuildTokensHTTPRoute_RuleNames(t *testing.T) {
	tests := []struct {
		name              string
		supportsRuleNames bool
		wantRuleNames     bool
	}{
		{
			name:              "rule names set when supported",
			supportsRuleNames: true,
			wantRuleNames:     true,
		},
		{
			name:              "rule names omitted when not supported",
			supportsRuleNames: false,
			wantRuleNames:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				Scheme:            runtime.NewScheme(),
				supportsRuleNames: tt.supportsRuleNames,
			}
			route := r.buildTokensHTTPRoute(baseMCPExt(), "mcp.example.com")

			for i, rule := range route.Spec.Rules {
				hasName := rule.Name != nil
				if hasName != tt.wantRuleNames {
					t.Errorf("rule[%d].Name: got hasName=%v, want %v", i, hasName, tt.wantRuleNames)
				}
			}
		})
	}
}

func TestHTTPRouteNeedsUpdate_NoRuleNames(t *testing.T) {
	pathType := gatewayv1.PathMatchPathPrefix
	mcpPath := "/mcp"
	route := func() *gatewayv1.HTTPRoute {
		return &gatewayv1.HTTPRoute{
			Spec: gatewayv1.HTTPRouteSpec{
				Rules: []gatewayv1.HTTPRouteRule{
					{
						Matches: []gatewayv1.HTTPRouteMatch{
							{Path: &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &mcpPath}},
						},
					},
				},
			},
		}
	}

	needsUpdate, reason := httpRouteNeedsUpdate(route(), route())
	if needsUpdate {
		t.Errorf("expected no update needed, got reason: %s", reason)
	}
}
