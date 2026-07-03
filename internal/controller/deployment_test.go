package controller

import (
	"slices"
	"strings"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestDeploymentNeedsUpdate(t *testing.T) {
	baseDeployment := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "test-container",
								Image:   "test-image:v1",
								Command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--mcp-gateway-public-host=test.example.com"},
								Ports: []corev1.ContainerPort{
									{Name: "http", ContainerPort: 8080},
									{Name: "grpc", ContainerPort: 50051},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "config-volume", MountPath: "/config"},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "config-volume",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: "config-secret",
									},
								},
							},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		modify   func(d *appsv1.Deployment)
		expected bool
	}{
		{
			name:     "no changes",
			modify:   func(_ *appsv1.Deployment) {},
			expected: false,
		},
		{
			name: "image changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Image = "test-image:v2"
			},
			expected: true,
		},
		{
			name: "managed flag changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:9090", "--mcp-gateway-public-host=test.example.com"}
			},
			expected: true,
		},
		{
			name: "user-added flag does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--discovery-tools-enabled=false",
				)
			},
			expected: false,
		},
		{
			name: "managed log-level flag added triggers update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--log-level=-4",
				)
			},
			expected: true,
		},
		{
			name: "managed flag added triggers update",
			modify: func(d *appsv1.Deployment) {
				// remove --mcp-gateway-public-host from existing to simulate a new managed flag
				var cmd []string
				for _, arg := range d.Spec.Template.Spec.Containers[0].Command {
					if !strings.HasPrefix(arg, "--mcp-gateway-public-host=") {
						cmd = append(cmd, arg)
					}
				}
				d.Spec.Template.Spec.Containers[0].Command = cmd
			},
			expected: true,
		},
		{
			name: "port changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort = 9090
			},
			expected: true,
		},
		{
			name: "port added",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Ports = append(
					d.Spec.Template.Spec.Containers[0].Ports,
					corev1.ContainerPort{Name: "metrics", ContainerPort: 9090},
				)
			},
			expected: true,
		},
		{
			name: "managed volume mount changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath = "/new-config"
			},
			expected: true,
		},
		{
			name: "user-added volume does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Volumes = append(
					d.Spec.Template.Spec.Volumes,
					corev1.Volume{
						Name: "ca-cert",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "my-ca"},
						},
					},
				)
			},
			expected: false,
		},
		{
			name: "user-added volume mount does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].VolumeMounts = append(
					d.Spec.Template.Spec.Containers[0].VolumeMounts,
					corev1.VolumeMount{Name: "ca-cert", MountPath: "/certs"},
				)
			},
			expected: false,
		},
		{
			name: "managed volume secret name changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Volumes[0].Secret.SecretName = "new-secret"
			},
			expected: true,
		},
		{
			name: "user flag cache-connection-string does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--cache-connection-string=redis://localhost:6379",
				)
			},
			expected: false,
		},
		{
			name: "user flag session-length does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--session-length=3600",
				)
			},
			expected: false,
		},
		{
			name: "arbitrary user flag does not trigger update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--some-custom-flag=value",
				)
			},
			expected: false,
		},
		{
			name: "env var added",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
					{
						Name: "TRUSTED_HEADER_PUBLIC_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-secret",
								},
								Key: "key",
							},
						},
					},
				}
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseDeployment()
			existing := baseDeployment()
			tt.modify(existing)

			result, reason := deploymentNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("deploymentNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}

func TestDeploymentNeedsUpdate_EmptyContainers(t *testing.T) {
	desired := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
		},
	}
	existing := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test"},
					},
				},
			},
		},
	}

	if needsUpdate, _ := deploymentNeedsUpdate(desired, existing); needsUpdate {
		t.Error("deploymentNeedsUpdate() should return false when desired has no containers")
	}

	if needsUpdate, _ := deploymentNeedsUpdate(existing, desired); needsUpdate {
		t.Error("deploymentNeedsUpdate() should return false when existing has no containers")
	}
}

