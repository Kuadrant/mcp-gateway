package controller

import (
	"strings"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
								Command: []string{"./app", "--flag=value"},
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
			name: "command changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = []string{"./app", "--flag=changed"}
			},
			expected: true,
		},
		{
			name: "command added",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--new-flag",
				)
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
			name: "ignored flag cache-connection-string changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--cache-connection-string=redis://localhost:6379",
				)
			},
			expected: false,
		},
		{
			name: "ignored flag log-level changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--log-level=debug",
				)
			},
			expected: false,
		},
		{
			name: "ignored flag log-format changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--log-format=json",
				)
			},
			expected: false,
		},
		{
			name: "ignored flag session-length changed",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--session-length=3600",
				)
			},
			expected: false,
		},
		{
			name: "non-ignored flag still triggers update",
			modify: func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Command = append(
					d.Spec.Template.Spec.Containers[0].Command,
					"--some-other-flag=value",
				)
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := baseDeployment()
			existing := baseDeployment()
			tt.modify(existing)

			result := deploymentNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("deploymentNeedsUpdate() = %v, expected %v", result, tt.expected)
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

	if deploymentNeedsUpdate(desired, existing) {
		t.Error("deploymentNeedsUpdate() should return false when desired has no containers")
	}

	if deploymentNeedsUpdate(existing, desired) {
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

			result := serviceNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("serviceNeedsUpdate() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestBuildBrokerRouterDeployment_PublicHost(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantFlag    string
	}{
		{
			name:        "public host from annotation",
			annotations: map[string]string{mcpv1alpha1.AnnotationPublicHost: "mcp.example.com"},
			wantFlag:    "--mcp-gateway-public-host=mcp.example.com",
		},
		{
			name:        "empty public host when no annotation",
			annotations: nil,
			wantFlag:    "--mcp-gateway-public-host=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage: "test-image:v1",
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-ext",
					Namespace:   "test-ns",
					Annotations: tt.annotations,
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt)
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

			deployment := r.buildBrokerRouterDeployment(mcpExt)
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
		name               string
		annotations        map[string]string
		reconcilerInterval string
		wantFlag           string
		wantAbsent         bool
	}{
		{
			name:               "poll interval from annotation",
			annotations:        map[string]string{mcpv1alpha1.AnnotationPollInterval: "30s"},
			reconcilerInterval: "",
			wantFlag:           "--mcp-check-interval=30s",
		},
		{
			name:               "poll interval from reconciler when no annotation",
			annotations:        nil,
			reconcilerInterval: "60s",
			wantFlag:           "--mcp-check-interval=60s",
		},
		{
			name:               "no poll interval flag when both empty",
			annotations:        nil,
			reconcilerInterval: "",
			wantAbsent:         true,
		},
		{
			name:               "annotation takes precedence over reconciler",
			annotations:        map[string]string{mcpv1alpha1.AnnotationPollInterval: "15s"},
			reconcilerInterval: "60s",
			wantFlag:           "--mcp-check-interval=15s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &MCPGatewayExtensionReconciler{
				BrokerRouterImage:  "test-image:v1",
				BrokerPollInterval: tt.reconcilerInterval,
			}
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-ext",
					Namespace:   "test-ns",
					Annotations: tt.annotations,
				},
				Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
					TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
						Name:      "my-gateway",
						Namespace: "gateway-system",
					},
				},
			}

			deployment := r.buildBrokerRouterDeployment(mcpExt)
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

func TestBuildBrokerRouterDeployment_RouterKey(t *testing.T) {
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

	deployment := r.buildBrokerRouterDeployment(mcpExt)
	command := deployment.Spec.Template.Spec.Containers[0].Command

	// verify router key flag is present
	found := false
	var keyValue string
	for _, arg := range command {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			found = true
			keyValue = strings.TrimPrefix(arg, "--mcp-router-key=")
			break
		}
	}
	if !found {
		t.Fatalf("expected command to contain --mcp-router-key flag, got %v", command)
	}
	if keyValue == "" {
		t.Error("expected router key to have a non-empty value")
	}

	// verify key is deterministic for same UID
	deployment2 := r.buildBrokerRouterDeployment(mcpExt)
	command2 := deployment2.Spec.Template.Spec.Containers[0].Command
	var keyValue2 string
	for _, arg := range command2 {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			keyValue2 = strings.TrimPrefix(arg, "--mcp-router-key=")
			break
		}
	}
	if keyValue != keyValue2 {
		t.Errorf("expected same key for same UID, got %q and %q", keyValue, keyValue2)
	}

	// verify different UID produces different key
	mcpExt2 := mcpExt.DeepCopy()
	mcpExt2.UID = types.UID("different-uid-67890")
	deployment3 := r.buildBrokerRouterDeployment(mcpExt2)
	command3 := deployment3.Spec.Template.Spec.Containers[0].Command
	var keyValue3 string
	for _, arg := range command3 {
		if strings.HasPrefix(arg, "--mcp-router-key=") {
			keyValue3 = strings.TrimPrefix(arg, "--mcp-router-key=")
			break
		}
	}
	if keyValue == keyValue3 {
		t.Errorf("expected different key for different UID, both got %q", keyValue)
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

			result := serviceAccountNeedsUpdate(desired, existing)
			if result != tt.expected {
				t.Errorf("serviceAccountNeedsUpdate() = %v, expected %v", result, tt.expected)
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

	deployment := r.buildBrokerRouterDeployment(mcpExt)

	if deployment.Spec.Template.Spec.ServiceAccountName != brokerRouterName {
		t.Errorf("expected ServiceAccountName %q, got %q", brokerRouterName, deployment.Spec.Template.Spec.ServiceAccountName)
	}
	if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken != false {
		t.Errorf("expected AutomountServiceAccountToken to be false on deployment pod spec")
	}
}
