package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPGatewayExtension_InternalHost(t *testing.T) {
	tests := []struct {
		name             string
		namespace        string
		targetRef        MCPGatewayExtensionTargetReference
		privateHost      string
		gatewayClassName string
		want             string
	}{
		{
			name:      "uses targetRef namespace when specified",
			namespace: "ext-namespace",
			targetRef: MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
			gatewayClassName: "istio",
			want:             "my-gateway-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name:      "falls back to extension namespace when targetRef namespace empty",
			namespace: "team-a",
			targetRef: MCPGatewayExtensionTargetReference{
				Name: "my-gateway",
			},
			gatewayClassName: "istio",
			want:             "my-gateway-istio.team-a.svc.cluster.local:8080",
		},
		{
			name:      "openshift-default gateway class uses correct service name",
			namespace: "mcp-system",
			targetRef: MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
			gatewayClassName: "openshift-default",
			want:             "my-gateway-openshift-default.gateway-system.svc.cluster.local:8080",
		},
		{
			name:      "privateHost overrides computed value",
			namespace: "team-a",
			targetRef: MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
			gatewayClassName: "istio",
			privateHost:      "custom-gateway.custom-ns.svc.cluster.local:9090",
			want:             "custom-gateway.custom-ns.svc.cluster.local:9090",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: tt.namespace,
				},
				Spec: MCPGatewayExtensionSpec{
					TargetRef:   tt.targetRef,
					PrivateHost: tt.privateHost,
				},
			}
			if got := m.InternalHost(8080, tt.gatewayClassName); got != tt.want {
				t.Errorf("InternalHost() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMCPGatewayExtension_HTTPRouteManagementDisabled(t *testing.T) {
	tests := []struct {
		name   string
		policy HTTPRouteManagementPolicy
		want   bool
	}{
		{
			name:   "empty string returns false",
			policy: "",
			want:   false,
		},
		{
			name:   "Enabled returns false",
			policy: HTTPRouteManagementEnabled,
			want:   false,
		},
		{
			name:   "Disabled returns true",
			policy: HTTPRouteManagementDisabled,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MCPGatewayExtension{
				Spec: MCPGatewayExtensionSpec{
					HTTPRouteManagement: tt.policy,
				},
			}
			if got := m.HTTPRouteDisabled(); got != tt.want {
				t.Errorf("HTTPRouteDisabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