func TestServiceNeedsUpdate(t *testing.T) {
	baseService := func() *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-service",
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": "mcp-gateway",
				},
				Ports: []corev1.ServicePort{
					{
						Name:       "http",
						Port:       8080,
						TargetPort: intstr.FromInt(8080),
					},
					{
						Name:       "grpc",
						Port:       50051,
						TargetPort: intstr.FromInt(50051),
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		modify   func(s *corev1.Service)
		expected bool
	}{
		{
			name:     "no changes",
			modify:   func(_ *corev1.Service) {},
			expected: false,
		},
		{
			name: "port number changed",
			modify: func(s *corev1.Service) {
				s.Spec.Ports[0].Port = 9090
			},
			expected: true,
		},
		{
			name: "target port changed",
			modify: func(s *corev1.Service) {
				s.Spec.Ports[0].TargetPort = intstr.FromInt(9090)
			},
			expected: true,
		},
		{
			name: "port added",
			modify: func(s *corev1.Service) {
				s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{
					Name:       "metrics",
					Port:       9090,
					TargetPort: intstr.FromInt(9090),
				})
			},
			expected: true,
		},
		{
			name: "port removed",
			modify: func(s *corev1.Service) {
				s.Spec.Ports = s.Spec.Ports[:1]
			},
			expected: true,
		},
		{
			name: "selector changed",
			modify: func(s *corev1.Service) {
				s.Spec.Selector["app"] = "different-app"
			},
			expected: true,
		},
		{
			name: "selector key added",
			modify: func(s *corev1.Service) {
				s.Spec.Selector["version"] = "v1"
			},
			expected: true,
		},
		{
			name: "port name changed",
			modify: func(s *corev1.Service) {
				s.Spec.Ports[0].Name = "web"
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseService()
			existing := baseService()
			tt.modify(existing)

			result, reason := serviceNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("serviceNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_PublicHost(t *testing.T) {
	tests := []struct {
		name       string
		publicHost string
		wantFlag   string
	}{
		{
			name:       "annotation overrides listener hostname",
			publicHost: "override.example.com",
			wantFlag:   "--mcp-gateway-public-host=override.example.com",
		},
		{
			name:       "uses listener hostname",
			publicHost: "listener.example.com",
			wantFlag:   "--mcp-gateway-public-host=listener.example.com",
		},
		{
			name:       "wildcard resolved to mcp subdomain",
			publicHost: "mcp.example.com",
			wantFlag:   "--mcp-gateway-public-host=mcp.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage: "test-image:v1",
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt, tt.publicHost, "my-gateway-istio.gateway-system.svc.cluster.local:8080")
			command := deployment.Spec.Template.Spec.Containers[0].Command

			found := false
			for _, arg := range command {
				if arg == tt.wantFlag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected command to contain %q, got %v", tt.wantFlag, command)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_InternalHost(t *testing.T) {
	tests := []struct {
		name             string
		extNamespace     string
		targetRefName    string
		targetRefNS      string
		gatewayClassName string
		wantInternalHost string
	}{
		{
			name:             "uses targetRef namespace when specified",
			extNamespace:     "team-a",
			targetRefName:    "my-gateway",
			targetRefNS:      "gateway-system",
			gatewayClassName: "istio",
			wantInternalHost: "my-gateway-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name:             "falls back to extension namespace when targetRef namespace empty",
			extNamespace:     "team-a",
			targetRefName:    "my-gateway",
			targetRefNS:      "",
			gatewayClassName: "istio",
			wantInternalHost: "my-gateway-istio.team-a.svc.cluster.local:8080",
		},
		{
			name:             "uses gatewayClassName for openshift-default",
			extNamespace:     "mcp-system",
			targetRefName:    "my-gateway",
			targetRefNS:      "gateway-system",
			gatewayClassName: "openshift-default",
			wantInternalHost: "my-gateway-openshift-default.gateway-system.svc.cluster.local:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage: "test-image:v1",
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: tt.extNamespace,
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      tt.targetRefName,
						Namespace: tt.targetRefNS,
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, tt.gatewayClassName))
			command := deployment.Spec.Template.Spec.Containers[0].Command

			wantFlag := "--mcp-gateway-private-host=" + tt.wantInternalHost
			found := false
			for _, arg := range command {
				if arg == wantFlag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected command to contain %q, got %v", wantFlag, command)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_PollInterval(t *testing.T) {
	tests := []struct {
		name                string
		backendPingInterval *int32
		wantFlag            string
		wantAbsent          bool
	}{
		{
			name:                "poll interval from spec",
			backendPingInterval: ptr.To(int32(30)),
			wantFlag:            "--mcp-check-interval=30",
		},
		{
			name:                "no flag when spec not set (binary default applies)",
			backendPingInterval: nil,
			wantAbsent:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage: "test-image:v1",
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					BackendPingIntervalSeconds: tt.backendPingInterval,
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
			command := deployment.Spec.Template.Spec.Containers[0].Command

			if tt.wantAbsent {
				for _, arg := range command {
					if strings.HasPrefix(arg, "--mcp-check-interval=") {
						t.Errorf("expected no --mcp-check-interval flag, but found %q", arg)
					}
				}
				return
			}

			found := false
			for _, arg := range command {
				if arg == tt.wantFlag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected command to contain %q, got %v", tt.wantFlag, command)
			}
		})
	}
}

// TestBuildBrokerRouterDeployment_NoRouterKeyFlag verifies the legacy
// --mcp-router-key flag is no longer emitted. Backend-init authentication is
// now performed via a short-lived JWT signed by the session signing key
// (GHSA-g53w-w6mj-hrpp).
func TestBuildBrokerRouterDeployment_NoRouterKeyFlag(t *testing.T) {
	r := &MCPGatewayExtensionReconciler{
		BrokerRouterImage: "test-image:v1",
	}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
			UID:       types.UID("test-uid-12345"),
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
		},
	}

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
	command := deployment.Spec.Template.Spec.Containers[0].Command

	for _, arg := range command {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			t.Errorf("--mcp-router-key flag must not be emitted by the controller, found %q", arg)
		}
	}
}

// TestMergeCommand_StripsLegacyRouterKeyFlag exercises the upgrade path: an
// existing deployment that still has the old --mcp-router-key=... flag should
// have it stripped on the next reconcile rather than preserved as a user flag.
func TestMergeCommand_StripsLegacyRouterKeyFlag(t *testing.T) {
	desired := []string{
		"./mcp_gateway",
		"--mcp-broker-public-address=0.0.0.0:8080",
		"--mcp-gateway-public-host=example.com",
	}
	existing := []string{
		"./mcp_gateway",
		"--mcp-broker-public-address=0.0.0.0:8080",
		"--mcp-gateway-public-host=example.com",
		"--mcp-router-key=deadbeefcafebabe",
		"--discovery-tools-enabled=false",
	}
	got := mergeCommand(desired, existing)
	for _, arg := range got {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			t.Errorf("mergeCommand should strip legacy --mcp-router-key, got %v", got)
		}
	}
	foundUserFlag := false
	for _, arg := range got {
		if arg == "--discovery-tools-enabled=false" {
			foundUserFlag = true
		}
	}
	if !foundUserFlag {
		t.Errorf("mergeCommand should preserve unrelated user flags, got %v", got)
	}
}

// TestBuildBrokerRouterDeployment_LogLevel verifies the broker --log-level
// flag is emitted only when the controller is configured with one
// (BROKER_ROUTER_LOG_LEVEL env var).
func TestBuildBrokerRouterDeployment_LogLevel(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
		want     string
	}{
		{
			name:     "no log-level flag when unset",
			logLevel: "",
			want:     "",
		},
		{
			name:     "log-level flag emitted when set",
			logLevel: "-4",
			want:     "--log-level=-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage:    "test-image:v1",
				BrokerRouterLogLevel: tt.logLevel,
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
			command := deployment.Spec.Template.Spec.Containers[0].Command

			var got string
			for _, arg := range command {
				if strings.HasPrefix(arg, "--log-level=") {
					got = arg
				}
			}
			if got != tt.want {
				t.Errorf("log-level flag = %q, want %q (command %v)", got, tt.want, command)
			}
		})
	}
}

