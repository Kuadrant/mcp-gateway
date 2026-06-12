package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHTTPRouteWrapper_Validate(t *testing.T) {
	tests := []struct {
		name    string
		route   *gatewayv1.HTTPRoute
		wantErr bool
	}{
		{
			name: "valid route",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: "my-service",
								},
							},
						}},
					}},
				},
			},
			wantErr: false,
		},
		{
			name: "no rules",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules:     []gatewayv1.HTTPRouteRule{},
				},
			},
			wantErr: true,
		},
		{
			name: "no backend refs",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{},
					}},
				},
			},
			wantErr: true,
		},
		{
			name: "multiple rules unsupported",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{
						{BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc1"}}}}},
						{BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc2"}}}}},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no hostnames",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{Name: "my-service"},
							},
						}},
					}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty backend name",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{Name: ""},
							},
						}},
					}},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WrapHTTPRoute(tt.route)
			err := w.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPRouteWrapper_BackendType(t *testing.T) {
	kindHostname := gatewayv1.Kind("Hostname")
	kindService := gatewayv1.Kind("Service")
	groupIstio := gatewayv1.Group("networking.istio.io")
	groupEmpty := gatewayv1.Group("")

	tests := []struct {
		name            string
		kind            *gatewayv1.Kind
		group           *gatewayv1.Group
		wantHostname    bool
		wantService     bool
		wantBackendKind string
	}{
		{
			name:            "hostname backend",
			kind:            &kindHostname,
			group:           &groupIstio,
			wantHostname:    true,
			wantService:     false,
			wantBackendKind: "Hostname",
		},
		{
			name:            "explicit service backend",
			kind:            &kindService,
			group:           &groupEmpty,
			wantHostname:    false,
			wantService:     true,
			wantBackendKind: "Service",
		},
		{
			name:            "default service backend (nil kind)",
			kind:            nil,
			group:           nil,
			wantHostname:    false,
			wantService:     true,
			wantBackendKind: "Service",
		},
		{
			name:            "hostname kind but wrong group",
			kind:            &kindHostname,
			group:           &groupEmpty,
			wantHostname:    false,
			wantService:     false,
			wantBackendKind: "Hostname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &gatewayv1.HTTPRoute{
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name:  "backend",
									Kind:  tt.kind,
									Group: tt.group,
								},
							},
						}},
					}},
				},
			}

			w := WrapHTTPRoute(route)

			if got := w.IsHostnameBackend(); got != tt.wantHostname {
				t.Errorf("IsHostnameBackend() = %v, want %v", got, tt.wantHostname)
			}
			if got := w.IsServiceBackend(); got != tt.wantService {
				t.Errorf("IsServiceBackend() = %v, want %v", got, tt.wantService)
			}
			if got := w.BackendKind(); got != tt.wantBackendKind {
				t.Errorf("BackendKind() = %v, want %v", got, tt.wantBackendKind)
			}
		})
	}
}

func TestHTTPRouteWrapper_Accessors(t *testing.T) {
	ns := gatewayv1.Namespace("other-ns")
	port := gatewayv1.PortNumber(8080)

	tests := []struct {
		name              string
		route             *gatewayv1.HTTPRoute
		wantBackendName   string
		wantBackendNS     string
		wantFirstHostname string
		wantPort          *gatewayv1.PortNumber
	}{
		{
			name: "all fields set",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"api.example.com", "www.example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name:      "my-backend",
									Namespace: &ns,
									Port:      &port,
								},
							},
						}},
					}},
				},
			},
			wantBackendName:   "my-backend",
			wantBackendNS:     "other-ns",
			wantFirstHostname: "api.example.com",
			wantPort:          &port,
		},
		{
			name: "namespace defaults to route namespace",
			route: &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "route-ns"},
				Spec: gatewayv1.HTTPRouteSpec{
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: "my-service",
								},
							},
						}},
					}},
				},
			},
			wantBackendName:   "my-service",
			wantBackendNS:     "route-ns",
			wantFirstHostname: "example.com",
			wantPort:          nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WrapHTTPRoute(tt.route)

			if got := w.BackendName(); got != tt.wantBackendName {
				t.Errorf("BackendName() = %v, want %v", got, tt.wantBackendName)
			}
			if got := w.BackendNamespace(); got != tt.wantBackendNS {
				t.Errorf("BackendNamespace() = %v, want %v", got, tt.wantBackendNS)
			}
			if got := w.FirstHostname(); got != tt.wantFirstHostname {
				t.Errorf("FirstHostname() = %v, want %v", got, tt.wantFirstHostname)
			}
			if tt.wantPort == nil {
				if got := w.BackendPort(); got != nil {
					t.Errorf("BackendPort() = %v, want nil", got)
				}
			} else {
				if got := w.BackendPort(); got == nil || *got != *tt.wantPort {
					t.Errorf("BackendPort() = %v, want %v", got, *tt.wantPort)
				}
			}
		})
	}
}

func TestHTTPRouteWrapper_UsesHTTPS(t *testing.T) {
	httpsListener := gatewayv1.SectionName("mcp-tls")
	httpListener := gatewayv1.SectionName("mcp")

	gateway := &gatewayv1.Gateway{
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{
				{Name: "mcp-tls", Port: 8443, Protocol: gatewayv1.HTTPSProtocolType},
				{Name: "mcp", Port: 8080, Protocol: gatewayv1.HTTPProtocolType},
			},
		},
	}

	tests := []struct {
		name       string
		parentRefs []gatewayv1.ParentReference
		gateway    *gatewayv1.Gateway
		want       bool
	}{
		{
			name:       "no parent refs",
			parentRefs: nil,
			gateway:    gateway,
			want:       false,
		},
		{
			name: "section targets HTTPS listener",
			parentRefs: []gatewayv1.ParentReference{{
				SectionName: &httpsListener,
			}},
			gateway: gateway,
			want:    true,
		},
		{
			name: "section targets HTTP listener",
			parentRefs: []gatewayv1.ParentReference{{
				SectionName: &httpListener,
			}},
			gateway: gateway,
			want:    false,
		},
		{
			name: "nil section name",
			parentRefs: []gatewayv1.ParentReference{{
				SectionName: nil,
			}},
			gateway: gateway,
			want:    false,
		},
		{
			name: "nil gateway",
			parentRefs: []gatewayv1.ParentReference{{
				SectionName: &httpsListener,
			}},
			gateway: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &gatewayv1.HTTPRoute{
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: tt.parentRefs,
					},
					Hostnames: []gatewayv1.Hostname{"example.com"},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"},
							},
						}},
					}},
				},
			}

			w := WrapHTTPRoute(route)
			if got := w.UsesHTTPS(tt.gateway); got != tt.want {
				t.Errorf("UsesHTTPS() = %v, want %v", got, tt.want)
			}
		})
	}
}
