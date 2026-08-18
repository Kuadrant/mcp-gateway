package controller

import (
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

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestMcpsrReferencesSecret(t *testing.T) {
	tests := []struct {
		name       string
		secretName string
		credRef    *mcpv1.SecretReference
		caCertRef  *mcpv1.CACertSecretReference
		wantMatch  bool
	}{
		{
			name:       "matches caCertSecretRef",
			secretName: "my-ca",
			caCertRef:  &mcpv1.CACertSecretReference{Name: "my-ca", Key: "ca.crt"},
			wantMatch:  true,
		},
		{
			name:       "matches credentialRef",
			secretName: "my-cred",
			credRef:    &mcpv1.SecretReference{Name: "my-cred", Key: "token"},
			wantMatch:  true,
		},
		{
			name:       "matches either ref",
			secretName: "shared-secret",
			credRef:    &mcpv1.SecretReference{Name: "other"},
			caCertRef:  &mcpv1.CACertSecretReference{Name: "shared-secret"},
			wantMatch:  true,
		},
		{
			name:       "no match",
			secretName: "unrelated",
			credRef:    &mcpv1.SecretReference{Name: "my-cred"},
			caCertRef:  &mcpv1.CACertSecretReference{Name: "my-ca"},
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
			spec := mcpv1.MCPServerRegistrationSpec{
				CredentialRef:   tt.credRef,
				CACertSecretRef: tt.caCertRef,
			}
			if got := mcpsrReferencesSecret(spec, tt.secretName); got != tt.wantMatch {
				t.Errorf("mcpsrReferencesSecret() = %v, want %v", got, tt.wantMatch)
			}
		})
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

// leafCertPEM builds a cert like a server's own TLS cert: BasicConstraints is
// present and explicitly says IsCA=false. This is what someone gets by mistakenly
// pasting a leaf cert into ca.crt instead of the issuing CA.
func leafCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "my-service.example.com"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// legacyRootCertPEM builds a cert like an old-style self-signed root CA that never
// set the BasicConstraints extension at all. These must keep validating successfully.
func legacyRootCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Legacy Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: false,
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
		{
			name:    "leaf certificate explicitly not a CA",
			data:    leafCertPEM(t),
			wantErr: "not a CA certificate",
		},
		{
			name: "legacy root CA without BasicConstraints must still be accepted",
			data: legacyRootCertPEM(t),
		},
		{
			name:    "chain with valid CA followed by a leaf cert",
			data:    append(testCACertPEM(t), leafCertPEM(t)...),
			wantErr: "not a CA certificate",
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

func TestFindOldestMCPServerRegistration(t *testing.T) {
	newReg := func(name string, created time.Time) mcpv1.MCPServerRegistration {
		return mcpv1.MCPServerRegistration{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				CreationTimestamp: metav1.NewTime(created),
			},
		}
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		regs []mcpv1.MCPServerRegistration
		want string
	}{
		{
			name: "two registrations, first is older",
			regs: []mcpv1.MCPServerRegistration{
				newReg("first", base),
				newReg("second", base.Add(time.Hour)),
			},
			want: "first",
		},
		{
			name: "two registrations, second is older",
			regs: []mcpv1.MCPServerRegistration{
				newReg("first", base.Add(time.Hour)),
				newReg("second", base),
			},
			want: "second",
		},
		{
			name: "three registrations, oldest in the middle",
			regs: []mcpv1.MCPServerRegistration{
				newReg("newest", base.Add(2*time.Hour)),
				newReg("oldest", base),
				newReg("middle", base.Add(time.Hour)),
			},
			want: "oldest",
		},
		{
			name: "single registration",
			regs: []mcpv1.MCPServerRegistration{
				newReg("only", base),
			},
			want: "only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findOldestMCPServerRegistration(tt.regs)
			if got.Name != tt.want {
				t.Errorf("findOldestMCPServerRegistration() = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

// TestFindOldestMCPServerRegistration_TieBreakIsSymmetric guards against the real
// failure mode this tie-break exists to prevent: CreationTimestamp only has second
// granularity, so two registrations created in the same second compare equal there.
// checkPrefixConflict always puts the currently-reconciling registration at index 0
// of the comparison slice, so if a tie resolved by index alone, EACH registration's
// own reconcile would see itself as oldest and both would reach Ready - the exact
// collision this check exists to prevent. The winner must be the same regardless of
// which registration is asking (i.e. which one is at index 0).
func TestFindOldestMCPServerRegistration_TieBreakIsSymmetric(t *testing.T) {
	sameInstant := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	a := mcpv1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "aaaa", CreationTimestamp: sameInstant},
	}
	b := mcpv1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "bbbb", CreationTimestamp: sameInstant},
	}

	fromA := findOldestMCPServerRegistration([]mcpv1.MCPServerRegistration{a, b})
	fromB := findOldestMCPServerRegistration([]mcpv1.MCPServerRegistration{b, a})

	if fromA.UID != fromB.UID {
		t.Fatalf("tie-break is not symmetric: asking as %q picked %q, asking as %q picked %q",
			a.Name, fromA.Name, b.Name, fromB.Name)
	}
	if fromA.UID != a.UID {
		t.Errorf("expected the lower UID (%q) to win the tie, got %q", a.UID, fromA.UID)
	}
}