// TestBuildBrokerRouterDeployment_SpecLogLevel verifies the broker --log-level
// flag is emitted from spec.logLevel with correct numeric mapping, and that
// spec.logLevel takes precedence over the operator-wide BrokerRouterLogLevel.
func TestBuildBrokerRouterDeployment_SpecLogLevel(t *testing.T) {
	tests := []struct {
		name           string
		brokerLogLevel string
		specLogLevel   mcpv1alpha1.LogLevel
		want           string
	}{
		{
			name:         "spec.logLevel debug maps to -4",
			specLogLevel: mcpv1alpha1.LogLevelDebug,
			want:         "--log-level=-4",
		},
		{
			name:         "spec.logLevel info maps to 0",
			specLogLevel: mcpv1alpha1.LogLevelInfo,
			want:         "--log-level=0",
		},
		{
			name:         "spec.logLevel warn maps to 4",
			specLogLevel: mcpv1alpha1.LogLevelWarn,
			want:         "--log-level=4",
		},
		{
			name:         "spec.logLevel error maps to 8",
			specLogLevel: mcpv1alpha1.LogLevelError,
			want:         "--log-level=8",
		},
		{
			name:           "spec.logLevel overrides operator-wide BrokerRouterLogLevel",
			brokerLogLevel: "-4",
			specLogLevel:   mcpv1alpha1.LogLevelError,
			want:           "--log-level=8",
		},
		{
			name:           "operator-wide BrokerRouterLogLevel used when spec.logLevel unset",
			brokerLogLevel: "-4",
			want:           "--log-level=-4",
		},
		{
			name: "no log-level flag when both unset",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage:    "test-image:v1",
				BrokerRouterLogLevel: tt.brokerLogLevel,
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
					LogLevel: tt.specLogLevel,
				},
			}
			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
			command := deployment.Spec.Template.Spec.Containers[0].Command
			var got string
			for _, arg := range command {
				if strings.HasPrefix(arg, "--log-level=") {
					got = arg
				}
			}
			if got != tt.want {
				t.Errorf("log-level flag = %q, want %q (command %v)", got, tt.want, command)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_TrustedHeadersKey(t *testing.T) {
	tests := []struct {
		name             string
		trustedHeaderKey *mcpv1alpha1.TrustedHeadersKey
		wantTrustedEnv   bool
		wantSecretName   string
	}{
		{
			name:             "no trusted header env var when TrustedHeadersKey is nil",
			trustedHeaderKey: nil,
			wantTrustedEnv:   false,
		},
		{
			name: "trusted header env var set when TrustedHeadersKey has SecretName",
			trustedHeaderKey: &mcpv1alpha1.TrustedHeadersKey{
				SecretName: "my-trusted-key-secret",
			},
			wantTrustedEnv: true,
			wantSecretName: "my-trusted-key-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage: "test-image:v1",
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ext",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TrustedHeadersKey: tt.trustedHeaderKey,
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
			container := deployment.Spec.Template.Spec.Containers[0]

			// JWT_SESSION_SIGNING_KEY should always be present
			var jwtEnv *corev1.EnvVar
			var trustedEnv *corev1.EnvVar
			for i := range container.Env {
				switch container.Env[i].Name {
				case "GATEWAY_SIGNING_KEY":
					jwtEnv = &container.Env[i]
				case "TRUSTED_HEADER_PUBLIC_KEY":
					trustedEnv = &container.Env[i]
				}
			}

			if jwtEnv == nil {
				t.Fatal("expected GATEWAY_SIGNING_KEY env var to always be present")
			}
			if jwtEnv.ValueFrom == nil || jwtEnv.ValueFrom.SecretKeyRef == nil {
				t.Fatal("expected GATEWAY_SIGNING_KEY to have secretKeyRef")
			}
			if jwtEnv.ValueFrom.SecretKeyRef.Name != sessionSigningKeySecretName {
				t.Errorf("expected gateway signing key secret name %q, got %q", sessionSigningKeySecretName, jwtEnv.ValueFrom.SecretKeyRef.Name)
			}
			if jwtEnv.ValueFrom.SecretKeyRef.Key != sessionSigningKeyDataKey {
				t.Errorf("expected gateway signing key secret key %q, got %q", sessionSigningKeyDataKey, jwtEnv.ValueFrom.SecretKeyRef.Key)
			}

			if !tt.wantTrustedEnv {
				if trustedEnv != nil {
					t.Errorf("expected no TRUSTED_HEADER_PUBLIC_KEY env var, but found one")
				}
				return
			}

			if trustedEnv == nil {
				t.Fatal("expected TRUSTED_HEADER_PUBLIC_KEY env var to be present")
			}
			if trustedEnv.ValueFrom == nil || trustedEnv.ValueFrom.SecretKeyRef == nil {
				t.Fatal("expected TRUSTED_HEADER_PUBLIC_KEY to have secretKeyRef")
			}
			if trustedEnv.ValueFrom.SecretKeyRef.Name != tt.wantSecretName {
				t.Errorf("expected secret name %q, got %q", tt.wantSecretName, trustedEnv.ValueFrom.SecretKeyRef.Name)
			}
			if trustedEnv.ValueFrom.SecretKeyRef.Key != "key" {
				t.Errorf("expected secret key %q, got %q", "key", trustedEnv.ValueFrom.SecretKeyRef.Key)
			}
		})
	}
}

func TestServiceAccountNeedsUpdate(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		desired  *bool
		existing *bool
		expected bool
	}{
		{
			name:     "no changes - both false",
			desired:  &falseVal,
			existing: &falseVal,
			expected: false,
		},
		{
			name:     "no changes - both true",
			desired:  &trueVal,
			existing: &trueVal,
			expected: false,
		},
		{
			name:     "no changes - both nil",
			desired:  nil,
			existing: nil,
			expected: false,
		},
		{
			name:     "changed from true to false",
			desired:  &falseVal,
			existing: &trueVal,
			expected: true,
		},
		{
			name:     "changed from false to true",
			desired:  &trueVal,
			existing: &falseVal,
			expected: true,
		},
		{
			name:     "changed from nil to false",
			desired:  &falseVal,
			existing: nil,
			expected: true,
		},
		{
			name:     "changed from false to nil",
			desired:  nil,
			existing: &falseVal,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := &corev1.ServiceAccount{
				AutomountServiceAccountToken: tt.desired,
			}
			existing := &corev1.ServiceAccount{
				AutomountServiceAccountToken: tt.existing,
			}

			result, reason := serviceAccountNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("serviceAccountNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}

func TestBuildBrokerRouterServiceAccount(t *testing.T) {
	r := &MCPGatewayExtensionReconciler{}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
		},
	}

	sa := r.buildBrokerRouterServiceAccount(mcpExt)

	if sa.Name != brokerRouterName {
		t.Errorf("expected name %q, got %q", brokerRouterName, sa.Name)
	}
	if sa.Namespace != mcpExt.Namespace {
		t.Errorf("expected namespace %q, got %q", mcpExt.Namespace, sa.Namespace)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken != false {
		t.Errorf("expected AutomountServiceAccountToken to be false")
	}
	if sa.Labels[labelAppName] != brokerRouterName {
		t.Errorf("expected label %q=%q, got %q", labelAppName, brokerRouterName, sa.Labels[labelAppName])
	}
	if sa.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("expected label %q=%q, got %q", labelManagedBy, labelManagedByValue, sa.Labels[labelManagedBy])
	}
}

func TestBuildBrokerRouterDeployment_ServiceAccount(t *testing.T) {
	r := &MCPGatewayExtensionReconciler{
		BrokerRouterImage: "test-image:v1",
	}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
		},
	}

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))

	if deployment.Spec.Template.Spec.ServiceAccountName != brokerRouterName {
		t.Errorf("expected ServiceAccountName %q, got %q", brokerRouterName, deployment.Spec.Template.Spec.ServiceAccountName)
	}
	if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken != false {
		t.Errorf("expected AutomountServiceAccountToken to be false on deployment pod spec")
	}
}

