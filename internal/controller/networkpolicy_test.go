package controller

import (
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestBuildBrokerRouterNetworkPolicy(t *testing.T) {
	tests := []struct {
		name              string
		extNamespace      string
		targetRefName     string
		targetRefNS       string
		wantGatewayNSName string
	}{
		{
			name:              "uses targetRef namespace when specified",
			extNamespace:      "team-a",
			targetRefName:     "my-gateway",
			targetRefNS:       "gateway-system",
			wantGatewayNSName: "gateway-system",
		},
		{
			name:              "falls back to extension namespace when targetRef namespace empty",
			extNamespace:      "team-a",
			targetRefName:     "my-gateway",
			targetRefNS:       "",
			wantGatewayNSName: "team-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{}
			mcpExt := &mcpv1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: tt.extNamespace,
				},
				Spec: mcpv1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1.MCPGatewayExtensionTargetReference{
						Name:      tt.targetRefName,
						Namespace: tt.targetRefNS,
					},
				},
			}

			np := r.buildBrokerRouterNetworkPolicy(mcpExt)

			if np.Namespace != tt.extNamespace {
				t.Errorf("namespace = %q, want %q", np.Namespace, tt.extNamespace)
			}
			if np.Name != brokerRouterName {
				t.Errorf("name = %q, want %q", np.Name, brokerRouterName)
			}
			if !equality.Semantic.DeepEqual(np.Spec.PodSelector.MatchLabels, brokerRouterLabels()) {
				t.Errorf("podSelector.matchLabels = %v, want %v", np.Spec.PodSelector.MatchLabels, brokerRouterLabels())
			}
			wantPolicyTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
			if !equality.Semantic.DeepEqual(np.Spec.PolicyTypes, wantPolicyTypes) {
				t.Errorf("policyTypes = %v, want %v", np.Spec.PolicyTypes, wantPolicyTypes)
			}

			if len(np.Spec.Ingress) != 2 {
				t.Fatalf("expected 2 ingress rules, got %d", len(np.Spec.Ingress))
			}

			// ingress rule 0: gateway namespace only, ports 8080 + 50051
			rule0 := np.Spec.Ingress[0]
			if len(rule0.From) != 1 || rule0.From[0].NamespaceSelector == nil {
				t.Fatalf("expected ingress[0].From to have a namespaceSelector, got %+v", rule0.From)
			}
			wantNSSelector := map[string]string{"kubernetes.io/metadata.name": tt.wantGatewayNSName}
			if !equality.Semantic.DeepEqual(rule0.From[0].NamespaceSelector.MatchLabels, wantNSSelector) {
				t.Errorf("ingress[0] namespaceSelector = %v, want %v", rule0.From[0].NamespaceSelector.MatchLabels, wantNSSelector)
			}
			if len(rule0.Ports) != 2 {
				t.Fatalf("expected 2 ports on ingress[0], got %d", len(rule0.Ports))
			}
			wantHTTPPort := intstr.FromInt(brokerHTTPPort)
			wantGRPCPort := intstr.FromInt(brokerGRPCPort)
			if rule0.Ports[0].Protocol == nil || *rule0.Ports[0].Protocol != corev1.ProtocolTCP || rule0.Ports[0].Port == nil || *rule0.Ports[0].Port != wantHTTPPort {
				t.Errorf("ingress[0].Ports[0] = %+v, want TCP/%v", rule0.Ports[0], wantHTTPPort)
			}
			if rule0.Ports[1].Protocol == nil || *rule0.Ports[1].Protocol != corev1.ProtocolTCP || rule0.Ports[1].Port == nil || *rule0.Ports[1].Port != wantGRPCPort {
				t.Errorf("ingress[0].Ports[1] = %+v, want TCP/%v", rule0.Ports[1], wantGRPCPort)
			}

			// ingress rule 1: no From (all sources), metrics port
			rule1 := np.Spec.Ingress[1]
			if len(rule1.From) != 0 {
				t.Errorf("expected ingress[1].From to be empty (all sources), got %+v", rule1.From)
			}
			if len(rule1.Ports) != 1 {
				t.Fatalf("expected 1 port on ingress[1], got %d", len(rule1.Ports))
			}
			wantMetricsPort := intstr.FromInt(brokerMetricsPort)
			if rule1.Ports[0].Protocol == nil || *rule1.Ports[0].Protocol != corev1.ProtocolTCP || rule1.Ports[0].Port == nil || *rule1.Ports[0].Port != wantMetricsPort {
				t.Errorf("ingress[1].Ports[0] = %+v, want TCP/%v", rule1.Ports[0], wantMetricsPort)
			}

			// egress: allow-all (single empty rule)
			if len(np.Spec.Egress) != 1 {
				t.Fatalf("expected 1 egress rule (allow-all), got %d", len(np.Spec.Egress))
			}
			if !equality.Semantic.DeepEqual(np.Spec.Egress[0], networkingv1.NetworkPolicyEgressRule{}) {
				t.Errorf("expected egress[0] to be empty (allow-all), got %+v", np.Spec.Egress[0])
			}
		})
	}
}

func TestNetworkPolicyNeedsUpdate(t *testing.T) {
	baseNetworkPolicy := func() *networkingv1.NetworkPolicy {
		tcp := corev1.ProtocolTCP
		httpPort := intstr.FromInt(brokerHTTPPort)
		grpcPort := intstr.FromInt(brokerGRPCPort)
		metricsPort := intstr.FromInt(brokerMetricsPort)
		return &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      brokerRouterName,
				Namespace: "default",
				Labels:    brokerRouterLabels(),
			},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: brokerRouterLabels(),
				},
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress,
					networkingv1.PolicyTypeEgress,
				},
				Ingress: []networkingv1.NetworkPolicyIngressRule{
					{
						From: []networkingv1.NetworkPolicyPeer{
							{
								NamespaceSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"kubernetes.io/metadata.name": "gateway-system",
									},
								},
							},
						},
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &tcp, Port: &httpPort},
							{Protocol: &tcp, Port: &grpcPort},
						},
					},
					{
						Ports: []networkingv1.NetworkPolicyPort{
							{Protocol: &tcp, Port: &metricsPort},
						},
					},
				},
				Egress: []networkingv1.NetworkPolicyEgressRule{{}},
			},
		}
	}

	tests := []struct {
		name     string
		modify   func(np *networkingv1.NetworkPolicy)
		expected bool
	}{
		{
			name:     "no changes",
			modify:   func(_ *networkingv1.NetworkPolicy) {},
			expected: false,
		},
		{
			name: "podSelector changed",
			modify: func(np *networkingv1.NetworkPolicy) {
				np.Spec.PodSelector.MatchLabels["extra"] = "label"
			},
			expected: true,
		},
		{
			name: "policyTypes changed",
			modify: func(np *networkingv1.NetworkPolicy) {
				np.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
			},
			expected: true,
		},
		{
			name: "ingress changed",
			modify: func(np *networkingv1.NetworkPolicy) {
				np.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] = "other-namespace"
			},
			expected: true,
		},
		{
			name: "egress changed",
			modify: func(np *networkingv1.NetworkPolicy) {
				np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{}
			},
			expected: true,
		},
		{
			name: "labels changed",
			modify: func(np *networkingv1.NetworkPolicy) {
				np.Labels["extra"] = "label"
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseNetworkPolicy()
			existing := baseNetworkPolicy()
			tt.modify(existing)

			result, reason := networkPolicyNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("networkPolicyNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}
