package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

func TestMcpsrReferencesSecret(t *testing.T) {
	tests := []struct {
		name       string
		secretName string
		credRef    *mcpv1alpha1.SecretReference
		caCertRef  *mcpv1alpha1.CACertSecretReference
		wantMatch  bool
	}{
		{
			name:       "matches caCertSecretRef",
			secretName: "my-ca",
			caCertRef:  &mcpv1alpha1.CACertSecretReference{Name: "my-ca", Key: "ca.crt"},
			wantMatch:  true,
		},
		{
			name:       "matches credentialRef",
			secretName: "my-cred",
			credRef:    &mcpv1alpha1.SecretReference{Name: "my-cred", Key: "token"},
			wantMatch:  true,
		},
		{
			name:       "matches either ref",
			secretName: "shared-secret",
			credRef:    &mcpv1alpha1.SecretReference{Name: "other"},
			caCertRef:  &mcpv1alpha1.CACertSecretReference{Name: "shared-secret"},
			wantMatch:  true,
		},
		{
			name:       "no match",
			secretName: "unrelated",
			credRef:    &mcpv1alpha1.SecretReference{Name: "my-cred"},
			caCertRef:  &mcpv1alpha1.CACertSecretReference{Name: "my-ca"},
			wantMatch:  false,
		},
		{
			name:       "nil refs",
			secretName: "any",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mcpv1alpha1.MCPServerRegistrationSpec{
				CredentialRef:   tt.credRef,
				CACertSecretRef: tt.caCertRef,
			}
			if got := mcpsrReferencesSecret(spec, tt.secretName); got != tt.wantMatch {
				t.Errorf("mcpsrReferencesSecret() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestGetTargetHTTPRouteUsesTargetRefNamespace(t *testing.T) {
	tests := []struct {
		name          string
		objects       []client.Object
		targetNS      string
		wantNamespace string
		wantErr       string
	}{
		{
			name: "uses targetRef namespace when ReferenceGrant allows it",
			objects: []client.Object{
				testHTTPRoute("registrations"),
				testHTTPRoute("routes"),
				testMCPServerReferenceGrant(),
			},
			targetNS:      "routes",
			wantNamespace: "routes",
		},
		{
			name: "uses registration namespace when targetRef namespace is empty",
			objects: []client.Object{
				testHTTPRoute("registrations"),
			},
			wantNamespace: "registrations",
		},
		{
			name: "requires ReferenceGrant for cross namespace route",
			objects: []client.Object{
				testHTTPRoute("routes"),
			},
			targetNS: "routes",
			wantErr:  "ReferenceGrant required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testScheme(t)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.objects...).
				Build()

			r := &MCPReconciler{Client: fakeClient}
			mcpsr := &mcpv1alpha1.MCPServerRegistration{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server",
					Namespace: "registrations",
				},
				Spec: mcpv1alpha1.MCPServerRegistrationSpec{
					TargetRef: mcpv1alpha1.TargetReference{
						Name:      "target-route",
						Namespace: tt.targetNS,
					},
				},
			}

			got, err := r.getTargetHTTPRoute(context.Background(), mcpsr)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("getTargetHTTPRoute() expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("getTargetHTTPRoute() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("getTargetHTTPRoute() unexpected error: %v", err)
			}
			if got.Namespace != tt.wantNamespace {
				t.Errorf("getTargetHTTPRoute() namespace = %q, want %q", got.Namespace, tt.wantNamespace)
			}
		})
	}
}

func TestUpdateHTTPRouteStatusRequiresReferenceGrantForCrossNamespaceCleanup(t *testing.T) {
	routeStatus := []gatewayv1.RouteParentStatus{
		{
			ControllerName: gatewayv1.GatewayController("test.example.com/gateway-controller"),
			ParentRef: gatewayv1.ParentReference{
				Name: gatewayv1.ObjectName("gateway"),
			},
			Conditions: []metav1.Condition{
				{
					Type:   "Programmed",
					Status: metav1.ConditionTrue,
					Reason: "InUseByMCPServerRegistration",
				},
			},
		},
	}

	tests := []struct {
		name           string
		objects        []client.Object
		wantConditions int
	}{
		{
			name: "removes stale status when ReferenceGrant is missing",
			objects: []client.Object{
				testHTTPRouteWithStatus("routes", routeStatus),
			},
			wantConditions: 0,
		},
		{
			name: "removes status when ReferenceGrant allows it",
			objects: []client.Object{
				testHTTPRouteWithStatus("routes", routeStatus),
				testMCPServerReferenceGrant(),
			},
			wantConditions: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testScheme(t)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.objects...).
				WithStatusSubresource(&gatewayv1.HTTPRoute{}).
				Build()

			now := metav1.Now()
			mcpsr := &mcpv1alpha1.MCPServerRegistration{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "server",
					Namespace:         "registrations",
					DeletionTimestamp: &now,
				},
				Spec: mcpv1alpha1.MCPServerRegistrationSpec{
					TargetRef: mcpv1alpha1.TargetReference{
						Kind:      "HTTPRoute",
						Name:      "target-route",
						Namespace: "routes",
					},
				},
			}

			r := &MCPReconciler{Client: fakeClient}
			if err := r.updateHTTPRouteStatus(context.Background(), mcpsr); err != nil {
				t.Fatalf("updateHTTPRouteStatus() unexpected error: %v", err)
			}

			got := &gatewayv1.HTTPRoute{}
			if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "target-route", Namespace: "routes"}, got); err != nil {
				t.Fatalf("failed to get HTTPRoute: %v", err)
			}
			if len(got.Status.Parents[0].Conditions) != tt.wantConditions {
				t.Fatalf("conditions = %v, want %d conditions", got.Status.Parents[0].Conditions, tt.wantConditions)
			}
		})
	}
}

