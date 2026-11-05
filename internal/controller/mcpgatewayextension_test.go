package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/redisconfig"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestValidateSessionStore(t *testing.T) {
	const privateValue = "private-test-credential"
	tests := []struct {
		name          string
		data          map[string]string
		noStore       bool
		missingSecret bool
		missingLabel  bool
		wantReason    string
		wantMode      string
	}{
		{
			name:    "no configured store",
			noStore: true,
		},
		{
			name:          "missing secret",
			missingSecret: true,
			wantReason:    mcpv1.ConditionReasonSecretNotFound,
		},
		{
			name:         "unlabelled secret",
			missingLabel: true,
			data:         map[string]string{"CACHE_CONNECTION_STRING": "redis://redis:6379/0"},
			wantReason:   mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "no secret data",
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "legacy standalone missing URL",
			data:       map[string]string{"unrelated": "value"},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "standalone empty URL",
			data:       map[string]string{"CACHE_MODE": "standalone", "CACHE_CONNECTION_STRING": ""},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:     "legacy standalone URL",
			data:     map[string]string{"CACHE_CONNECTION_STRING": "redis://user:" + privateValue + "@redis:6379/2?pool_size=5"},
			wantMode: "standalone",
		},
		{
			name: "explicit standalone ignores unused HA values",
			data: map[string]string{
				"CACHE_MODE":              "standalone",
				"CACHE_CONNECTION_STRING": "redis://redis:6379/0",
				"CACHE_SENTINEL_ADDRS":    privateValue,
				"CACHE_CLUSTER_ADDRS":     privateValue,
			},
			wantMode: "standalone",
		},
		{
			name: "sentinel without standalone URL",
			data: map[string]string{
				"CACHE_MODE":              "sentinel",
				"CACHE_SENTINEL_MASTER":   "mymaster",
				"CACHE_SENTINEL_ADDRS":    "sentinel-0:26379,sentinel-1:26379",
				"CACHE_SENTINEL_USERNAME": "sentinel-user",
				"CACHE_SENTINEL_PASSWORD": privateValue,
				"CACHE_USERNAME":          "redis-user",
				"CACHE_PASSWORD":          privateValue,
				"CACHE_DB":                "2",
				"CACHE_TLS_ENABLED":       "true",
			},
			wantMode: "sentinel",
		},
		{
			name: "cluster without standalone URL",
			data: map[string]string{
				"CACHE_MODE":          "cluster",
				"CACHE_CLUSTER_ADDRS": "redis-0:6379,redis-1:6379",
				"CACHE_DB":            "0",
			},
			wantMode: "cluster",
		},
		{
			name:       "unknown mode",
			data:       map[string]string{"CACHE_MODE": privateValue},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "invalid standalone URL is redacted",
			data:       map[string]string{"CACHE_CONNECTION_STRING": "redis://user:" + privateValue + "@redis:invalid/0"},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "sentinel missing master",
			data: map[string]string{
				"CACHE_MODE":           "sentinel",
				"CACHE_SENTINEL_ADDRS": "sentinel:26379",
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "sentinel missing addresses",
			data: map[string]string{
				"CACHE_MODE":            "sentinel",
				"CACHE_SENTINEL_MASTER": "mymaster",
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name:       "cluster missing addresses",
			data:       map[string]string{"CACHE_MODE": "cluster"},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "invalid sentinel address is redacted",
			data: map[string]string{
				"CACHE_MODE":            "sentinel",
				"CACHE_SENTINEL_MASTER": "mymaster",
				"CACHE_SENTINEL_ADDRS":  "redis://user:" + privateValue + "@sentinel:26379",
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "sentinel rejects negative database",
			data: map[string]string{
				"CACHE_MODE":            "sentinel",
				"CACHE_SENTINEL_MASTER": "mymaster",
				"CACHE_SENTINEL_ADDRS":  "sentinel:26379",
				"CACHE_DB":              "-1",
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "cluster rejects nonzero database",
			data: map[string]string{
				"CACHE_MODE":          "cluster",
				"CACHE_CLUSTER_ADDRS": "redis:6379",
				"CACHE_DB":            "1",
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
		{
			name: "invalid TLS value is redacted",
			data: map[string]string{
				"CACHE_MODE":          "cluster",
				"CACHE_CLUSTER_ADDRS": "redis:6379",
				"CACHE_TLS_ENABLED":   privateValue,
			},
			wantReason: mcpv1.ConditionReasonSecretInvalid,
		},
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := baseMCPExt()
			if !tt.noStore {
				ext.Spec.SessionStore = &mcpv1.SessionStore{SecretName: "redis-session"}
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: "redis-session", Namespace: ext.Namespace,
					Labels: map[string]string{ManagedSecretLabel: ManagedSecretValue},
				},
			}
			if tt.missingLabel {
				secret.Labels = nil
			}
			if tt.data != nil {
				secret.Data = make(map[string][]byte, len(tt.data))
				for key, value := range tt.data {
					secret.Data[key] = []byte(value)
				}
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if !tt.missingSecret {
				builder.WithObjects(secret)
			}
			var logs bytes.Buffer
			r := &MCPGatewayExtensionReconciler{DirectAPIReader: builder.Build(), log: slog.New(slog.NewTextHandler(&logs, nil))}
			err := r.validateSessionStore(context.Background(), ext)
			if strings.Contains(logs.String(), privateValue) {
				t.Fatalf("log leaked a secret value: %s", logs.String())
			}
			if tt.wantReason != "" {
				var valErr *validationError
				if !errors.As(err, &valErr) {
					t.Fatalf("expected validation error, got %T", err)
				}
				if valErr.reason != tt.wantReason {
					t.Errorf("reason = %q, want %q", valErr.reason, tt.wantReason)
				}
				if strings.Contains(err.Error(), privateValue) {
					t.Error("validation error contains secret data")
				}
				return
			}
			if err != nil {
				t.Fatal("valid session store rejected")
			}
			if tt.noStore {
				return
			}

			// Resolve the generated cache environment as Kubernetes would: absent
			// mode-specific keys must not prevent the container from starting.
			deployment := r.buildBrokerRouterDeployment(ext, "mcp.example.com", "gateway:8080")
			resolved := make(map[string]string)
			for _, env := range deployment.Spec.Template.Spec.Containers[0].Env {
				if !strings.HasPrefix(env.Name, "CACHE_") {
					continue
				}
				if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					t.Fatalf("%s must use a SecretKeyRef rather than expose a value", env.Name)
				}
				ref := env.ValueFrom.SecretKeyRef
				if ref.Name != secret.Name {
					t.Fatalf("%s references a different secret", env.Name)
				}
				value, present := secret.Data[ref.Key]
				if !present && (ref.Optional == nil || !*ref.Optional) {
					t.Fatalf("missing %s would block pod startup", ref.Key)
				}
				resolved[env.Name] = string(value)
			}
			config, err := redisconfig.Parse(func(key string) string { return resolved[key] })
			if err != nil {
				t.Fatal("generated pod environment is not a valid Redis configuration")
			}
			if config.Mode != tt.wantMode {
				t.Errorf("pod Redis mode = %q, want %q", config.Mode, tt.wantMode)
			}
		})
	}
}
