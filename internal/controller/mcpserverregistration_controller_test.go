package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestDesiredRouteParents(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	accepted := metav1.Condition{
		Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Accepted",
		ObservedGeneration: 3, LastTransitionTime: now, Message: "route accepted",
	}
	resolved := metav1.Condition{
		Type: "ResolvedRefs", Status: metav1.ConditionTrue, Reason: "ResolvedRefs",
		ObservedGeneration: 3, LastTransitionTime: now, Message: "references resolved",
	}
	programmed := metav1.Condition{
		Type: "Programmed", Status: metav1.ConditionTrue, Reason: "InUseByMCPServerRegistration",
		ObservedGeneration: 3, LastTransitionTime: now,
		Message: "HTTPRoute is referenced by at least one MCPServerRegistration",
	}
	foreignReason := programmed
	foreignReason.Reason = "Programmed"
	foreignMessage := programmed
	foreignMessage.Message = "route programmed by another controller"
	parent := gatewayv1.ParentReference{Name: "gateway"}
	status := func(ref gatewayv1.ParentReference, controller gatewayv1.GatewayController, conditions ...metav1.Condition) gatewayv1.RouteParentStatus {
		return gatewayv1.RouteParentStatus{ParentRef: ref, ControllerName: controller, Conditions: conditions}
	}
	istio := status(parent, "istio.io/gateway-controller", accepted, resolved)
	mcp := status(parent, MCPControllerName, programmed)
	legacyIstio := status(parent, istio.ControllerName, programmed, accepted, resolved)
	orphan := status(parent, "kuadrant.io/policy-controller", programmed)
	otherController := status(parent, "example.com/gateway-controller", accepted)
	mcpWithExtra := status(parent, MCPControllerName, programmed, resolved)
	oldMCP := *mcp.DeepCopy()
	oldMCP.Conditions[0].LastTransitionTime = metav1.NewTime(now.Add(-time.Hour))
	newGenerationMCP := *oldMCP.DeepCopy()
	newGenerationMCP.Conditions[0].ObservedGeneration = 4
	defaultedParent := gatewayv1.ParentReference{
		Name: parent.Name, Group: ptrTo(gatewayv1.Group(gatewayv1.GroupName)),
		Kind: ptrTo(gatewayv1.Kind("Gateway")), Namespace: ptrTo(gatewayv1.Namespace("routes")),
	}
	serviceParent := gatewayv1.ParentReference{
		Name: parent.Name, Group: ptrTo(gatewayv1.Group("")), Kind: ptrTo(gatewayv1.Kind("Service")),
	}
	implicitGroupServiceParent := *serviceParent.DeepCopy()
	implicitGroupServiceParent.Group = nil
	crossNamespaceParent := gatewayv1.ParentReference{Name: parent.Name, Namespace: ptrTo(gatewayv1.Namespace("other"))}
	sectionParent := gatewayv1.ParentReference{Name: parent.Name, SectionName: ptrTo(gatewayv1.SectionName("https"))}
	portParent := gatewayv1.ParentReference{Name: parent.Name, Port: ptrTo(gatewayv1.PortNumber(443))}
	otherParent := gatewayv1.ParentReference{Name: "other-gateway"}
	listenerParent := gatewayv1.ParentReference{
		Name: parent.Name, SectionName: ptrTo(gatewayv1.SectionName("https")), Port: ptrTo(gatewayv1.PortNumber(443)),
	}
	notAccepted := accepted
	notAccepted.Status = metav1.ConditionFalse
	unknownAccepted := accepted
	unknownAccepted.Status = metav1.ConditionUnknown

	tests := []struct {
		name       string
		parents    []gatewayv1.RouteParentStatus
		generation int64
		present    bool
		want       []gatewayv1.RouteParentStatus
	}{
		{
			name: "input not mutated", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{legacyIstio, oldMCP},
			want:    []gatewayv1.RouteParentStatus{istio, oldMCP},
		},
		{
			name: "round-trip idempotence", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, oldMCP},
			want:    []gatewayv1.RouteParentStatus{istio, oldMCP},
		},
		{
			name: "accepted foreign entry preserved", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio},
			want:    []gatewayv1.RouteParentStatus{istio, mcp},
		},
		{
			name: "MCP entry already present", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, mcp},
			want:    []gatewayv1.RouteParentStatus{istio, mcp},
		},
		{
			name: "remove MCP entry", generation: 3,
			parents: []gatewayv1.RouteParentStatus{istio, mcp},
			want:    []gatewayv1.RouteParentStatus{istio},
		},
		{
			name: "migrate legacy Programmed", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{legacyIstio},
			want:    []gatewayv1.RouteParentStatus{istio, mcp},
		},
		{
			name: "remove Kuadrant orphan", generation: 3,
			parents: []gatewayv1.RouteParentStatus{orphan},
			want:    []gatewayv1.RouteParentStatus{},
		},
		{
			name: "retain foreign Programmed with different reason", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, accepted, resolved, foreignReason)},
			want:    []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, accepted, resolved, foreignReason), mcp},
		},
		{
			name: "retain foreign Programmed with different message", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, otherController.ControllerName, foreignMessage)},
			want:    []gatewayv1.RouteParentStatus{status(parent, otherController.ControllerName, foreignMessage)},
		},
		{
			name: "retain foreign-only Programmed during deletion", generation: 3,
			parents: []gatewayv1.RouteParentStatus{status(parent, otherController.ControllerName, foreignReason)},
			want:    []gatewayv1.RouteParentStatus{status(parent, otherController.ControllerName, foreignReason)},
		},
		{
			name: "deduplicate accepted parentRefs", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, otherController},
			want:    []gatewayv1.RouteParentStatus{istio, otherController, mcp},
		},
		{
			name: "deduplicate defaulted parentRefs", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(defaultedParent, otherController.ControllerName, accepted)},
			want:    []gatewayv1.RouteParentStatus{istio, status(defaultedParent, otherController.ControllerName, accepted), mcp},
		},
		{
			name: "reuse existing defaulted parentRef", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(defaultedParent, MCPControllerName, programmed)},
			want:    []gatewayv1.RouteParentStatus{istio, status(defaultedParent, MCPControllerName, programmed)},
		},
		{
			name: "retain unrelated MCP condition on removal", generation: 3,
			parents: []gatewayv1.RouteParentStatus{istio, mcpWithExtra},
			want:    []gatewayv1.RouteParentStatus{istio, status(parent, MCPControllerName, resolved)},
		},
		{
			name: "core Service differs from Gateway", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(serviceParent, otherController.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				istio, status(serviceParent, otherController.ControllerName, accepted), mcp,
				status(serviceParent, MCPControllerName, programmed),
			},
		},
		{
			name: "explicit core group differs from omitted group", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{
				status(implicitGroupServiceParent, istio.ControllerName, accepted), status(serviceParent, otherController.ControllerName, accepted),
			},
			want: []gatewayv1.RouteParentStatus{
				status(implicitGroupServiceParent, istio.ControllerName, accepted), status(serviceParent, otherController.ControllerName, accepted),
				status(implicitGroupServiceParent, MCPControllerName, programmed), status(serviceParent, MCPControllerName, programmed),
			},
		},
		{
			name: "different parent namespaces", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(crossNamespaceParent, istio.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				istio, status(crossNamespaceParent, istio.ControllerName, accepted), mcp, status(crossNamespaceParent, MCPControllerName, programmed),
			},
		},
		{
			name: "different parent sections", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(sectionParent, istio.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				istio, status(sectionParent, istio.ControllerName, accepted), mcp, status(sectionParent, MCPControllerName, programmed),
			},
		},
		{
			name: "different parent ports", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(portParent, istio.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				istio, status(portParent, istio.ControllerName, accepted), mcp, status(portParent, MCPControllerName, programmed),
			},
		},
		{
			name: "nil input", generation: 3, present: true,
			want: []gatewayv1.RouteParentStatus{},
		},
		{
			name: "empty input", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{},
			want:    []gatewayv1.RouteParentStatus{},
		},
		{
			name: "generation bump preserves transition time", generation: 4, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, oldMCP},
			want:    []gatewayv1.RouteParentStatus{istio, newGenerationMCP},
		},
		{
			name: "two accepted gateways", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{istio, status(otherParent, istio.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				istio, status(otherParent, istio.ControllerName, accepted), mcp, status(otherParent, MCPControllerName, programmed),
			},
		},
		{
			name: "preserve sectionName and port", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(listenerParent, istio.ControllerName, accepted)},
			want: []gatewayv1.RouteParentStatus{
				status(listenerParent, istio.ControllerName, accepted), status(listenerParent, MCPControllerName, programmed),
			},
		},
		{
			name: "foreign parent without Accepted", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, resolved)},
			want:    []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, resolved)},
		},
		{
			name: "foreign parent with Accepted false", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, notAccepted)},
			want:    []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, notAccepted)},
		},
		{
			name: "foreign parent with Accepted unknown", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, unknownAccepted)},
			want:    []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, unknownAccepted)},
		},
		{
			name: "remove MCP entry for no longer accepted parent", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, notAccepted), mcp},
			want:    []gatewayv1.RouteParentStatus{status(parent, istio.ControllerName, notAccepted)},
		},
		{
			name: "retain unrelated MCP condition for no longer accepted parent", generation: 3, present: true,
			parents: []gatewayv1.RouteParentStatus{mcpWithExtra},
			want:    []gatewayv1.RouteParentStatus{status(parent, MCPControllerName, resolved)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var snapshot []gatewayv1.RouteParentStatus
			if tt.parents != nil {
				snapshot = make([]gatewayv1.RouteParentStatus, len(tt.parents))
				for i := range tt.parents {
					snapshot[i] = *tt.parents[i].DeepCopy()
				}
			}
			got := desiredRouteParents(tt.parents, "routes", tt.generation, now, tt.present)
			if !reflect.DeepEqual(tt.parents, snapshot) {
				t.Errorf("input mutated: got %#v, want %#v", tt.parents, snapshot)
			}
			if got == nil {
				t.Error("desiredRouteParents() returned nil")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("desiredRouteParents() = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if len(got[i].Conditions) == 0 {
					t.Errorf("parent %d has empty conditions", i)
				}
			}
			if again := desiredRouteParents(got, "routes", tt.generation, metav1.NewTime(now.Add(time.Hour)), tt.present); !reflect.DeepEqual(again, got) {
				t.Errorf("repeat call changed output: got %#v, want %#v", again, got)
			}
		})
	}
}