func TestDerivePublicHost(t *testing.T) {
	tests := []struct {
		name               string
		listenerConfig     *mcpv1alpha1.ListenerConfig
		annotationOverride string
		want               string
		wantErr            bool
	}{
		{
			name:               "annotation overrides listener hostname",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "override.example.com",
			want:               "override.example.com",
		},
		{
			name:               "uses listener hostname when no annotation",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "",
			want:               "listener.example.com",
		},
		{
			name:               "handles wildcard hostname",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "*.example.com"},
			annotationOverride: "",
			want:               "mcp.example.com",
		},
		{
			name:               "handles double-wildcard hostname",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "*.team-a.example.com"},
			annotationOverride: "",
			want:               "mcp.team-a.example.com",
		},
		{
			name:               "empty hostname returns error",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: ""},
			annotationOverride: "",
			wantErr:            true,
		},
		{
			name:               "nil listener config returns error",
			listenerConfig:     nil,
			annotationOverride: "",
			wantErr:            true,
		},
		{
			name:               "annotation takes precedence even with wildcard",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "*.example.com"},
			annotationOverride: "specific.example.com",
			want:               "specific.example.com",
		},
		{
			name:               "strips port from annotation override",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "mcp.127-0-0-1.sslip.io:8001",
			want:               "mcp.127-0-0-1.sslip.io",
		},
		{
			name:               "annotation without port unchanged",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "mcp.127-0-0-1.sslip.io",
			want:               "mcp.127-0-0-1.sslip.io",
		},
		{
			name:               "invalid hostname with path returns error",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "example.com/path"},
			annotationOverride: "",
			wantErr:            true,
		},
		{
			name:               "annotation with scheme should error",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "https://example.com",
			wantErr:            true,
		},
		{
			name:               "annotation with path should error",
			listenerConfig:     &mcpv1alpha1.ListenerConfig{Hostname: "listener.example.com"},
			annotationOverride: "example.com/path",
			wantErr:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := derivePublicHost(tt.listenerConfig, tt.annotationOverride)
			if tt.wantErr {
				if err == nil {
					t.Errorf("derivePublicHost() expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Errorf("derivePublicHost() unexpected error: %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("derivePublicHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDerivePrivateHost(t *testing.T) {
	tests := []struct {
		name             string
		spec             mcpv1alpha1.MCPGatewayExtensionSpec
		listenerConfig   *mcpv1alpha1.ListenerConfig
		gatewayClassName string
		want             string
	}{
		{
			name: "HTTP listener: bare host, no scheme prefix (backwards compatible)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 8080, Protocol: "HTTP"},
			want:             "my-gw-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name: "HTTPS listener: scheme is prepended (issue #917)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "HTTPS"},
			want:             "https://my-gw-istio.gateway-system.svc.cluster.local:443",
		},
		{
			name: "HTTPS listener with mixed-case protocol value still detected",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "https"},
			want:             "https://my-gw-istio.gateway-system.svc.cluster.local:443",
		},
		{
			name: "openshift-default gateway class uses correct service name",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gateway",
					Namespace: "gateway-system",
				},
			},
			gatewayClassName: "openshift-default",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "HTTPS"},
			want:             "https://my-gateway-openshift-default.gateway-system.svc.cluster.local:443",
		},
		{
			name: "PrivateHost override is honoured verbatim (no scheme injection)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
				PrivateHost: "my-gw-istio.gateway-system.svc.cluster.local:8081",
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "HTTPS"},
			want:             "my-gw-istio.gateway-system.svc.cluster.local:8081",
		},
		{
			name: "PrivateHost override may carry its own scheme",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
				PrivateHost: "https://custom.example.com:443",
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 8080, Protocol: "HTTP"},
			want:             "https://custom.example.com:443",
		},
		{
			name: "TCP listener (no recognised TLS): fallback to plain host (no scheme)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			gatewayClassName: "istio",
			listenerConfig:   &mcpv1alpha1.ListenerConfig{Port: 9090, Protocol: "TCP"},
			want:             "my-gw-istio.gateway-system.svc.cluster.local:9090",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{Spec: tt.spec}
			got := derivePrivateHost(mcpExt, tt.listenerConfig, tt.gatewayClassName)
			if got != tt.want {
				t.Errorf("derivePrivateHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindListenerConfig(t *testing.T) {
	hostname := gatewayv1.Hostname("mcp.example.com")
	wildcardHostname := gatewayv1.Hostname("*.example.com")

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gateway",
			Namespace: "test-ns",
		},
		Spec: gatewayv1.GatewaySpec{
			Listeners: []gatewayv1.Listener{
				{
					Name:     "http",
					Port:     8080,
					Protocol: gatewayv1.HTTPProtocolType,
					Hostname: &hostname,
				},
				{
					Name:     "https",
					Port:     8443,
					Protocol: gatewayv1.HTTPSProtocolType,
					Hostname: &wildcardHostname,
				},
				{
					Name:     "no-hostname",
					Port:     9090,
					Protocol: gatewayv1.HTTPProtocolType,
				},
			},
		},
	}

	tests := []struct {
		name         string
		sectionName  string
		wantPort     uint32
		wantHost     string
		wantProtocol string
		wantErr      bool
	}{
		{
			name:         "finds http listener",
			sectionName:  "http",
			wantPort:     8080,
			wantHost:     "mcp.example.com",
			wantProtocol: "HTTP",
			wantErr:      false,
		},
		{
			name:         "finds https listener with wildcard",
			sectionName:  "https",
			wantPort:     8443,
			wantHost:     "*.example.com",
			wantProtocol: "HTTPS",
			wantErr:      false,
		},
		{
			name:         "finds listener without hostname",
			sectionName:  "no-hostname",
			wantPort:     9090,
			wantHost:     "",
			wantProtocol: "HTTP",
			wantErr:      false,
		},
		{
			name:        "returns error for non-existent listener",
			sectionName: "nonexistent",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := findListenerConfigByName(gateway, tt.sectionName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("findListenerConfigByName() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("findListenerConfigByName() unexpected error: %v", err)
				return
			}
			if config.Port != tt.wantPort {
				t.Errorf("findListenerConfigByName() port = %d, want %d", config.Port, tt.wantPort)
			}
			if config.Hostname != tt.wantHost {
				t.Errorf("findListenerConfigByName() hostname = %q, want %q", config.Hostname, tt.wantHost)
			}
			if config.Name != tt.sectionName {
				t.Errorf("findListenerConfigByName() name = %q, want %q", config.Name, tt.sectionName)
			}
			if config.Protocol != tt.wantProtocol {
				t.Errorf("findListenerConfigByName() protocol = %q, want %q", config.Protocol, tt.wantProtocol)
			}
		})
	}
}

func TestListenerAllowsNamespace(t *testing.T) {
	allNamespaces := gatewayv1.NamespacesFromAll
	sameNamespace := gatewayv1.NamespacesFromSame
	selectorNamespace := gatewayv1.NamespacesFromSelector

	tests := []struct {
		name             string
		listener         *gatewayv1.Listener
		namespace        string
		gatewayNamespace string
		nsLabels         map[string]string
		want             bool
	}{
		{
			name: "nil allowedRoutes defaults to Same namespace - same ns",
			listener: &gatewayv1.Listener{
				Name:          "test",
				AllowedRoutes: nil,
			},
			namespace:        "gateway-ns",
			gatewayNamespace: "gateway-ns",
			want:             true,
		},
		{
			name: "nil allowedRoutes defaults to Same namespace - different ns",
			listener: &gatewayv1.Listener{
				Name:          "test",
				AllowedRoutes: nil,
			},
			namespace:        "other-ns",
			gatewayNamespace: "gateway-ns",
			want:             false,
		},
		{
			name: "All namespaces allows any namespace",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &allNamespaces,
					},
				},
			},
			namespace:        "any-namespace",
			gatewayNamespace: "gateway-ns",
			want:             true,
		},
		{
			name: "Same namespace only allows gateway namespace",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &sameNamespace,
					},
				},
			},
			namespace:        "other-ns",
			gatewayNamespace: "gateway-ns",
			want:             false,
		},
		{
			name: "Same namespace allows gateway namespace",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &sameNamespace,
					},
				},
			},
			namespace:        "gateway-ns",
			gatewayNamespace: "gateway-ns",
			want:             true,
		},
		{
			name: "Selector matches namespace labels",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &selectorNamespace,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"env": "prod"},
						},
					},
				},
			},
			namespace:        "any-ns",
			gatewayNamespace: "gateway-ns",
			nsLabels:         map[string]string{"env": "prod", "team": "backend"},
			want:             true,
		},
		{
			name: "Selector does not match namespace labels",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &selectorNamespace,
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"env": "prod"},
						},
					},
				},
			},
			namespace:        "any-ns",
			gatewayNamespace: "gateway-ns",
			nsLabels:         map[string]string{"env": "staging"},
			want:             false,
		},
		{
			name: "Selector with nil selector rejects",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &selectorNamespace,
					},
				},
			},
			namespace:        "any-ns",
			gatewayNamespace: "gateway-ns",
			want:             false,
		},
		{
			name: "nil From defaults to Same",
			listener: &gatewayv1.Listener{
				Name: "test",
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: nil,
					},
				},
			},
			namespace:        "other-ns",
			gatewayNamespace: "gateway-ns",
			want:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenerAllowsNamespace(tt.listener, tt.namespace, tt.gatewayNamespace, tt.nsLabels)
			if got != tt.want {
				t.Errorf("listenerAllowsNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildGatewayHTTPRoute(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{}

	tests := []struct {
		name           string
		mcpExt         *mcpv1alpha1.MCPGatewayExtension
		publicHost     string
		expectHostname string
	}{
		{
			name: "exact hostname",
			mcpExt: &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:        "my-gateway",
						Namespace:   "gateway-ns",
						SectionName: "mcp",
					},
				},
			},
			publicHost:     "mcp.example.com",
			expectHostname: "mcp.example.com",
		},
		{
			name: "wildcard resolved to mcp subdomain",
			mcpExt: &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:        "my-gateway",
						Namespace:   "gateway-ns",
						SectionName: "wildcard",
					},
				},
			},
			publicHost:     "mcp.example.com",
			expectHostname: "mcp.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := reconciler.buildGatewayHTTPRoute(tt.mcpExt, tt.publicHost)
			if route == nil {
				t.Fatal("expected non-nil HTTPRoute")
			}
			if route.Name != gatewayHTTPRouteName {
				t.Errorf("name = %q, want %q", route.Name, gatewayHTTPRouteName)
			}
			if route.Namespace != tt.mcpExt.Namespace {
				t.Errorf("namespace = %q, want %q", route.Namespace, tt.mcpExt.Namespace)
			}
			if len(route.Spec.Hostnames) != 1 || string(route.Spec.Hostnames[0]) != tt.expectHostname {
				t.Errorf("hostnames = %v, want [%s]", route.Spec.Hostnames, tt.expectHostname)
			}
			if len(route.Spec.ParentRefs) != 1 {
				t.Fatalf("expected 1 parentRef, got %d", len(route.Spec.ParentRefs))
			}
			parentRef := route.Spec.ParentRefs[0]
			if string(parentRef.Name) != tt.mcpExt.Spec.TargetRef.Name {
				t.Errorf("parentRef name = %q, want %q", parentRef.Name, tt.mcpExt.Spec.TargetRef.Name)
			}
			if parentRef.SectionName == nil || string(*parentRef.SectionName) != tt.mcpExt.Spec.TargetRef.SectionName {
				t.Errorf("parentRef sectionName = %v, want %q", parentRef.SectionName, tt.mcpExt.Spec.TargetRef.SectionName)
			}
		})
	}
}

