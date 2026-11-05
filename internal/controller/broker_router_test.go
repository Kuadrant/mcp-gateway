package controller

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/redisconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestReconcileBrokerRouterSessionStoreTransition(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, addToScheme := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		networkingv1.AddToScheme,
		mcpv1.AddToScheme,
		gatewayv1.Install,
	} {
		if err := addToScheme(scheme); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name       string
		secretName string
	}{
		{name: "removing the store"},
		{name: "replacing the secret", secretName: "new-redis-session"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const privateValue = "old-private-test-credential"
			var logs bytes.Buffer
			r := &MCPGatewayExtensionReconciler{
				Scheme:            scheme,
				BrokerRouterImage: DefaultBrokerRouterImage,
				log:               slog.New(slog.NewTextHandler(&logs, nil)),
			}
			ext := baseMCPExt()
			ext.Spec.SessionStore = &mcpv1.SessionStore{SecretName: "old-redis-session"}
			listener := &ListenerConfig{Hostname: "mcp.example.com", Port: 8080, Protocol: "HTTP"}
			privateHost := derivePrivateHost(ext, listener, "istio")
			existing := r.buildBrokerRouterDeployment(ext, listener.Hostname, privateHost)
			userEnv := corev1.EnvVar{Name: "MY_CUSTOM_VAR", Value: "preserved"}
			existing.Spec.Template.Spec.Containers[0].Env = append(existing.Spec.Template.Spec.Containers[0].Env, userEnv)
			// Reconcile a manually supplied password alongside old Secret refs.
			// Neither the updated pod template nor update logs may retain it.
			for i := range existing.Spec.Template.Spec.Containers[0].Env {
				env := &existing.Spec.Template.Spec.Containers[0].Env[i]
				if env.Name == "CACHE_PASSWORD" {
					env.ValueFrom = nil
					env.Value = privateValue
				}
			}
			r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
			if tt.secretName == "" {
				ext.Spec.SessionStore = nil
			} else {
				ext.Spec.SessionStore.SecretName = tt.secretName
			}

			if _, err := r.reconcileBrokerRouter(context.Background(), ext, listener, "istio"); err != nil {
				t.Fatalf("reconcileBrokerRouter() failed: %v", err)
			}
			updated := &appsv1.Deployment{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(existing), updated); err != nil {
				t.Fatal(err)
			}
			seen := make(map[string]int)
			userPreserved := false
			for _, env := range updated.Spec.Template.Spec.Containers[0].Env {
				if env.Name == userEnv.Name {
					userPreserved = equality.Semantic.DeepEqual(env, userEnv)
				}
				if !slices.Contains(redisconfig.Keys, env.Name) {
					continue
				}
				if tt.secretName == "" {
					t.Errorf("%s remains after removing the store", env.Name)
					continue
				}
				seen[env.Name]++
				if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					t.Errorf("%s retains a literal value instead of using the replacement secret", env.Name)
					continue
				}
				if env.ValueFrom.SecretKeyRef.Name != tt.secretName {
					t.Errorf("%s still references the old secret", env.Name)
				}
			}
			if !userPreserved {
				t.Error("reconciling the store changed unrelated user environment")
			}
			if tt.secretName != "" {
				for _, key := range redisconfig.Keys {
					if seen[key] != 1 {
						t.Errorf("%s occurs %d times after secret replacement, want once", key, seen[key])
					}
				}
			}
			if strings.Contains(logs.String(), privateValue) {
				t.Error("reconciliation logs contain a cache credential")
			}
			desired := r.buildBrokerRouterDeployment(ext, listener.Hostname, privateHost)
			if needsUpdate, _ := deploymentNeedsUpdate(desired, updated); needsUpdate {
				t.Error("session store reconciliation did not converge")
			}
		})
	}
}