func TestAcceptedParentRefs(t *testing.T) {
	parents := []gatewayv1.RouteParentStatus{
		{
			ParentRef: gatewayv1.ParentReference{Name: "foreign-gateway"}, ControllerName: "istio.io/gateway-controller",
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}},
		},
		{
			ParentRef: gatewayv1.ParentReference{Name: "own-gateway"}, ControllerName: MCPControllerName,
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}},
		},
	}
	want := []gatewayv1.ParentReference{parents[0].ParentRef}
	if got := acceptedParentRefs(parents); !reflect.DeepEqual(got, want) {
		t.Errorf("acceptedParentRefs() = %#v, want %#v", got, want)
	}
}

func TestMcpsrReferencesSecret(t *testing.T) {
	tests := []struct {
		name       string
		secretName string
		credRef    *mcpv1.SecretReference
		caCertRef  *mcpv1.CACertSecretReference
		oauth2Ref  *mcpv1.OAuth2ClientCredentialsConfig
		wantMatch  bool
	}{
		{
			name:       "matches caCertSecretRef",
			secretName: "my-ca",
			caCertRef:  &mcpv1.CACertSecretReference{Name: "my-ca", Key: "ca.crt"},
			wantMatch:  true,
		},
		{
			name:       "matches oauth2ClientCredentials secretRef",
			secretName: "my-oauth-client",
			oauth2Ref: &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "https://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "my-oauth-client"},
			},
			wantMatch: true,
		},
		{
			name:       "no match with oauth2ClientCredentials set",
			secretName: "unrelated",
			oauth2Ref: &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "https://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "my-oauth-client"},
			},
			wantMatch: false,
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
				CredentialRef:           tt.credRef,
				CACertSecretRef:         tt.caCertRef,
				OAuth2ClientCredentials: tt.oauth2Ref,
			}
			if got := mcpsrReferencesSecret(spec, tt.secretName); got != tt.wantMatch {
				t.Errorf("mcpsrReferencesSecret() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestResolveOAuth2ClientCredentials(t *testing.T) {
	const clientSecretValue = "s3cr3t-value-that-must-never-leak"
	const authServerURL = "https://as.example.com/token"

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	oauth2Spec := func(secretName string, scopes ...string) *mcpv1.OAuth2ClientCredentialsConfig {
		return &mcpv1.OAuth2ClientCredentialsConfig{
			TokenURL:  authServerURL,
			SecretRef: mcpv1.ClientCredentialsSecretReference{Name: secretName},
			Scopes:    scopes,
		}
	}
	oauth2Secret := func(name string, labels map[string]string, data map[string][]byte) corev1.Secret {
		return corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns", Labels: labels},
			Data:       data,
		}
	}
	labeled := map[string]string{ManagedSecretLabel: ManagedSecretValue}
	bothKeys := map[string][]byte{
		oauth2ClientIDKey:     []byte("mcp-broker"),
		oauth2ClientSecretKey: []byte(clientSecretValue),
	}

	tests := []struct {
		name        string
		oauth2      *mcpv1.OAuth2ClientCredentialsConfig
		secrets     []corev1.Secret
		want        *config.OAuth2ClientCredentials
		errContains string
	}{
		{
			name:        "secret not found",
			oauth2:      oauth2Spec("missing"),
			errContains: "secret missing not found",
		},
		{
			name:        "missing required label",
			oauth2:      oauth2Spec("unlabeled"),
			secrets:     []corev1.Secret{oauth2Secret("unlabeled", nil, bothKeys)},
			errContains: "missing required label mcp.kuadrant.io/secret=true",
		},
		{
			name:   "missing clientID",
			oauth2: oauth2Spec("no-id"),
			secrets: []corev1.Secret{oauth2Secret("no-id", labeled, map[string][]byte{
				oauth2ClientSecretKey: []byte(clientSecretValue),
			})},
			errContains: "missing or empty key clientID",
		},
		{
			name:   "missing clientSecret",
			oauth2: oauth2Spec("no-secret"),
			secrets: []corev1.Secret{oauth2Secret("no-secret", labeled, map[string][]byte{
				oauth2ClientIDKey: []byte("mcp-broker"),
			})},
			errContains: "missing or empty key clientSecret",
		},
		{
			name:   "empty clientID",
			oauth2: oauth2Spec("empty-id"),
			secrets: []corev1.Secret{oauth2Secret("empty-id", labeled, map[string][]byte{
				oauth2ClientIDKey:     {},
				oauth2ClientSecretKey: []byte(clientSecretValue),
			})},
			errContains: "missing or empty key clientID",
		},
		{
			name:   "empty clientSecret",
			oauth2: oauth2Spec("empty-secret"),
			secrets: []corev1.Secret{oauth2Secret("empty-secret", labeled, map[string][]byte{
				oauth2ClientIDKey:     []byte("mcp-broker"),
				oauth2ClientSecretKey: {},
			})},
			errContains: "missing or empty key clientSecret",
		},
		{
			name:    "resolved with scopes",
			oauth2:  oauth2Spec("oauth-client", "mcp.read", "mcp.write"),
			secrets: []corev1.Secret{oauth2Secret("oauth-client", labeled, bothKeys)},
			want: &config.OAuth2ClientCredentials{
				TokenURL:     authServerURL,
				ClientID:     "mcp-broker",
				ClientSecret: clientSecretValue,
				Scopes:       []string{"mcp.read", "mcp.write"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, len(tt.secrets))
			for i := range tt.secrets {
				objs[i] = &tt.secrets[i]
			}
			fc := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
			r := &MCPReconciler{DirectAPIReader: fc}

			mcpsr := &mcpv1.MCPServerRegistration{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "test-ns"},
				Spec:       mcpv1.MCPServerRegistrationSpec{OAuth2ClientCredentials: tt.oauth2},
			}

			got, err := r.resolveOAuth2ClientCredentials(t.Context(), mcpsr)
			if tt.errContains != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err, tt.errContains)
				}
				// the error becomes the Ready condition message verbatim
				if strings.Contains(err.Error(), clientSecretValue) {
					t.Fatal("error message leaks the client secret")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
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

// TestDetermineProtocol is a regression test: the upstream scheme comes from the
// backend service port, not the gateway listener protocol. A port with appProtocol
// or name "https" is a TLS upstream; everything else is http. The gateway listener
// (HTTP vs HTTPS) only affects the hairpin path, never the broker→upstream URL.
func TestDetermineProtocol(t *testing.T) {
	// route always targets the HTTPS listener to prove the listener protocol
	// does not bleed into the upstream scheme.
	newRoute := func(port int32) *HTTPRouteWrapper {
		return WrapHTTPRoute(&gatewayv1.HTTPRoute{
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
								Port: ptrTo(port),
							},
						},
					}},
				}},
			},
		})
	}

	tests := []struct {
		name string
		port corev1.ServicePort
		want string
	}{
		{"unnamed port defaults to http", corev1.ServicePort{Port: 9090}, "http"},
		{"port named http", corev1.ServicePort{Port: 9090, Name: "http"}, "http"},
		{"port named https", corev1.ServicePort{Port: 8443, Name: "https"}, "https"},
		{"appProtocol https", corev1.ServicePort{Port: 8443, AppProtocol: ptrTo("https")}, "https"},
	}

	r := &MCPReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{tt.port}}}
			got := r.determineProtocol(newRoute(tt.port.Port), svc)
			if got != tt.want {
				t.Errorf("determineProtocol() = %q, want %q", got, tt.want)
			}
		})
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