func TestFilterManagedFlags(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    []string
	}{
		{
			name:    "binary only",
			command: []string{"./mcp_gateway"},
			want:    []string{"./mcp_gateway"},
		},
		{
			name:    "all managed flags kept",
			command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--mcp-gateway-public-host=example.com"},
			want:    []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--mcp-gateway-public-host=example.com"},
		},
		{
			name:    "user flags stripped",
			command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--discovery-tools-enabled=false", "--cache-connection-string=redis://localhost"},
			want:    []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
		},
		{
			name:    "managed log-level flag kept",
			command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=-4"},
			want:    []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=-4"},
		},
		{
			name:    "empty command",
			command: []string{},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterManagedFlags(tt.command)
			if len(got) != len(tt.want) {
				t.Fatalf("filterManagedFlags() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("filterManagedFlags()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMergeCommand(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		existing []string
		want     []string
	}{
		{
			name:     "no user flags",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
		},
		{
			name:     "preserves user flags from existing",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--discovery-tools-enabled=false"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--discovery-tools-enabled=false"},
		},
		{
			name:     "managed log-level replaced by desired value",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=-4"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=0"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=-4"},
		},
		{
			name:     "managed log-level stripped when not desired",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=-4"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
		},
		{
			name:     "updates managed flag and preserves user flags",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:9090"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--cache-connection-string=redis://localhost"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:9090", "--cache-connection-string=redis://localhost"},
		},
		{
			name:     "multiple user flags preserved",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--discovery-tools-enabled=false", "--session-length=3600"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--discovery-tools-enabled=false", "--session-length=3600"},
		},
		{
			name:     "existing has no flags",
			desired:  []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
			existing: []string{"./mcp_gateway"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeCommand(tt.desired, tt.existing)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeCommand() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("mergeCommand()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFilterManagedEnvVars(t *testing.T) {
	tests := []struct {
		name string
		env  []corev1.EnvVar
		want []corev1.EnvVar
	}{
		{
			name: "only managed vars returned",
			env: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"},
				{Name: "LOG_LEVEL", Value: "debug"},
				{Name: "CACHE_CONNECTION_STRING", Value: "redis://localhost"},
			},
			want: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"},
				{Name: "CACHE_CONNECTION_STRING", Value: "redis://localhost"},
			},
		},
		{
			name: "oauth vars are managed",
			env: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://keycloak/realms/mcp"},
			},
			want: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://keycloak/realms/mcp"},
			},
		},
		{
			name: "no managed vars",
			env: []corev1.EnvVar{
				{Name: "LOG_LEVEL", Value: "debug"},
			},
			want: nil,
		},
		{
			name: "empty input",
			env:  nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterManagedEnvVars(tt.env)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("filterManagedEnvVars() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		desired  []corev1.EnvVar
		existing []corev1.EnvVar
		want     []corev1.EnvVar
	}{
		{
			name:     "no user vars",
			desired:  []corev1.EnvVar{{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"}},
			existing: []corev1.EnvVar{{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"}},
			want:     []corev1.EnvVar{{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"}},
		},
		{
			name:    "preserves user vars from existing",
			desired: []corev1.EnvVar{{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"}},
			existing: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "old-key"},
				{Name: "LOG_LEVEL", Value: "debug"},
				{Name: "MY_CUSTOM_VAR", Value: "custom-value"},
			},
			want: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"},
				{Name: "LOG_LEVEL", Value: "debug"},
				{Name: "MY_CUSTOM_VAR", Value: "custom-value"},
			},
		},
		{
			name:    "no desired managed vars still preserves user vars",
			desired: nil,
			existing: []corev1.EnvVar{
				{Name: "LOG_LEVEL", Value: "debug"},
			},
			want: []corev1.EnvVar{
				{Name: "LOG_LEVEL", Value: "debug"},
			},
		},
		{
			name: "removing oauth config strips managed oauth vars",
			desired: []corev1.EnvVar{
				{Name: "GATEWAY_SIGNING_KEY", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "signing-key"},
						Key:                  "key",
					},
				}},
			},
			existing: []corev1.EnvVar{
				{Name: "GATEWAY_SIGNING_KEY", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "signing-key"},
						Key:                  "key",
					},
				}},
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "OAUTH_RESOURCE", Value: "https://mcp.example.com/mcp"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://keycloak/realms/mcp"},
				{Name: "OAUTH_BEARER_METHODS_SUPPORTED", Value: "header"},
				{Name: "OAUTH_SCOPES_SUPPORTED", Value: "basic"},
				{Name: "LOG_LEVEL", Value: "debug"},
			},
			want: []corev1.EnvVar{
				{Name: "GATEWAY_SIGNING_KEY", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "signing-key"},
						Key:                  "key",
					},
				}},
				{Name: "LOG_LEVEL", Value: "debug"},
			},
		},
		{
			name: "managed oauth var in desired replaces existing value",
			desired: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "new-name"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://new-server"},
			},
			existing: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "old-name"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://old-server"},
				{Name: "LOG_LEVEL", Value: "debug"},
			},
			want: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "new-name"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "https://new-server"},
				{Name: "LOG_LEVEL", Value: "debug"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeEnvVars(tt.desired, tt.existing)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("mergeEnvVars() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeVolumes(t *testing.T) {
	tests := []struct {
		name     string
		desired  []corev1.Volume
		existing []corev1.Volume
		want     []corev1.Volume
	}{
		{
			name:     "no user volumes",
			desired:  []corev1.Volume{{Name: "config-volume"}},
			existing: []corev1.Volume{{Name: "config-volume"}},
			want:     []corev1.Volume{{Name: "config-volume"}},
		},
		{
			name:    "preserves user volumes from existing",
			desired: []corev1.Volume{{Name: "config-volume"}},
			existing: []corev1.Volume{
				{Name: "config-volume"},
				{Name: "ca-cert", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "my-ca"}}},
			},
			want: []corev1.Volume{
				{Name: "config-volume"},
				{Name: "ca-cert", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "my-ca"}}},
			},
		},
		{
			name:    "updates managed volume, preserves user volume",
			desired: []corev1.Volume{{Name: "config-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "new-config"}}}},
			existing: []corev1.Volume{
				{Name: "config-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "old-config"}}},
				{Name: "user-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
			want: []corev1.Volume{
				{Name: "config-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "new-config"}}},
				{Name: "user-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVolumes(tt.desired, tt.existing)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("mergeVolumes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeVolumeMounts(t *testing.T) {
	tests := []struct {
		name     string
		desired  []corev1.VolumeMount
		existing []corev1.VolumeMount
		want     []corev1.VolumeMount
	}{
		{
			name:     "no user mounts",
			desired:  []corev1.VolumeMount{{Name: "config-volume", MountPath: "/config"}},
			existing: []corev1.VolumeMount{{Name: "config-volume", MountPath: "/config"}},
			want:     []corev1.VolumeMount{{Name: "config-volume", MountPath: "/config"}},
		},
		{
			name:    "preserves user mounts from existing",
			desired: []corev1.VolumeMount{{Name: "config-volume", MountPath: "/config"}},
			existing: []corev1.VolumeMount{
				{Name: "config-volume", MountPath: "/config"},
				{Name: "ca-cert", MountPath: "/certs/ca.crt", SubPath: "ca.crt", ReadOnly: true},
			},
			want: []corev1.VolumeMount{
				{Name: "config-volume", MountPath: "/config"},
				{Name: "ca-cert", MountPath: "/certs/ca.crt", SubPath: "ca.crt", ReadOnly: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVolumeMounts(tt.desired, tt.existing)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("mergeVolumeMounts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeploymentNeedsUpdate_UserEnvVarsIgnored(t *testing.T) {
	base := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "mcp-gateway",
							Image: "test:latest",
						}},
					},
				},
			},
		}
	}

	desired := base()
	existing := base()
	existing.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "LOG_LEVEL", Value: "debug"},
		{Name: "MY_CUSTOM_VAR", Value: "custom-value"},
	}

	needsUpdate, _ := deploymentNeedsUpdate(desired, existing)
	if needsUpdate {
		t.Error("deploymentNeedsUpdate() should not trigger for user-added env vars")
	}
}

// TestBuildGatewayHTTPRoute_StripsRouterHeaders verifies the route always
// includes a RequestHeaderModifier filter that removes the router-internal
// `mcp-init-host` and `router-key` headers. This is defense-in-depth so that
// caller-controlled values for these headers can never reach a backend MCP
// server (GHSA-g53w-w6mj-hrpp).
func TestBuildGatewayHTTPRoute_StripsRouterHeaders(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:        "my-gateway",
				Namespace:   "gateway-ns",
				SectionName: "mcp",
			},
		},
	}

	route := reconciler.buildGatewayHTTPRoute(mcpExt, "mcp.example.com")
	if len(route.Spec.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(route.Spec.Rules))
	}

	if route.Spec.Rules[0].Name == nil || string(*route.Spec.Rules[0].Name) != "mcp" {
		t.Errorf("expected first rule name = %q, got %v", "mcp", route.Spec.Rules[0].Name)
	}
	if route.Spec.Rules[1].Name == nil || string(*route.Spec.Rules[1].Name) != "well-known" {
		t.Errorf("expected second rule name = %q, got %v", "well-known", route.Spec.Rules[1].Name)
	}
	if route.Spec.Rules[2].Name == nil || string(*route.Spec.Rules[2].Name) != "status" {
		t.Errorf("expected third rule name = %q, got %v", "status", route.Spec.Rules[2].Name)
	}
	if len(route.Spec.Rules[2].Filters) != 0 {
		t.Errorf("status rule should have no filters, got %d", len(route.Spec.Rules[2].Filters))
	}

	var found bool
	for _, rule := range route.Spec.Rules {
		for _, f := range rule.Filters {
			if f.Type != gatewayv1.HTTPRouteFilterRequestHeaderModifier {
				continue
			}
			if f.RequestHeaderModifier == nil {
				continue
			}

			removed := map[string]bool{}
			for _, h := range f.RequestHeaderModifier.Remove {
				removed[h] = true
			}

			if !removed["mcp-init-host"] {
				t.Errorf("expected RequestHeaderModifier.Remove to contain mcp-init-host, got %v", f.RequestHeaderModifier.Remove)
			}

			if !removed["router-key"] {
				t.Errorf("expected RequestHeaderModifier.Remove to contain router-key, got %v", f.RequestHeaderModifier.Remove)
			}

			found = true
		}
	}
	if !found {
		t.Errorf("expected RequestHeaderModifier filter to be present in HTTPRoute rules")
	}
}

func TestHTTPRouteNeedsUpdate(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:        "my-gateway",
				Namespace:   "gateway-ns",
				SectionName: "mcp",
			},
		},
	}
	publicHost := "mcp.example.com"

	tests := []struct {
		name       string
		modify     func(r *gatewayv1.HTTPRoute)
		wantUpdate bool
	}{
		{
			name:       "no changes",
			modify:     func(_ *gatewayv1.HTTPRoute) {},
			wantUpdate: false,
		},
		{
			name: "hostname changed",
			modify: func(r *gatewayv1.HTTPRoute) {
				r.Spec.Hostnames = []gatewayv1.Hostname{"other.example.com"}
			},
			wantUpdate: true,
		},
		{
			name: "parentRef changed",
			modify: func(r *gatewayv1.HTTPRoute) {
				r.Spec.ParentRefs[0].Name = "other-gateway"
			},
			wantUpdate: true,
		},
		{
			name: "rule name changed",
			modify: func(r *gatewayv1.HTTPRoute) {
				r.Spec.Rules[0].Name = nil
			},
			wantUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := reconciler.buildGatewayHTTPRoute(mcpExt, publicHost)
			existing := reconciler.buildGatewayHTTPRoute(mcpExt, publicHost)
			tt.modify(existing)
			needsUpdate, _ := httpRouteNeedsUpdate(desired, existing)
			if needsUpdate != tt.wantUpdate {
				t.Errorf("httpRouteNeedsUpdate() = %v, want %v", needsUpdate, tt.wantUpdate)
			}
		})
	}
}

