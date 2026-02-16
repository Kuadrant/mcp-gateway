package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// broker-router deployment constants
	brokerRouterName = "mcp-gateway"

	// DefaultBrokerRouterImage is the default image for the broker-router deployment
	DefaultBrokerRouterImage = "ghcr.io/kuadrant/mcp-gateway:latest"

	// broker-router ports
	brokerHTTPPort   = 8080
	brokerGRPCPort   = 50051
	brokerConfigPort = 8181
)

// flags that can be changed directly on the deployment without triggering an update
var ignoredCommandFlags = []string{
	"--cache-connection-string",
	"--log-level",
	"--log-format",
	"--session-length",
}

func brokerRouterLabels() map[string]string {
	return map[string]string{
		labelAppName:   brokerRouterName,
		labelManagedBy: labelManagedByValue,
	}
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterDeployment(mcpExt *mcpv1alpha1.MCPGatewayExtension) *appsv1.Deployment {
	labels := brokerRouterLabels()
	replicas := int32(1)

	command := []string{"./mcp_gateway", fmt.Sprintf("--mcp-broker-public-address=0.0.0.0:%d", brokerHTTPPort),
		"--mcp-gateway-private-host=" + mcpExt.InternalHost(),
		"--mcp-gateway-config=/config/config.yaml"}
	// annotation takes precedence over reconciler default
	pollInterval := mcpExt.PollInterval()
	if pollInterval == "" {
		pollInterval = r.BrokerPollInterval
	}
	if pollInterval != "" {
		command = append(command, "--mcp-check-interval="+pollInterval)
	}
	command = append(command, "--mcp-gateway-public-host="+mcpExt.PublicHost())
	command = append(command, "--mcp-router-key="+routerKey(mcpExt))
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      brokerRouterName,
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            brokerRouterName,
							Image:           r.BrokerRouterImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         command,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: brokerHTTPPort,
								},
								{
									Name:          "grpc",
									ContainerPort: brokerGRPCPort,
								},
								{
									Name:          "config",
									ContainerPort: brokerConfigPort,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config-volume",
									MountPath: "/config",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config-volume",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: "mcp-gateway-config",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterService(mcpExt *mcpv1alpha1.MCPGatewayExtension) *corev1.Service {
	labels := brokerRouterLabels()

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      brokerRouterName,
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				labelAppName: brokerRouterName,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       brokerHTTPPort,
					TargetPort: intstr.FromInt(brokerHTTPPort),
				},
				{
					Name:       "grpc",
					Port:       brokerGRPCPort,
					TargetPort: intstr.FromInt(brokerGRPCPort),
				},
			},
		},
	}
}

// routerKey generates a deterministic key for hair-pinning requests based on the extension's UID
func routerKey(mcpExt *mcpv1alpha1.MCPGatewayExtension) string {
	hash := sha256.Sum256([]byte(mcpExt.UID))
	return hex.EncodeToString(hash[:16])
}

func (r *MCPGatewayExtensionReconciler) reconcileBrokerRouter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (bool, error) {
	// reconcile deployment
	deployment := r.buildBrokerRouterDeployment(mcpExt)
	if err := controllerutil.SetControllerReference(mcpExt, deployment, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on deployment: %w", err)
	}

	existingDeployment := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(deployment), existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating broker-router deployment", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, deployment); err != nil {
				return false, fmt.Errorf("failed to create deployment: %w", err)
			}
			return false, nil // deployment just created, not ready yet
		}
		return false, fmt.Errorf("failed to get deployment: %w", err)
	}

	if deploymentNeedsUpdate(deployment, existingDeployment) {
		r.log.Info("updating broker-router deployment", "namespace", mcpExt.Namespace)
		existingDeployment.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
		existingDeployment.Spec.Template.Spec.Volumes = deployment.Spec.Template.Spec.Volumes
		if err := r.Update(ctx, existingDeployment); err != nil {
			return false, fmt.Errorf("failed to update deployment: %w", err)
		}
	}

	// reconcile service
	service := r.buildBrokerRouterService(mcpExt)
	if err := controllerutil.SetControllerReference(mcpExt, service, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on service: %w", err)
	}

	existingService := &corev1.Service{}
	err = r.Get(ctx, client.ObjectKeyFromObject(service), existingService)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating broker-router service", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, service); err != nil {
				return false, fmt.Errorf("failed to create service: %w", err)
			}
		} else {
			return false, fmt.Errorf("failed to get service: %w", err)
		}
	} else if serviceNeedsUpdate(service, existingService) {
		r.log.Info("updating broker-router service", "namespace", mcpExt.Namespace)
		existingService.Spec.Ports = service.Spec.Ports
		existingService.Spec.Selector = service.Spec.Selector
		if err := r.Update(ctx, existingService); err != nil {
			return false, fmt.Errorf("failed to update service: %w", err)
		}
	}

	// check deployment readiness
	deploymentReady := existingDeployment.Status.ReadyReplicas > 0 &&
		existingDeployment.Status.ReadyReplicas == existingDeployment.Status.Replicas

	return deploymentReady, nil
}

func serviceNeedsUpdate(desired, existing *corev1.Service) bool {
	if !equality.Semantic.DeepEqual(desired.Spec.Ports, existing.Spec.Ports) {
		return true
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Selector, existing.Spec.Selector) {
		return true
	}
	return false
}

func deploymentNeedsUpdate(desired, existing *appsv1.Deployment) bool {
	if len(desired.Spec.Template.Spec.Containers) == 0 || len(existing.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	desiredContainer := desired.Spec.Template.Spec.Containers[0]
	existingContainer := existing.Spec.Template.Spec.Containers[0]

	if desiredContainer.Image != existingContainer.Image {
		return true
	}
	// filter out flags that can be changed directly on the deployment
	desiredCmd := filterIgnoredFlags(desiredContainer.Command)
	existingCmd := filterIgnoredFlags(existingContainer.Command)
	if !equality.Semantic.DeepEqual(desiredCmd, existingCmd) {
		return true
	}
	if !equality.Semantic.DeepEqual(desiredContainer.Ports, existingContainer.Ports) {
		return true
	}
	if !equality.Semantic.DeepEqual(desiredContainer.VolumeMounts, existingContainer.VolumeMounts) {
		return true
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Template.Spec.Volumes, existing.Spec.Template.Spec.Volumes) {
		return true
	}
	return false
}

func filterIgnoredFlags(command []string) []string {
	filtered := make([]string, 0, len(command))
	for _, arg := range command {
		ignore := false
		for _, flag := range ignoredCommandFlags {
			if strings.HasPrefix(arg, flag) {
				ignore = true
				break
			}
		}
		if !ignore {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}