func TestFindMCPServerRegistrationsForReferenceGrant(t *testing.T) {
	scheme := testScheme(t)

	mcpsr := &mcpv1alpha1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server",
			Namespace: "registrations",
		},
		Spec: mcpv1alpha1.MCPServerRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{
				Kind:      "HTTPRoute",
				Name:      "target-route",
				Namespace: "routes",
			},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpsr).
		WithIndex(&mcpv1alpha1.MCPServerRegistration{}, MCPServerRegistrationReferenceGrantIndex, func(obj client.Object) []string {
			mcpsr := obj.(*mcpv1alpha1.MCPServerRegistration)
			return []string{mcpServerRegistrationToRefGrantIndexValue(*mcpsr)}
		}).
		Build()

	r := &MCPReconciler{Client: fakeClient}
	requests := r.findMCPServerRegistrationsForReferenceGrant(context.Background(),
		testMCPServerReferenceGrant())

	if len(requests) != 1 {
		t.Fatalf("requests = %v, want one request", requests)
	}
	if requests[0].NamespacedName != client.ObjectKeyFromObject(mcpsr) {
		t.Fatalf("request = %v, want %v", requests[0].NamespacedName, client.ObjectKeyFromObject(mcpsr))
	}
}

func TestReconcileRemovesConfigWhenReferenceGrantIsMissing(t *testing.T) {
	scheme := testScheme(t)

	mcpsr := &mcpv1alpha1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "server",
			Namespace:  "registrations",
			Finalizers: []string{mcpGatewayFinalizer},
		},
		Spec: mcpv1alpha1.MCPServerRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{
				Kind:      "HTTPRoute",
				Name:      "target-route",
				Namespace: "routes",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpsr, testHTTPRoute("routes")).
		WithStatusSubresource(&mcpv1alpha1.MCPServerRegistration{}).
		Build()

	writer := &testMCPServerConfigReaderWriter{}
	r := &MCPReconciler{
		Client:             fakeClient,
		ConfigReaderWriter: writer,
	}
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "server", Namespace: "registrations"},
	})
	if err == nil {
		t.Fatal("Reconcile() expected error")
	}
	if !strings.Contains(err.Error(), "ReferenceGrant required") {
		t.Fatalf("Reconcile() error = %v, want ReferenceGrant required", err)
	}
	if len(writer.removedServers) != 1 || writer.removedServers[0] != "registrations/server" {
		t.Fatalf("removedServers = %v, want registrations/server", writer.removedServers)
	}

	got := &mcpv1alpha1.MCPServerRegistration{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "server", Namespace: "registrations"}, got); err != nil {
		t.Fatalf("failed to get MCPServerRegistration: %v", err)
	}
	condition := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if condition == nil || condition.Reason != mcpv1alpha1.ConditionReasonRefGrantRequired {
		t.Fatalf("Ready condition = %v, want reason %q", condition, mcpv1alpha1.ConditionReasonRefGrantRequired)
	}
}