func TestBuildTokensHTTPRoute(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:        "my-gateway",
				Namespace:   "gateway-ns",
				SectionName: "mcp",
			},
		},
	}

	route := reconciler.buildTokensHTTPRoute(mcpExt, "mcp.example.com")
	if route.Name != tokensHTTPRouteName {
		t.Errorf("name = %q, want %q", route.Name, tokensHTTPRouteName)
	}
	if len(route.Spec.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(route.Spec.Rules))
	}
	if route.Spec.Rules[0].Name == nil || string(*route.Spec.Rules[0].Name) != "tokens" {
		t.Errorf("expected rule name = %q, got %v", "tokens", route.Spec.Rules[0].Name)
	}
	pathVal := route.Spec.Rules[0].Matches[0].Path.Value
	if pathVal == nil || *pathVal != "/tokens" {
		t.Errorf("expected path /tokens, got %v", pathVal)
	}
	if len(route.Spec.Rules[0].Filters) != 0 {
		t.Errorf("expected no filters on tokens route, got %d", len(route.Spec.Rules[0].Filters))
	}
}

func TestBuildBrokerRouterDeployment_URLElicitation(t *testing.T) {
	r := &MCPGatewayExtensionReconciler{
		BrokerRouterImage: "test-image:v1",
	}

	tests := []struct {
		name     string
		policy   mcpv1alpha1.URLElicitationPolicy
		wantFlag bool
	}{
		{
			name:     "enabled",
			policy:   mcpv1alpha1.URLElicitationEnabled,
			wantFlag: true,
		},
		{
			name:     "disabled",
			policy:   mcpv1alpha1.URLElicitationDisabled,
			wantFlag: false,
		},
		{
			name:     "empty (default)",
			policy:   "",
			wantFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:        "my-gateway",
						Namespace:   "gateway-ns",
						SectionName: "mcp",
					},
					URLElicitation: tt.policy,
				},
			}

			dep := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", "internal:8080")
			cmd := dep.Spec.Template.Spec.Containers[0].Command
			hasFlag := slices.Contains(cmd, "--enable-url-elicitation")
			if hasFlag != tt.wantFlag {
				t.Errorf("--enable-url-elicitation present = %v, want %v", hasFlag, tt.wantFlag)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_ReadinessProbe(t *testing.T) {
	r := &MCPGatewayExtensionReconciler{
		BrokerRouterImage: "test-image:v1",
	}
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ext",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Name:      "my-gateway",
				Namespace: "gateway-system",
			},
		},
	}

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080, "istio"))
	probe := deployment.Spec.Template.Spec.Containers[0].ReadinessProbe

	if probe == nil {
		t.Fatal("expected ReadinessProbe to be set on broker container, got nil")
	}
	if probe.HTTPGet == nil {
		t.Fatal("expected HTTPGet probe handler, got nil")
	}
	if probe.HTTPGet.Path != "/readyz" {
		t.Errorf("expected probe Path /readyz, got %q", probe.HTTPGet.Path)
	}
	wantPort := intstr.FromString("http")
	if probe.HTTPGet.Port != wantPort {
		t.Errorf("expected probe Port %v (named http), got %v", wantPort, probe.HTTPGet.Port)
	}
}

