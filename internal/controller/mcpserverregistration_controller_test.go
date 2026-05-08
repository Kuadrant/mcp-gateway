package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestIsValidHostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		valid    bool
	}{
		// valid hostnames
		{"simple hostname", "example.com", true},
		{"subdomain", "api.example.com", true},
		{"deep subdomain", "a.b.c.example.com", true},
		{"with port", "example.com:443", true},
		{"localhost", "localhost", true},
		{"localhost with port", "localhost:8080", true},
		{"ipv4", "192.168.1.1", true},
		{"ipv4 with port", "192.168.1.1:443", true},
		{"ipv6 bracketed", "[::1]", true},
		{"ipv6 with port", "[::1]:443", true},
		{"ipv6 full", "[2001:db8::1]", true},

		// invalid - path injection
		{"path injection", "example.com/path", false},
		{"path injection with dotdot", "example.com/../etc/passwd", false},
		{"path in middle", "example.com/foo/bar", false},
		{"trailing slash", "example.com/", false},

		// invalid - userinfo injection
		{"userinfo", "user@example.com", false},
		{"userinfo with pass", "user:pass@example.com", false},

		// invalid - empty/malformed
		{"empty", "", false},
		{"just slash", "/", false},
		{"just path", "/path", false},
		{"query string", "example.com?foo=bar", false},
		{"fragment", "example.com#anchor", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidHostname(tt.hostname)
			if got != tt.valid {
				t.Errorf("isValidHostname(%q) = %v, want %v", tt.hostname, got, tt.valid)
			}
		})
	}
}

func TestDetermineProtocolFromGatewayListener(t *testing.T) {
	scheme := runtime.NewScheme()

	if err := gatewayv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add gateway api scheme: %v", err)
	}

	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}

	tests := []struct {
		name         string
		listenerName gatewayv1.SectionName
		protocol     gatewayv1.ProtocolType
		expected     string
	}{
		{
			name:         "https listener with arbitrary name",
			listenerName: "secure-listener",
			protocol:     gatewayv1.HTTPSProtocolType,
			expected:     "https",
		},
		{
			name:         "http listener with misleading name",
			listenerName: "https-looking-name",
			protocol:     gatewayv1.HTTPProtocolType,
			expected:     "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: gatewayv1.GatewaySpec{
					Listeners: []gatewayv1.Listener{
						{
							Name:     tt.listenerName,
							Port:     443,
							Protocol: tt.protocol,
						},
					},
				},
			}

			route := WrapHTTPRoute(&gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: "default",
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{
								Name:        gatewayv1.ObjectName("test-gateway"),
								SectionName: &tt.listenerName,
							},
						},
					},
				},
			})

			r := &MCPReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(gateway).
					Build(),
			}

			got := r.determineProtocol(
				context.Background(),
				route,
				&corev1.Service{},
				false,
			)

			if got != tt.expected {
				t.Fatalf("determineProtocol() = %s, want %s", got, tt.expected)
			}
		})
	}
}
