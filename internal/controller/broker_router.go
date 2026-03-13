package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strings"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// broker-router deployment constants
	brokerRouterName     = "mcp-gateway"
	gatewayHTTPRouteName = "mcp-gateway-route"

	// DefaultBrokerRouterImage is the default image for the broker-router deployment
	DefaultBrokerRouterImage = "ghcr.io/kuadrant/mcp-gateway:latest"

	// broker-router ports
	brokerHTTPPort   = 8080
	brokerGRPCPort   = 50051
	brokerConfigPort = 8181
)

// managedCommandFlags are the flags the controller owns and reconciles.
// Any flag not in this list is user-managed and preserved as-is.
var managedCommandFlags = []string{
	"--mcp-broker-public-address",
	"--mcp-gateway-private-host",
	"--mcp-gateway-config",
	"--mcp-check-interval",
	"--mcp-gateway-public-host",
	"--mcp-router-key",
}

func brokerRouterLabels() map[string]string {
	return map[string]string{
		labelAppName:   brokerRouterName,
		labelManagedBy: labelManagedByValue,
	}
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterDeployment(mcpExt *mcpv1alpha1.MCPGatewayExtension, publicHost, internalHost string) *appsv1.Deployment {
	labels := brokerRouterLabels()
	replicas := int32(1)

	command := []string{"./mcp_gateway", fmt.Sprintf("--mcp-broker-public-address=0.0.0.0:%d", brokerHTTPPort),
		"--mcp-gateway-private-host=" + internalHost,
		"--mcp-gateway-config=/config/config.yaml"}
	// only override the binary's default (60s) when explicitly set in spec
	if mcpExt.Spec.BackendPingIntervalSeconds != nil {
		command = append(command, fmt.Sprintf("--mcp-check-interval=%d", *mcpExt.Spec.BackendPingIntervalSeconds))
	}
	command = append(command, "--mcp-gateway-public-host="+publicHost)
	command = append(command, "--mcp-router-key="+routerKey(mcpExt))

	var envVars []corev1.EnvVar
	if mcpExt.Spec.TrustedHeadersKey != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "TRUSTED_HEADER_PUBLIC_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: mcpExt.Spec.TrustedHeadersKey.SecretName,
					},
					Key: "key",
				},
			},
		})
	}
	if mcpExt.Spec.SessionStore != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "CACHE_CONNECTION_STRING",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: mcpExt.Spec.SessionStore.SecretName,
					},
					Key: "REDIS_URL",
				},
			},
		})
	}

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
					ServiceAccountName:           brokerRouterName,
					AutomountServiceAccountToken: ptr.To(false),
					Containers: []corev1.Container{
						{
							Name:            brokerRouterName,
							Image:           r.BrokerRouterImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         command,
							Env:             envVars,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: brokerHTTPPort,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "grpc",
									ContainerPort: brokerGRPCPort,
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "config",
									ContainerPort: brokerConfigPort,
									Protocol:      corev1.ProtocolTCP,
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
									SecretName:  "mcp-gateway-config",
									DefaultMode: ptr.To(int32(420)), // 0644 octal
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterServiceAccount(mcpExt *mcpv1alpha1.MCPGatewayExtension) *corev1.ServiceAccount {
	labels := brokerRouterLabels()
	automount := false

	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      brokerRouterName,
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		AutomountServiceAccountToken: &automount,
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
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "grpc",
					Port:       brokerGRPCPort,
					TargetPort: intstr.FromInt(brokerGRPCPort),
					Protocol:   corev1.ProtocolTCP,
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

// stripPort removes port suffix from a host string (e.g. "example.com:8001" -> "example.com")
func stripPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	return h
}

// derivePublicHost determines the public host for the MCP Gateway.
// priority: annotation override > listener hostname.
// For wildcard hostnames (*.example.com), we use mcp.example.com as the default subdomain.
// Any port suffix is stripped since HTTPRoute hostnames don't allow ports.
func derivePublicHost(listenerConfig *mcpv1alpha1.ListenerConfig, annotationOverride string) (string, error) {
	var hostname string
	// annotation takes precedence for backwards compatibility
	if annotationOverride != "" {
		if strings.Contains(annotationOverride, "://") {
			return "", fmt.Errorf("invalid public host %q: must be a hostname, not a URL", annotationOverride)
		}
		hostname = stripPort(annotationOverride)
	} else if listenerConfig != nil && listenerConfig.Hostname != "" {
		hostname = listenerConfig.Hostname
		// handle wildcard hostnames: *.example.com -> mcp.example.com
		if strings.HasPrefix(hostname, "*.") {
			hostname = "mcp" + hostname[1:]
		}
	}
	if hostname == "" {
		return "", fmt.Errorf("unable to derive public host: no hostname available")
	}
	if errs := validation.IsDNS1123Subdomain(hostname); len(errs) > 0 {
		return "", fmt.Errorf("invalid public host %q: %s", hostname, strings.Join(errs, "; "))
	}
	return hostname, nil
}

func (r *MCPGatewayExtensionReconciler) reconcileBrokerRouter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, listenerConfig *mcpv1alpha1.ListenerConfig) (bool, error) {
	// derive values from listener config before building resources
	publicHost, err := derivePublicHost(listenerConfig, mcpExt.Spec.PublicHost)
	if err != nil {
		return false, newValidationError(mcpv1alpha1.ConditionReasonInvalid, err.Error())
	}
	internalHost := mcpExt.InternalHost(listenerConfig.Port)

	// reconcile service account (must exist before deployment)
	serviceAccount := r.buildBrokerRouterServiceAccount(mcpExt)
	if err := controllerutil.SetControllerReference(mcpExt, serviceAccount, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on service account: %w", err)
	}

	existingServiceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(serviceAccount), existingServiceAccount); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating broker-router service account", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, serviceAccount); err != nil {
				return false, fmt.Errorf("failed to create service account: %w", err)
			}
		} else {
			return false, fmt.Errorf("failed to get service account: %w", err)
		}
	} else if needsUpdate, reason := serviceAccountNeedsUpdate(serviceAccount, existingServiceAccount); needsUpdate {
		r.log.Info("updating broker-router service account", "namespace", mcpExt.Namespace, "reason", reason)
		existingServiceAccount.AutomountServiceAccountToken = serviceAccount.AutomountServiceAccountToken
		if err := r.Update(ctx, existingServiceAccount); err != nil {
			return false, fmt.Errorf("failed to update service account: %w", err)
		}
	}

	// reconcile deployment
	deployment := r.buildBrokerRouterDeployment(mcpExt, publicHost, internalHost)
	if err := controllerutil.SetControllerReference(mcpExt, deployment, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on deployment: %w", err)
	}

	existingDeployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(deployment), existingDeployment); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating broker-router deployment", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, deployment); err != nil {
				return false, fmt.Errorf("failed to create deployment: %w", err)
			}
			return false, nil // deployment just created, not ready yet
		}
		return false, fmt.Errorf("failed to get deployment: %w", err)
	}

	if needsUpdate, reason := deploymentNeedsUpdate(deployment, existingDeployment); needsUpdate {
		r.log.Info("updating broker-router deployment", "namespace", mcpExt.Namespace, "reason", reason)
		desiredContainer := deployment.Spec.Template.Spec.Containers[0]
		existingContainer := existingDeployment.Spec.Template.Spec.Containers[0]
		// merge command: update managed flags, preserve user flags
		existingContainer.Command = mergeCommand(desiredContainer.Command, existingContainer.Command)
		existingContainer.Image = desiredContainer.Image
		existingContainer.Ports = desiredContainer.Ports
		existingContainer.Env = desiredContainer.Env
		existingContainer.VolumeMounts = desiredContainer.VolumeMounts
		existingDeployment.Spec.Template.Spec.Containers[0] = existingContainer
		existingDeployment.Spec.Template.Spec.Volumes = deployment.Spec.Template.Spec.Volumes
		if err := r.Update(ctx, existingDeployment); err != nil {
			return false, fmt.Errorf("failed to update deployment: %w", err)
		}
		return false, nil // deployment updated, requeue to get fresh status
	}

	// reconcile service
	service := r.buildBrokerRouterService(mcpExt)
	if err := controllerutil.SetControllerReference(mcpExt, service, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on service: %w", err)
	}

	existingService := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(service), existingService); err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating broker-router service", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, service); err != nil {
				return false, fmt.Errorf("failed to create service: %w", err)
			}
		} else {
			return false, fmt.Errorf("failed to get service: %w", err)
		}
	} else if needsUpdate, reason := serviceNeedsUpdate(service, existingService); needsUpdate {
		r.log.Info("updating broker-router service", "namespace", mcpExt.Namespace, "reason", reason)
		existingService.Spec.Ports = service.Spec.Ports
		existingService.Spec.Selector = service.Spec.Selector
		if err := r.Update(ctx, existingService); err != nil {
			return false, fmt.Errorf("failed to update service: %w", err)
		}
	}

	// reconcile gateway HTTPRoute (unless disabled by spec)
	if !mcpExt.HTTPRouteDisabled() {
		httpRoute := r.buildGatewayHTTPRoute(mcpExt, publicHost)
		if err := controllerutil.SetControllerReference(mcpExt, httpRoute, r.Scheme); err != nil {
			return false, fmt.Errorf("failed to set controller reference on httproute: %w", err)
		}

		existingHTTPRoute := &gatewayv1.HTTPRoute{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(httpRoute), existingHTTPRoute); err != nil {
			if apierrors.IsNotFound(err) {
				r.log.Info("creating gateway httproute", "namespace", mcpExt.Namespace)
				if err := r.Create(ctx, httpRoute); err != nil {
					return false, fmt.Errorf("failed to create httproute: %w", err)
				}
			} else {
				return false, fmt.Errorf("failed to get httproute: %w", err)
			}
		} else if needsUpdate, reason := httpRouteNeedsUpdate(httpRoute, existingHTTPRoute); needsUpdate {
			r.log.Info("updating gateway httproute", "namespace", mcpExt.Namespace, "reason", reason)
			existingHTTPRoute.Spec.ParentRefs = httpRoute.Spec.ParentRefs
			existingHTTPRoute.Spec.Hostnames = httpRoute.Spec.Hostnames
			existingHTTPRoute.Spec.Rules = httpRoute.Spec.Rules
			if err := r.Update(ctx, existingHTTPRoute); err != nil {
				return false, fmt.Errorf("failed to update httproute: %w", err)
			}
		}
	}

	// check deployment readiness
	deploymentReady := existingDeployment.Status.ReadyReplicas > 0 &&
		existingDeployment.Status.ReadyReplicas == existingDeployment.Status.Replicas

	return deploymentReady, nil
}

