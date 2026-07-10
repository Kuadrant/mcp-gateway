package controller

import (
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHostnameMatches(t *testing.T) {
	tests := []struct {
		name             string
		routeHostname    string
		listenerHostname string
		want             bool
	}{
		{
			name:             "exact match",
			routeHostname:    "team-a.example.com",
			listenerHostname: "team-a.example.com",
			want:             true,
		},
		{
			name:             "wildcard match",
			routeHostname:    "server1.team-a.mcp.local",
			listenerHostname: "*.team-a.mcp.local",
			want:             true,
		},
		{
			name:             "wildcard no match different domain",
			routeHostname:    "server1.team-b.mcp.local",
			listenerHostname: "*.team-a.mcp.local",
			want:             false,
		},
		{
			name:             "wildcard no match nested subdomain",
			routeHostname:    "a.b.team-a.mcp.local",
			listenerHostname: "*.team-a.mcp.local",
			want:             false,
		},
		{
			name:             "no match different hostnames",
			routeHostname:    "team-a.example.com",
			listenerHostname: "team-b.example.com",
			want:             false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostnameMatches(tt.routeHostname, tt.listenerHostname); got != tt.want {
				t.Errorf("hostnameMatches(%q, %q) = %v, want %v", tt.routeHostname, tt.listenerHostname, got, tt.want)
			}
		})
	}
}

func testGateway() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-gateway",
			Namespace: "gateway-system",
		},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{
				{
					Name:     "team-a-mcp",
					Port:     8080,
					Hostname: ptr.To(gatewayv1.Hostname("team-a.example.com")),
				},
				{
					Name:     "team-a-mcps",
					Port:     8080,
					Hostname: ptr.To(gatewayv1.Hostname("*.team-a.mcp.local")),
				},
				{
					Name:     "team-b-mcp",
					Port:     8081,
					Hostname: ptr.To(gatewayv1.Hostname("team-b.example.com")),
				},
				{
					Name:     "team-b-mcps",
					Port:     8081,
					Hostname: ptr.To(gatewayv1.Hostname("*.team-b.mcp.local")),
				},
			},
		},
	}
}

func testExtension(sectionName string) *mcpv1.MCPGatewayExtension {
	return &mcpv1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ext", Namespace: "team-a"},
		Spec: mcpv1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1.MCPGatewayExtensionTargetReference{
				Group:       "gateway.networking.k8s.io",
				Kind:        "Gateway",
				Name:        "shared-gateway",
				Namespace:   "gateway-system",
				SectionName: sectionName,
			},
		},
	}
}

func TestHTTPRouteAttachesToListener(t *testing.T) {
	gw := testGateway()

	tests := []struct {
		name  string
		route *gatewayv1.HTTPRoute
		ext   *mcpv1.MCPGatewayExtension
		want  bool
	}{
		{
			name: "sectionName matches same port listener",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-a"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:        "shared-gateway",
							Namespace:   ptr.To(gatewayv1.Namespace("gateway-system")),
							SectionName: ptr.To(gatewayv1.SectionName("team-a-mcps")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"server1.team-a.mcp.local"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: true,
		},
		{
			name: "sectionName on different port does not match",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-b"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:        "shared-gateway",
							Namespace:   ptr.To(gatewayv1.Namespace("gateway-system")),
							SectionName: ptr.To(gatewayv1.SectionName("team-b-mcps")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"server1.team-b.mcp.local"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: false,
		},
		{
			name: "no sectionName hostname matches wildcard listener",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-a"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      "shared-gateway",
							Namespace: ptr.To(gatewayv1.Namespace("gateway-system")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"server1.team-a.mcp.local"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: true,
		},
		{
			name: "no sectionName hostname matches exact listener",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-a"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      "shared-gateway",
							Namespace: ptr.To(gatewayv1.Namespace("gateway-system")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"team-a.example.com"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: true,
		},
		{
			name: "no sectionName hostname does not match other team",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-b"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      "shared-gateway",
							Namespace: ptr.To(gatewayv1.Namespace("gateway-system")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"server1.team-b.mcp.local"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: false,
		},
		{
			name: "parentRef targets different gateway",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-a"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      "other-gateway",
							Namespace: ptr.To(gatewayv1.Namespace("gateway-system")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"team-a.example.com"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: false,
		},
		{
			name: "extension targets nonexistent listener",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "team-a"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name:      "shared-gateway",
							Namespace: ptr.To(gatewayv1.Namespace("gateway-system")),
						}},
					},
					Hostnames: []gatewayv1.Hostname{"team-a.example.com"},
				},
			},
			ext:  testExtension("nonexistent"),
			want: false,
		},
		{
			name: "parentRef namespace defaults to route namespace",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "gateway-system"},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{{
							Name: "shared-gateway",
							// no namespace - defaults to route namespace
						}},
					},
					Hostnames: []gatewayv1.Hostname{"team-a.example.com"},
				},
			},
			ext:  testExtension("team-a-mcp"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := httpRouteAttachesToListener(tt.route, gw, tt.ext); got != tt.want {
				t.Errorf("httpRouteAttachesToListener() = %v, want %v", got, tt.want)
			}
		})
	}
}
