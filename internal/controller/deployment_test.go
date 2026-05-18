package controller

import (
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
									{Name: "config", MountPath: "/config"},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "config",
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
					"--log-level=debug",
				)
			},
			expected: false,
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
			name: "volume mount changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath = "/new-config"
			},
			expected: true,
		},
		{
			name: "volume added",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Volumes = append(
					d.Spec.Template.Spec.Volumes,
					corev1.Volume{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				)
			},
			expected: true,
		},
		{
			name: "volume secret name changed",
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
		wantInternalHost string
	}{
		{
			name:             "uses targetRef namespace when specified",
			extNamespace:     "team-a",
			targetRefName:    "my-gateway",
			targetRefNS:      "gateway-system",
			wantInternalHost: "my-gateway-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name:             "falls back to extension namespace when targetRef namespace empty",
			extNamespace:     "team-a",
			targetRefName:    "my-gateway",
			targetRefNS:      "",
			wantInternalHost: "my-gateway-istio.team-a.svc.cluster.local:8080",
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

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))
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

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))
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

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))
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
		"--log-level=debug",
	}
	got := mergeCommand(desired, existing)
	for _, arg := range got {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			t.Errorf("mergeCommand should strip legacy --mcp-router-key, got %v", got)
		}
	}
	foundLogLevel := false
	for _, arg := range got {
		if arg == "--log-level=debug" {
			foundLogLevel = true
		}
	}
	if !foundLogLevel {
		t.Errorf("mergeCommand should preserve unrelated user flags, got %v", got)
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

			deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))
			container := deployment.Spec.Template.Spec.Containers[0]

			// JWT_SESSION_SIGNING_KEY should always be present
			var jwtEnv *corev1.EnvVar
			var trustedEnv *corev1.EnvVar
			for i := range container.Env {
				switch container.Env[i].Name {
				case "JWT_SESSION_SIGNING_KEY":
					jwtEnv = &container.Env[i]
				case "TRUSTED_HEADER_PUBLIC_KEY":
					trustedEnv = &container.Env[i]
				}
			}

			if jwtEnv == nil {
				t.Fatal("expected JWT_SESSION_SIGNING_KEY env var to always be present")
			}
			if jwtEnv.ValueFrom == nil || jwtEnv.ValueFrom.SecretKeyRef == nil {
				t.Fatal("expected JWT_SESSION_SIGNING_KEY to have secretKeyRef")
			}
			if jwtEnv.ValueFrom.SecretKeyRef.Name != sessionSigningKeySecretName {
				t.Errorf("expected JWT secret name %q, got %q", sessionSigningKeySecretName, jwtEnv.ValueFrom.SecretKeyRef.Name)
			}
			if jwtEnv.ValueFrom.SecretKeyRef.Key != sessionSigningKeyDataKey {
				t.Errorf("expected JWT secret key %q, got %q", sessionSigningKeyDataKey, jwtEnv.ValueFrom.SecretKeyRef.Key)
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

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))

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
		name           string
		spec           mcpv1alpha1.MCPGatewayExtensionSpec
		listenerConfig *mcpv1alpha1.ListenerConfig
		want           string
	}{
		{
			name: "HTTP listener: bare host, no scheme prefix (backwards compatible)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 8080, Protocol: "HTTP"},
			want:           "my-gw-istio.gateway-system.svc.cluster.local:8080",
		},
		{
			name: "HTTPS listener: scheme is prepended (issue #917)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "HTTPS"},
			want:           "https://my-gw-istio.gateway-system.svc.cluster.local:443",
		},
		{
			name: "HTTPS listener with mixed-case protocol value still detected",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "https"},
			want:           "https://my-gw-istio.gateway-system.svc.cluster.local:443",
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
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 443, Protocol: "HTTPS"},
			want:           "my-gw-istio.gateway-system.svc.cluster.local:8081",
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
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 8080, Protocol: "HTTP"},
			want:           "https://custom.example.com:443",
		},
		{
			name: "TCP listener (no recognised TLS): fallback to plain host (no scheme)",
			spec: mcpv1alpha1.MCPGatewayExtensionSpec{
				TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
					Name:      "my-gw",
					Namespace: "gateway-system",
				},
			},
			listenerConfig: &mcpv1alpha1.ListenerConfig{Port: 9090, Protocol: "TCP"},
			want:           "my-gw-istio.gateway-system.svc.cluster.local:9090",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{Spec: tt.spec}
			got := derivePrivateHost(mcpExt, tt.listenerConfig)
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
			command: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=debug", "--cache-connection-string=redis://localhost"},
			want:    []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080"},
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
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=debug"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=debug"},
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
			existing: []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=debug", "--session-length=3600"},
			want:     []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080", "--log-level=debug", "--session-length=3600"},
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
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "CACHE_CONNECTION_STRING", Value: "redis://localhost"},
			},
			want: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"},
				{Name: "CACHE_CONNECTION_STRING", Value: "redis://localhost"},
			},
		},
		{
			name: "no managed vars",
			env: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
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
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "http://keycloak/realms/mcp"},
			},
			want: []corev1.EnvVar{
				{Name: "TRUSTED_HEADER_PUBLIC_KEY", Value: "key1"},
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
				{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "http://keycloak/realms/mcp"},
			},
		},
		{
			name:    "no desired managed vars still preserves user vars",
			desired: nil,
			existing: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
			},
			want: []corev1.EnvVar{
				{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
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
		{Name: "OAUTH_RESOURCE_NAME", Value: "MCP Server"},
		{Name: "OAUTH_AUTHORIZATION_SERVERS", Value: "http://keycloak/realms/mcp"},
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
	if len(route.Spec.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(route.Spec.Rules))
	}

	var found bool
	for _, f := range route.Spec.Rules[0].Filters {
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

	deployment := r.buildBrokerRouterDeployment(mcpExt, "mcp.example.com", mcpExt.InternalHost(8080))
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