// serviceNeedsUpdate checks if the service needs to be updated
// returns (needsUpdate, reason) where reason describes what changed
func serviceNeedsUpdate(desired, existing *corev1.Service) (bool, string) {
	if !equality.Semantic.DeepEqual(desired.Spec.Ports, existing.Spec.Ports) {
		return true, fmt.Sprintf("ports changed: %+v -> %+v", existing.Spec.Ports, desired.Spec.Ports)
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Selector, existing.Spec.Selector) {
		return true, fmt.Sprintf("selector changed: %v -> %v", existing.Spec.Selector, desired.Spec.Selector)
	}
	return false, ""
}

// serviceAccountNeedsUpdate checks if the service account needs to be updated
// returns (needsUpdate, reason) where reason describes what changed
func serviceAccountNeedsUpdate(desired, existing *corev1.ServiceAccount) (bool, string) {
	if !equality.Semantic.DeepEqual(desired.AutomountServiceAccountToken, existing.AutomountServiceAccountToken) {
		return true, fmt.Sprintf("automountServiceAccountToken changed: %v -> %v",
			existing.AutomountServiceAccountToken, desired.AutomountServiceAccountToken)
	}
	return false, ""
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
// returns (needsUpdate, reason) where reason describes what changed
func deploymentNeedsUpdate(desired, existing *appsv1.Deployment) (bool, string) {
	if len(desired.Spec.Template.Spec.Containers) == 0 || len(existing.Spec.Template.Spec.Containers) == 0 {
		return false, ""
	}
	desiredContainer := desired.Spec.Template.Spec.Containers[0]
	existingContainer := existing.Spec.Template.Spec.Containers[0]

	if desiredContainer.Image != existingContainer.Image {
		return true, fmt.Sprintf("image changed: %q -> %q", existingContainer.Image, desiredContainer.Image)
	}
	// only compare flags the controller manages; user-added flags are preserved
	desiredCmd := filterManagedFlags(desiredContainer.Command)
	existingCmd := filterManagedFlags(existingContainer.Command)
	if !equality.Semantic.DeepEqual(desiredCmd, existingCmd) {
		return true, fmt.Sprintf("command changed: %v -> %v", existingCmd, desiredCmd)
	}
	if !equality.Semantic.DeepEqual(desiredContainer.Ports, existingContainer.Ports) {
		return true, fmt.Sprintf("ports changed: %+v -> %+v", existingContainer.Ports, desiredContainer.Ports)
	}
	if !equality.Semantic.DeepEqual(desiredContainer.VolumeMounts, existingContainer.VolumeMounts) {
		return true, fmt.Sprintf("volumeMounts changed: %+v -> %+v", existingContainer.VolumeMounts, desiredContainer.VolumeMounts)
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Template.Spec.Volumes, existing.Spec.Template.Spec.Volumes) {
		return true, fmt.Sprintf("volumes changed: %+v -> %+v", existing.Spec.Template.Spec.Volumes, desired.Spec.Template.Spec.Volumes)
	}
	if !equality.Semantic.DeepEqual(desiredContainer.Env, existingContainer.Env) {
		return true, fmt.Sprintf("env changed: %+v -> %+v", existingContainer.Env, desiredContainer.Env)
	}
	return false, ""
}

// filterManagedFlags returns only the binary name and flags the controller manages.
func filterManagedFlags(command []string) []string {
	var out []string
	for _, arg := range command {
		if !strings.HasPrefix(arg, "--") || slices.ContainsFunc(managedCommandFlags, func(flag string) bool {
			return strings.HasPrefix(arg, flag)
		}) {
			out = append(out, arg)
		}
	}
	return out
}

// mergeCommand takes the desired command from the controller and the existing
// command from the deployment. It returns a merged command that preserves any
// user-added flags while updating controller-managed flags.
func mergeCommand(desired, existing []string) []string {
	// start with all user flags from the existing command
	var userFlags []string
	for _, arg := range existing {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		if !slices.ContainsFunc(managedCommandFlags, func(flag string) bool {
			return strings.HasPrefix(arg, flag)
		}) {
			userFlags = append(userFlags, arg)
		}
	}
	return slices.Concat(desired, userFlags)
}

func (r *MCPGatewayExtensionReconciler) buildGatewayHTTPRoute(mcpExt *mcpv1alpha1.MCPGatewayExtension, publicHost string) *gatewayv1.HTTPRoute {
	labels := brokerRouterLabels()
	pathType := gatewayv1.PathMatchPathPrefix
	pathValue := "/mcp"
	port := gatewayv1.PortNumber(brokerHTTPPort)
	gatewayNamespace := gatewayv1.Namespace(mcpExt.Spec.TargetRef.Namespace)
	sectionName := gatewayv1.SectionName(mcpExt.Spec.TargetRef.SectionName)

	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayHTTPRouteName,
			Namespace: mcpExt.Namespace,
			Labels:    labels,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:       ptr.To(gatewayv1.Group("gateway.networking.k8s.io")),
						Kind:        ptr.To(gatewayv1.Kind("Gateway")),
						Name:        gatewayv1.ObjectName(mcpExt.Spec.TargetRef.Name),
						Namespace:   &gatewayNamespace,
						SectionName: &sectionName,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{
				gatewayv1.Hostname(publicHost),
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: &pathValue,
							},
						},
					},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(brokerRouterName),
									Port: &port,
								},
							},
						},
					},
				},
			},
		},
	}
}

// httpRouteNeedsUpdate checks if the HTTPRoute needs to be updated
func httpRouteNeedsUpdate(desired, existing *gatewayv1.HTTPRoute) (bool, string) {
	if !equality.Semantic.DeepEqual(desired.Spec.ParentRefs, existing.Spec.ParentRefs) {
		return true, "parentRefs changed"
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Hostnames, existing.Spec.Hostnames) {
		return true, fmt.Sprintf("hostnames changed: %v -> %v", existing.Spec.Hostnames, desired.Spec.Hostnames)
	}
	if !equality.Semantic.DeepEqual(desired.Spec.Rules, existing.Spec.Rules) {
		return true, "rules changed"
	}
	return false, ""
}