func TestParseGuardrailsConfigIDs(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        []string
	}{
		{name: "nil annotations"},
		{name: "unset", annotations: map[string]string{}},
		{
			name:        "comma separated",
			annotations: map[string]string{ManagedGuardrailsAnnotation: "strict-input-checking,pii-detection"},
			want:        []string{"strict-input-checking", "pii-detection"},
		},
		{
			name:        "trims spaces and drops empties",
			annotations: map[string]string{ManagedGuardrailsAnnotation: " a, ,b "},
			want:        []string{"a", "b"},
		},
		{
			name:        "present but no usable IDs",
			annotations: map[string]string{ManagedGuardrailsAnnotation: " , "},
			want:        []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGuardrailsConfigIDs(tt.annotations)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") || (got == nil) != (tt.want == nil) {
				t.Fatalf("parseGuardrailsConfigIDs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRequireGatewayGuardrails(t *testing.T) {
	t.Run("ok when every extension has guardrails-ref", func(t *testing.T) {
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "ns",
				Annotations: map[string]string{labelGuardrailsReference: "rails"},
			},
		}}, []string{"strict"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ok without annotation when no per-server IDs", func(t *testing.T) {
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{{
			ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"},
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("error when annotation is present but has no IDs", func(t *testing.T) {
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "ns",
				Annotations: map[string]string{labelGuardrailsReference: "rails"},
			},
		}}, []string{})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("error when an extension has no guardrails-ref", func(t *testing.T) {
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{{
			ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"},
		}}, []string{"strict"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("error when guardrails secret is not found even without per-server IDs", func(t *testing.T) {
		ext := &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "ns",
				Annotations: map[string]string{labelGuardrailsReference: "rails"},
			},
		}
		ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretNotFound, "guardrails secret rails not found")
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{ext}, nil)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "guardrails secret rails not found") {
			t.Fatalf("error %q does not include the GuardrailsResolved condition message", err.Error())
		}
	})
	t.Run("error when guardrails secret is invalid even without per-server IDs", func(t *testing.T) {
		ext := &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "ns",
				Annotations: map[string]string{labelGuardrailsReference: "rails"},
			},
		}
		ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretInvalid, "guardrails secret rails missing required label")
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{ext}, nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("ok when Ready is false for an unrelated reason; per-server IDs still allowed", func(t *testing.T) {
		ext := &mcpv1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "ns",
				Annotations: map[string]string{labelGuardrailsReference: "rails"},
			},
		}
		// GuardrailsResolved is left untouched (unset) - an unrelated Ready
		// failure (e.g. a bad CA bundle secret) must not block a
		// registration that only depends on guardrails being resolved.
		ext.SetReadyCondition(metav1.ConditionFalse, mcpv1.ConditionReasonSecretInvalid, "unrelated CA bundle secret invalid")
		err := requireGatewayGuardrails([]*mcpv1.MCPGatewayExtension{ext}, []string{"strict"})
		if err != nil {
			t.Fatal(err)
		}
	})

}