type testMCPServerConfigReaderWriter struct {
	removedServers []string
}

func (w *testMCPServerConfigReaderWriter) UpsertMCPServer(context.Context, config.MCPServer, types.NamespacedName) error {
	return nil
}

func (w *testMCPServerConfigReaderWriter) RemoveMCPServer(_ context.Context, serverName string) error {
	w.removedServers = append(w.removedServers, serverName)
	return nil
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := gatewayv1beta1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func testHTTPRoute(namespace string) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target-route",
			Namespace: namespace,
		},
	}
}

func testHTTPRouteWithStatus(namespace string, status []gatewayv1.RouteParentStatus) *gatewayv1.HTTPRoute {
	httpRoute := testHTTPRoute(namespace)
	httpRoute.Status.Parents = status
	return httpRoute
}

func testMCPServerReferenceGrant() *gatewayv1beta1.ReferenceGrant {
	routeName := gatewayv1beta1.ObjectName("target-route")
	return &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-target-route",
			Namespace: "routes",
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     gatewayv1beta1.Group(mcpv1alpha1.GroupVersion.Group),
					Kind:      "MCPServerRegistration",
					Namespace: "registrations",
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{
					Group: gatewayv1beta1.Group(gatewayv1.GroupVersion.Group),
					Kind:  "HTTPRoute",
					Name:  &routeName,
				},
			},
		},
	}
}

func testCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestValidateCACertPEM(t *testing.T) {
	validPEM := testCACertPEM(t)

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{
			name: "valid single cert",
			data: validPEM,
		},
		{
			name: "valid chain",
			data: append(validPEM, testCACertPEM(t)...),
		},
		{
			name:    "not PEM at all",
			data:    []byte("this is not PEM data"),
			wantErr: "no valid PEM certificate blocks found",
		},
		{
			name:    "empty",
			data:    []byte{},
			wantErr: "no valid PEM certificate blocks found",
		},
		{
			name:    "wrong block type",
			data:    pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("fake")}),
			wantErr: "unexpected PEM block type",
		},
		{
			name:    "corrupt certificate DER",
			data:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-valid-der")}),
			wantErr: "failed to parse certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCACertPEM(tt.data)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateCACertPEM() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("validateCACertPEM() expected error containing %q, got nil", tt.wantErr)
				} else if got := err.Error(); !strings.Contains(got, tt.wantErr) {
					t.Errorf("validateCACertPEM() error = %q, want substring %q", got, tt.wantErr)
				}
			}
		})
	}
}

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

// TestDetermineProtocol_InternalServiceAlwaysHTTP is a regression test:
// the upstream protocol for internal services must always be http, regardless
// of the gateway listener protocol. The gateway listener (HTTP vs HTTPS) only
// affects the hairpin path, not the broker→upstream connection. TLS upstreams
// are handled separately via caCertSecretRef in buildMCPServerConfig.
func TestDetermineProtocol_InternalServiceAlwaysHTTP(t *testing.T) {
	r := &MCPReconciler{}
	route := WrapHTTPRoute(&gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					SectionName: ptrTo(gatewayv1.SectionName("mcp-tls")),
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "my-server",
							Port: ptrTo(gatewayv1.PortNumber(9090)),
						},
					},
				}},
			}},
		},
	})
	svc := &corev1.Service{
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 9090}},
		},
	}

	got := r.determineProtocol(route, svc, false)
	if got != "http" {
		t.Errorf("determineProtocol() = %q for internal service, want %q — "+
			"gateway listener protocol must not affect upstream URL scheme", got, "http")
	}
}

func ptrTo[T any](v T) *T { return &v }
