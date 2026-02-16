package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPGatewayExtension_PublicHost(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			name:        "nil annotations returns empty string",
			annotations: nil,
			want:        "",
		},
		{
			name:        "empty annotations returns empty string",
			annotations: map[string]string{},
			want:        "",
		},
		{
			name: "annotation not present returns empty string",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			want: "",
		},
		{
			name: "annotation present returns value",
			annotations: map[string]string{
				AnnotationPublicHost: "mcp.example.com",
			},
			want: "mcp.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
			}
			if got := m.PublicHost(); got != tt.want {
				t.Errorf("PublicHost() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMCPGatewayExtension_InternalHost(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		targetRef MCPGatewayExtensionTargetReference
		want      string
	}{
		{
			name:      "uses targetRef namespace when specified",
			namespace: "ext-namespace",
			targetRef: MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
			want: "my-gateway-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name:      "falls back to extension namespace when targetRef namespace empty",
			namespace: "team-a",
			targetRef: MCPGatewayExtensionTargetReference{
				Name: "my-gateway",
			},
			want: "my-gateway-istio.team-a.svc.cluster.local:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: tt.namespace,
				},
				Spec: MCPGatewayExtensionSpec{
					TargetRef: tt.targetRef,
				},
			}
			if got := m.InternalHost(); got != tt.want {
				t.Errorf("InternalHost() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMCPGatewayExtension_PollInterval(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{
			name:        "nil annotations returns empty string",
			annotations: nil,
			want:        "",
		},
		{
			name:        "empty annotations returns empty string",
			annotations: map[string]string{},
			want:        "",
		},
		{
			name: "annotation not present returns empty string",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			want: "",
		},
		{
			name: "annotation present returns value",
			annotations: map[string]string{
				AnnotationPollInterval: "30s",
			},
			want: "30s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
			}
			if got := m.PollInterval(); got != tt.want {
				t.Errorf("PollInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}
