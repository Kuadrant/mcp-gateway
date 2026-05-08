package controller

import (
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	istiov1alpha3 "istio.io/api/networking/v1alpha3"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestManagedLabelsMatch(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]string
		desired  map[string]string
		expected bool
	}{
		{
			name: "all managed labels match",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: true,
		},
		{
			name: "existing has extra user labels - still matches",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
				"user-label":                          "user-value",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: true,
		},
		{
			name: "extension name differs",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "old-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "new-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
		{
			name: "managed-by differs",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        "other-controller",
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
		{
			name: "missing managed label in existing",
			existing: map[string]string{
				labelAppName:   "mcp-gateway",
				labelManagedBy: labelManagedByValue,
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
		{
			name:     "nil existing labels",
			existing: nil,
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kuadrant.io/extension-name":      "test-ext",
				"mcp.kuadrant.io/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := managedLabelsDiff(tt.existing, tt.desired)
			result := diff == "" // no diff means labels match
			if result != tt.expected {
				t.Errorf("managedLabelsDiff() returned %q, match=%v, expected match=%v", diff, result, tt.expected)
			}
		})
	}
}

func TestEnvoyFilterNeedsUpdate(t *testing.T) {
	baseEnvoyFilter := func() *istionetv1alpha3.EnvoyFilter {
		return &istionetv1alpha3.EnvoyFilter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-filter",
				Namespace: "gateway-system",
				Labels: map[string]string{
					labelAppName:                          "mcp-gateway",
					labelManagedBy:                        labelManagedByValue,
					"mcp.kuadrant.io/extension-name":      "test-ext",
					"mcp.kuadrant.io/extension-namespace": "test-ns",
					"istio.io/rev":                        "default",
				},
			},
			Spec: istiov1alpha3.EnvoyFilter{
				ConfigPatches: []*istiov1alpha3.EnvoyFilter_EnvoyConfigObjectPatch{
					{
						ApplyTo: istiov1alpha3.EnvoyFilter_HTTP_FILTER,
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		modify   func(ef *istionetv1alpha3.EnvoyFilter)
		expected bool
	}{
		{
			name:     "no changes",
			modify:   func(_ *istionetv1alpha3.EnvoyFilter) {},
			expected: false,
		},
		{
			name: "spec changed - different apply to",
			modify: func(ef *istionetv1alpha3.EnvoyFilter) {
				ef.Spec.ConfigPatches[0].ApplyTo = istiov1alpha3.EnvoyFilter_LISTENER
			},
			expected: true,
		},
		{
			name: "managed label changed",
			modify: func(ef *istionetv1alpha3.EnvoyFilter) {
				ef.Labels["mcp.kuadrant.io/extension-name"] = "different-ext"
			},
			expected: true,
		},
		{
			name: "user label added - no update needed",
			modify: func(ef *istionetv1alpha3.EnvoyFilter) {
				ef.Labels["user-custom-label"] = "user-value"
			},
			expected: false,
		},
		{
			name: "user label changed - no update needed",
			modify: func(ef *istionetv1alpha3.EnvoyFilter) {
				ef.Labels["user-custom-label"] = "changed-value"
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseEnvoyFilter()
			existing := baseEnvoyFilter()
			tt.modify(existing)

			result, reason := envoyFilterNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("envoyFilterNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}

func TestEnvoyFilterPatchOpTranslation(t *testing.T) {
	tests := []struct {
		name string
		op   mcpv1alpha1.EnvoyFilterPatchOperation
		want istiov1alpha3.EnvoyFilter_Patch_Operation
	}{
		{name: "default empty falls back to insert first", op: "", want: istiov1alpha3.EnvoyFilter_Patch_INSERT_FIRST},
		{name: "insert first", op: mcpv1alpha1.EnvoyFilterPatchInsertFirst, want: istiov1alpha3.EnvoyFilter_Patch_INSERT_FIRST},
		{name: "insert before", op: mcpv1alpha1.EnvoyFilterPatchInsertBefore, want: istiov1alpha3.EnvoyFilter_Patch_INSERT_BEFORE},
		{name: "insert after", op: mcpv1alpha1.EnvoyFilterPatchInsertAfter, want: istiov1alpha3.EnvoyFilter_Patch_INSERT_AFTER},
		{name: "unknown value falls back to insert first", op: "Bogus", want: istiov1alpha3.EnvoyFilter_Patch_INSERT_FIRST},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envoyFilterPatchOp(tt.op); got != tt.want {
				t.Errorf("envoyFilterPatchOp(%q) = %v, want %v", tt.op, got, tt.want)
			}
		})
	}
}

func TestEnvoyFilterDisabledAndPatchOpHelpers(t *testing.T) {
	tests := []struct {
		name         string
		spec         mcpv1alpha1.MCPGatewayExtensionSpec
		wantDisabled bool
		wantPatchOp  mcpv1alpha1.EnvoyFilterPatchOperation
	}{
		{
			name:         "no envoy filter config returns defaults",
			spec:         mcpv1alpha1.MCPGatewayExtensionSpec{},
			wantDisabled: false,
			wantPatchOp:  mcpv1alpha1.EnvoyFilterPatchInsertFirst,
		},
		{
			name: "management enabled keeps creation",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				EnvoyFilter: &mcpv1alpha1.EnvoyFilterConfig{
					Management: mcpv1alpha1.EnvoyFilterManagementEnabled,
				},
			},
			wantDisabled: false,
			wantPatchOp:  mcpv1alpha1.EnvoyFilterPatchInsertFirst,
		},
		{
			name: "management disabled opts out",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				EnvoyFilter: &mcpv1alpha1.EnvoyFilterConfig{
					Management: mcpv1alpha1.EnvoyFilterManagementDisabled,
				},
			},
			wantDisabled: true,
			wantPatchOp:  mcpv1alpha1.EnvoyFilterPatchInsertFirst,
		},
		{
			name: "patch operation override is honoured",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				EnvoyFilter: &mcpv1alpha1.EnvoyFilterConfig{
					PatchOperation: mcpv1alpha1.EnvoyFilterPatchInsertBefore,
				},
			},
			wantDisabled: false,
			wantPatchOp:  mcpv1alpha1.EnvoyFilterPatchInsertBefore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &mcpv1alpha1.MCPGatewayExtension{Spec: tt.spec}
			if got := ext.EnvoyFilterDisabled(); got != tt.wantDisabled {
				t.Errorf("EnvoyFilterDisabled() = %v, want %v", got, tt.wantDisabled)
			}
			if got := ext.EnvoyFilterPatchOp(); got != tt.wantPatchOp {
				t.Errorf("EnvoyFilterPatchOp() = %q, want %q", got, tt.wantPatchOp)
			}
		})
	}
}

func TestBuildEnvoyFilterRespectsPatchOperation(t *testing.T) {
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-gateway", Namespace: "gateway-system"},
	}
	listener := &mcpv1alpha1.ListenerConfig{Port: 8080, Hostname: "team-a.example.com", Name: "team-a-mcp"}

	tests := []struct {
		name string
		op   mcpv1alpha1.EnvoyFilterPatchOperation
		want istiov1alpha3.EnvoyFilter_Patch_Operation
	}{
		{name: "default insert first", op: "", want: istiov1alpha3.EnvoyFilter_Patch_INSERT_FIRST},
		{name: "explicit insert before", op: mcpv1alpha1.EnvoyFilterPatchInsertBefore, want: istiov1alpha3.EnvoyFilter_Patch_INSERT_BEFORE},
		{name: "explicit insert after", op: mcpv1alpha1.EnvoyFilterPatchInsertAfter, want: istiov1alpha3.EnvoyFilter_Patch_INSERT_AFTER},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ext", Namespace: "team-a"},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					EnvoyFilter: &mcpv1alpha1.EnvoyFilterConfig{PatchOperation: tt.op},
				},
			}
			if tt.op == "" {
				ext.Spec.EnvoyFilter = nil
			}

			r := &MCPGatewayExtensionReconciler{}
			ef, err := r.buildEnvoyFilter(ext, gateway, listener)
			if err != nil {
				t.Fatalf("buildEnvoyFilter returned error: %v", err)
			}
			if len(ef.Spec.ConfigPatches) != 1 {
				t.Fatalf("expected 1 config patch, got %d", len(ef.Spec.ConfigPatches))
			}
			if got := ef.Spec.ConfigPatches[0].Patch.Operation; got != tt.want {
				t.Errorf("patch operation = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnvoyFilterLabels_IstioRevInheritance(t *testing.T) {
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
		},
	}

	tests := []struct {
		name          string
		gatewayLabels map[string]string
		expectedRev   string
	}{
		{
			name:          "nil gateway uses default",
			gatewayLabels: nil,
			expectedRev:   "default",
		},
		{
			name:          "gateway without istio.io/rev uses default",
			gatewayLabels: map[string]string{"other-label": "value"},
			expectedRev:   "default",
		},
		{
			name:          "gateway with empty istio.io/rev uses default",
			gatewayLabels: map[string]string{labelIstioRev: ""},
			expectedRev:   "default",
		},
		{
			name:          "gateway with istio.io/rev inherits value",
			gatewayLabels: map[string]string{labelIstioRev: "1-20"},
			expectedRev:   "1-20",
		},
		{
			name:          "gateway with custom revision",
			gatewayLabels: map[string]string{labelIstioRev: "canary"},
			expectedRev:   "canary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gateway *gatewayv1.Gateway
			if tt.gatewayLabels != nil {
				gateway = &gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "test-gateway",
						Labels: tt.gatewayLabels,
					},
				}
			}

			labels := envoyFilterLabels(mcpExt, gateway)
			if labels[labelIstioRev] != tt.expectedRev {
				t.Errorf("envoyFilterLabels() istio.io/rev = %q, expected %q", labels[labelIstioRev], tt.expectedRev)
			}
		})
	}
}
