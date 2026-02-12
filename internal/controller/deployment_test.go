package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