func TestDeploymentNeedsUpdate_Probe(t *testing.T) {
	baseDeployment := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-deployment",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    "test-container",
								Image:   "test-image:v1",
								Command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
								Ports: []corev1.ContainerPort{
									{Name: "http", ContainerPort: 8080},
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path:   "/readyz",
											Port:   intstr.FromString("http"),
											Scheme: corev1.URISchemeHTTP,
										},
									},
									InitialDelaySeconds: 5,
									TimeoutSeconds:      1,
									PeriodSeconds:       10,
									SuccessThreshold:    1,
									FailureThreshold:    3,
								},
							},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		modify   func(d *appsv1.Deployment)
		expected bool
	}{
		{
			name:     "no changes",
			modify:   func(_ *appsv1.Deployment) {},
			expected: false,
		},
		{
			name: "probe removed triggers update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].ReadinessProbe = nil
			},
			expected: true,
		},
		{
			name: "probe path changed triggers update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path = "/different"
			},
			expected: true,
		},
		{
			name: "probe port changed triggers update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Port = intstr.FromString("grpc")
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseDeployment()
			existing := baseDeployment()
			tt.modify(existing)

			result, reason := deploymentNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("deploymentNeedsUpdate() = %v, expected %v, reason: %s", result, tt.expected, reason)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_OAuthProtectedResource(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{BrokerRouterImage: "test-image:latest"}

	base := func() *mcpv1alpha1.MCPGatewayExtension {
		return &mcpv1alpha1.MCPGatewayExtension{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "mcp-system", UID: "test-uid"},
			Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name: "mcp-gateway", Namespace: "gateway-system", SectionName: "mcp",
				},
			},
		}
	}

	t.Run("no oauth config sets no oauth env vars", func(t *testing.T) {
		dep := reconciler.buildBrokerRouterDeployment(base(), "mcp.example.com", "internal:8080")
		env := dep.Spec.Template.Spec.Containers[0].Env
		for _, e := range env {
			if strings.HasPrefix(e.Name, "OAUTH_") {
				t.Errorf("unexpected env var %q when oauthProtectedResource is nil", e.Name)
			}
		}
	})

	t.Run("all defaults applied when only authorizationServers set", func(t *testing.T) {
		mcpExt := base()
		mcpExt.Spec.OAuthProtectedResource = &mcpv1alpha1.OAuthProtectedResource{
			AuthorizationServers: []string{"https://keycloak.example.com/realms/mcp"},
		}
		dep := reconciler.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", "internal:8080")
		envMap := make(map[string]string)
		for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
			envMap[e.Name] = e.Value
		}
		if envMap["OAUTH_RESOURCE_NAME"] != "MCP Server" {
			t.Errorf("OAUTH_RESOURCE_NAME = %q, want %q", envMap["OAUTH_RESOURCE_NAME"], "MCP Server")
		}
		if envMap["OAUTH_RESOURCE"] != "https://mcp.example.com/mcp" {
			t.Errorf("OAUTH_RESOURCE = %q, want %q", envMap["OAUTH_RESOURCE"], "https://mcp.example.com/mcp")
		}
		if envMap["OAUTH_AUTHORIZATION_SERVERS"] != "https://keycloak.example.com/realms/mcp" {
			t.Errorf("OAUTH_AUTHORIZATION_SERVERS = %q", envMap["OAUTH_AUTHORIZATION_SERVERS"])
		}
		if envMap["OAUTH_BEARER_METHODS_SUPPORTED"] != "header" {
			t.Errorf("OAUTH_BEARER_METHODS_SUPPORTED = %q, want %q", envMap["OAUTH_BEARER_METHODS_SUPPORTED"], "header")
		}
		if envMap["OAUTH_SCOPES_SUPPORTED"] != "basic" {
			t.Errorf("OAUTH_SCOPES_SUPPORTED = %q, want %q", envMap["OAUTH_SCOPES_SUPPORTED"], "basic")
		}
	})

	t.Run("explicit values override defaults", func(t *testing.T) {
		mcpExt := base()
		mcpExt.Spec.OAuthProtectedResource = &mcpv1alpha1.OAuthProtectedResource{
			ResourceName:           "My MCP",
			Resource:               "http://mcp.127-0-0-1.sslip.io:8001/mcp",
			AuthorizationServers:   []string{"https://auth1.example.com", "https://auth2.example.com"},
			BearerMethodsSupported: []string{"header", "body"},
			ScopesSupported:        []string{"basic", "groups", "roles"},
		}
		dep := reconciler.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", "internal:8080")
		envMap := make(map[string]string)
		for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
			envMap[e.Name] = e.Value
		}
		if envMap["OAUTH_RESOURCE_NAME"] != "My MCP" {
			t.Errorf("OAUTH_RESOURCE_NAME = %q, want %q", envMap["OAUTH_RESOURCE_NAME"], "My MCP")
		}
		if envMap["OAUTH_RESOURCE"] != "http://mcp.127-0-0-1.sslip.io:8001/mcp" {
			t.Errorf("OAUTH_RESOURCE = %q", envMap["OAUTH_RESOURCE"])
		}
		if envMap["OAUTH_AUTHORIZATION_SERVERS"] != "https://auth1.example.com,https://auth2.example.com" {
			t.Errorf("OAUTH_AUTHORIZATION_SERVERS = %q", envMap["OAUTH_AUTHORIZATION_SERVERS"])
		}
		if envMap["OAUTH_BEARER_METHODS_SUPPORTED"] != "header,body" {
			t.Errorf("OAUTH_BEARER_METHODS_SUPPORTED = %q", envMap["OAUTH_BEARER_METHODS_SUPPORTED"])
		}
		if envMap["OAUTH_SCOPES_SUPPORTED"] != "basic,groups,roles" {
			t.Errorf("OAUTH_SCOPES_SUPPORTED = %q", envMap["OAUTH_SCOPES_SUPPORTED"])
		}
	})

	t.Run("oauth env change triggers deployment update", func(t *testing.T) {
		mcpExt := base()
		mcpExt.Spec.OAuthProtectedResource = &mcpv1alpha1.OAuthProtectedResource{
			AuthorizationServers: []string{"https://keycloak.example.com/realms/mcp"},
		}
		desired := reconciler.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", "internal:8080")

		mcpExt2 := base()
		mcpExt2.Spec.OAuthProtectedResource = &mcpv1alpha1.OAuthProtectedResource{
			AuthorizationServers: []string{"https://other-auth.example.com/realms/mcp"},
		}
		existing := reconciler.buildBrokerRouterDeployment(mcpExt2, "mcp.example.com", "internal:8080")

		needsUpdate, _ := deploymentNeedsUpdate(desired, existing)
		if !needsUpdate {
			t.Error("deploymentNeedsUpdate() should trigger when oauth authorization servers change")
		}
	})

	t.Run("removing oauth config triggers deployment update", func(t *testing.T) {
		mcpExt := base()
		mcpExt.Spec.OAuthProtectedResource = &mcpv1alpha1.OAuthProtectedResource{
			AuthorizationServers: []string{"https://keycloak.example.com/realms/mcp"},
		}
		desired := reconciler.buildBrokerRouterDeployment(base(), "mcp.example.com", "internal:8080")
		existing := reconciler.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", "internal:8080")

		needsUpdate, _ := deploymentNeedsUpdate(desired, existing)
		if !needsUpdate {
			t.Error("deploymentNeedsUpdate() should trigger when oauthProtectedResource is removed")
		}
	})
}
