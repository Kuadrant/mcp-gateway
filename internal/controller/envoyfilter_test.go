package controller

import (
	"testing"

	istiov1alpha3 "istio.io/api/networking/v1alpha3"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: true,
		},
		{
			name: "existing has extra user labels - still matches",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
				"user-label":                          "user-value",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: true,
		},
		{
			name: "extension name differs",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "old-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "new-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
		{
			name: "managed-by differs",
			existing: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        "other-controller",
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			desired: map[string]string{
				labelAppName:                          "mcp-gateway",
				labelManagedBy:                        labelManagedByValue,
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
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
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
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
				"mcp.kagenti.com/extension-name":      "test-ext",
				"mcp.kagenti.com/extension-namespace": "test-ns",
				"istio.io/rev":                        "default",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := managedLabelsMatch(tt.existing, tt.desired)
			if result != tt.expected {
				t.Errorf("managedLabelsMatch() = %v, expected %v", result, tt.expected)
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
					"mcp.kagenti.com/extension-name":      "test-ext",
					"mcp.kagenti.com/extension-namespace": "test-ns",
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
				ef.Labels["mcp.kagenti.com/extension-name"] = "different-ext"
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

			result := envoyFilterNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("envoyFilterNeedsUpdate() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
