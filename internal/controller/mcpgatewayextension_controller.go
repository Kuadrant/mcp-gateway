package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/go-logr/logr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	istiov1alpha3 "istio.io/api/networking/v1alpha3"
	istiotypev1beta1 "istio.io/api/type/v1beta1"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	mcpGatewayFinalizer = "mcp.kagenti.com/finalizer"
	// gatewayIndexKey is the index used to improve look up of mcpgatewayextensions related to a gateway
	gatewayIndexKey  = "spec.targetRef.gateway"
	refGrantIndexKey = "spec.from.ref"

	// broker-router deployment constants
	brokerRouterName    = "mcp-gateway"
	labelAppName        = "app.kubernetes.io/name"
	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelManagedByValue = "mcp-gateway-controller"

	// DefaultBrokerRouterImage is the default image for the broker-router deployment
	DefaultBrokerRouterImage = "ghcr.io/kuadrant/mcp-gateway:latest"

	// envoy filter labels
	labelExtensionName      = "mcp.kagenti.com/extension-name"
	labelExtensionNamespace = "mcp.kagenti.com/extension-namespace"
	labelIstioRev           = "istio.io/rev"
)

func brokerRouterLabels() map[string]string {
	return map[string]string{
		labelAppName:   brokerRouterName,
		labelManagedBy: labelManagedByValue,
	}
}

func envoyFilterLabels(mcpExt *mcpv1alpha1.MCPGatewayExtension) map[string]string {
	return map[string]string{
		labelAppName:            brokerRouterName,
		labelManagedBy:          labelManagedByValue,
		labelExtensionName:      mcpExt.Name,
		labelExtensionNamespace: mcpExt.Namespace,
		labelIstioRev:           "default",
	}
}

// envoyFilterManagedLabelKeys lists the labels we manage on EnvoyFilter resources
var envoyFilterManagedLabelKeys = []string{
	labelAppName,
	labelManagedBy,
	labelExtensionName,
	labelExtensionNamespace,
	labelIstioRev,
}

// validationError represents a validation error with a reason and message for status reporting
type validationError struct {
	reason  string
	message string
}

func (e *validationError) Error() string {
	return e.message
}

func newValidationError(reason, message string) *validationError {
	return &validationError{reason: reason, message: message}
}

// ConfigWriterDeleter writes and deletes config
type ConfigWriterDeleter interface {
	DeleteConfig(ctx context.Context, namespaceName types.NamespacedName) error
	EnsureConfigExists(ctx context.Context, namespaceName types.NamespacedName) error
	WriteEmptyConfig(ctx context.Context, namespaceName types.NamespacedName) error
}

// MCPGatewayExtensionReconciler reconciles a MCPGatewayExtension object
type MCPGatewayExtensionReconciler struct {
	client.Client
	DirectAPIReader       client.Reader
	Scheme                *runtime.Scheme
	log                   *slog.Logger
	ConfigWriterDeleter   ConfigWriterDeleter
	MCPExtFinderValidator MCPGatewayExtensionFinderValidator
	BrokerRouterImage     string
	BrokerPollInterval    string // optional poll interval in seconds for broker health checks
}

// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=envoyfilters,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles an MCPGatewayExtension resource. Deploying and configuring a MCP Gateway instance configured to integrate and provide MCP functionality with the targeted gateway
func (r *MCPGatewayExtensionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mcpExt := &mcpv1alpha1.MCPGatewayExtension{}
	if err := r.Get(ctx, req.NamespacedName, mcpExt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	r.log.Info("reconciling mcpgatewayextension", "name", mcpExt.Name)

	if !mcpExt.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, mcpExt)
	}

	if added, err := r.ensureFinalizer(ctx, mcpExt); added || err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcileActive(ctx, mcpExt)
}

func (r *MCPGatewayExtensionReconciler) handleDeletion(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(mcpExt, mcpGatewayFinalizer) {
		return ctrl.Result{}, nil
	}

	r.log.Info("deleting mcpgatewayextension", "name", mcpExt.Name, "namespace", mcpExt.Namespace)

	if err := r.deleteEnvoyFilter(ctx, mcpExt); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ConfigWriterDeleter.WriteEmptyConfig(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(mcpExt, mcpGatewayFinalizer)
	return ctrl.Result{}, r.Update(ctx, mcpExt)
}

func (r *MCPGatewayExtensionReconciler) ensureFinalizer(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (bool, error) {
	if controllerutil.ContainsFinalizer(mcpExt, mcpGatewayFinalizer) {
		return false, nil
	}

	controllerutil.AddFinalizer(mcpExt, mcpGatewayFinalizer)
	return true, r.Update(ctx, mcpExt)
}

func (r *MCPGatewayExtensionReconciler) reconcileActive(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (ctrl.Result, error) {
	targetGateway, err := r.validateGatewayTarget(ctx, mcpExt)
	if err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	if err := r.checkGatewayConflict(ctx, mcpExt, targetGateway); err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	if err := r.ConfigWriterDeleter.EnsureConfigExists(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	deploymentReady, err := r.reconcileBrokerRouter(ctx, mcpExt)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !deploymentReady {
		return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonDeploymentNotReady, "broker-router deployment is not ready")
	}

	if err := r.reconcileEnvoyFilter(ctx, mcpExt, targetGateway); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "successfully verified and configured")
}

func (r *MCPGatewayExtensionReconciler) validateGatewayTarget(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (*gatewayv1.Gateway, error) {
	targetGateway, err := r.gatewayTarget(ctx, mcpExt.Spec.TargetRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, newValidationError(mcpv1alpha1.ConditionReasonInvalid,
				fmt.Sprintf("gateway %s/%s not found", mcpExt.Spec.TargetRef.Namespace, mcpExt.Spec.TargetRef.Name))
		}
		return nil, err
	}

	// cross-namespace reference requires ReferenceGrant
	if mcpExt.Spec.TargetRef.Namespace != mcpExt.Namespace {
		hasGrant, err := r.MCPExtFinderValidator.HasValidReferenceGrant(ctx, mcpExt)
		if err != nil {
			return nil, err
		}

		if !hasGrant {
			r.log.Info("no valid ReferenceGrant for cross-namespace reference",
				"extension", mcpExt.Name, "extension-namespace", mcpExt.Namespace,
				"gateway-namespace", mcpExt.Spec.TargetRef.Namespace)
			if err := r.ConfigWriterDeleter.WriteEmptyConfig(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
				return nil, err
			}
			return nil, newValidationError(mcpv1alpha1.ConditionReasonRefGrantRequired,
				fmt.Sprintf("ReferenceGrant required in namespace %s to allow cross-namespace reference from %s", mcpExt.Spec.TargetRef.Namespace, mcpExt.Namespace))
		}
	}

	return targetGateway, nil
}

func (r *MCPGatewayExtensionReconciler) checkGatewayConflict(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway) error {
	existingExts, err := r.listMCPGatewayExtsForGateway(ctx, targetGateway)
	if err != nil {
		return fmt.Errorf("failed to list extensions for gateway: %w", err)
	}

	if len(existingExts.Items) <= 1 {
		return nil
	}

	oldest := findOldestExtension(existingExts.Items)
	if oldest.GetUID() == mcpExt.GetUID() {
		return nil
	}

	return newValidationError(mcpv1alpha1.ConditionReasonInvalid,
		fmt.Sprintf("gateway %s/%s is already configured by MCPGatewayExtension %s/%s",
			targetGateway.Namespace, targetGateway.Name, oldest.Namespace, oldest.Name))
}

func findOldestExtension(exts []mcpv1alpha1.MCPGatewayExtension) *mcpv1alpha1.MCPGatewayExtension {
	oldest := &exts[0]
	for i := range exts[1:] {
		if exts[i+1].CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = &exts[i+1]
		}
	}
	return oldest
}

func (r *MCPGatewayExtensionReconciler) updateStatus(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, status metav1.ConditionStatus, reason, message string) error {
	existing := meta.FindStatusCondition(mcpExt.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
	var existingCopy metav1.Condition
	if existing != nil {
		existingCopy = *existing
	}

	mcpExt.SetReadyCondition(status, reason, message)
	updated := meta.FindStatusCondition(mcpExt.Status.Conditions, mcpv1alpha1.ConditionTypeReady)

	if existing != nil && equality.Semantic.DeepEqual(existingCopy, *updated) {
		return nil
	}
	return r.Status().Update(ctx, mcpExt)
}

func (r *MCPGatewayExtensionReconciler) gatewayTarget(ctx context.Context, target mcpv1alpha1.MCPGatewayExtensionTargetReference) (*gatewayv1.Gateway, error) {
	g := &gatewayv1.Gateway{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name}, g); err != nil {
		// return NotFound errors unwrapped so caller can check with apierrors.IsNotFound
		if apierrors.IsNotFound(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to find gateway targeted by the mcpgatewayextension %w", err)
	}
	return g, nil
}

func mcpExtToRefGrantIndexValue(mext mcpv1alpha1.MCPGatewayExtension) string {
	return fmt.Sprintf("mcp.kagenti.com/MCPGatewayExtension/%s", mext.Namespace)
}

func mcpExtToGatewayIndexValue(mext mcpv1alpha1.MCPGatewayExtension) string {
	return fmt.Sprintf("%s/%s", mext.Spec.TargetRef.Namespace, mext.Spec.TargetRef.Name)
}

func gatewayToMCPExtIndexValue(g gatewayv1.Gateway) string {
	return fmt.Sprintf("%s/%s", g.Namespace, g.Name)
}

func refGrantToMCPExtIndexValue(r gatewayv1beta1.ReferenceGrant) string {
	f := r.Spec.From[0]
	return fmt.Sprintf("%s/%s/%s", f.Group, f.Kind, f.Namespace)
}

// setupIndexExtensionToGateway creates an index for the gateway targeted by an MCPGatewayExtension
func setupIndexExtensionToGateway(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(
		ctx,
		&mcpv1alpha1.MCPGatewayExtension{},
		gatewayIndexKey,
		func(obj client.Object) []string {
			mcpExt, ok := obj.(*mcpv1alpha1.MCPGatewayExtension)
			if !ok {
				return nil
			}
			return []string{mcpExtToGatewayIndexValue(*mcpExt)}
		},
	); err != nil {
		return fmt.Errorf("failed to setup index mcpgatewayextension to gateway: %w", err)
	}
	return nil
}

func (r *MCPGatewayExtensionReconciler) listMCPGatewayExtsForGateway(ctx context.Context, g *gatewayv1.Gateway) (*mcpv1alpha1.MCPGatewayExtensionList, error) {
	mcpGatewayExtList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{gatewayIndexKey: gatewayToMCPExtIndexValue(*g)},
	); err != nil {
		return mcpGatewayExtList, err
	}
	return mcpGatewayExtList, nil
}

func (r *MCPGatewayExtensionReconciler) enqueueMCPGatewayExtForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayv1.Gateway)
	requests := []reconcile.Request{}
	if !ok {
		return requests
	}
	mcpGatewayExtList, err := r.listMCPGatewayExtsForGateway(ctx, gateway)
	if err != nil {
		// just log as this is adhering to the EnqueueRequestsFromMapFunc signature
		r.log.Error("failed to list existing mcpgatewayextension for gateway", "error", err, "gateway", gateway)
		return requests
	}
	for _, ext := range mcpGatewayExtList.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ext)})
	}
	return requests
}

// setupIndexExtensionToReferenceGrant creates an index for ReferenceGrants allowing cross-namespace references
func setupIndexExtensionToReferenceGrant(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(
		ctx,
		&mcpv1alpha1.MCPGatewayExtension{},
		refGrantIndexKey,
		func(obj client.Object) []string {
			mcpGatewayExt, ok := obj.(*mcpv1alpha1.MCPGatewayExtension)
			if !ok {
				return nil
			}
			return []string{mcpExtToRefGrantIndexValue(*mcpGatewayExt)}
		},
	); err != nil {
		return fmt.Errorf("failed to setup index mcpgatewayextension to reference grants: %w", err)
	}
	return nil
}

func (r *MCPGatewayExtensionReconciler) enqueueMCPGatewayExtForReferenceGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	ref, ok := obj.(*gatewayv1beta1.ReferenceGrant)
	if !ok || len(ref.Spec.From) == 0 {
		return nil
	}

	mcpGatewayExtList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{refGrantIndexKey: refGrantToMCPExtIndexValue(*ref)},
	); err != nil {
		r.log.Error("failed to list mcpgatewayextensions for reference grant", "error", err)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(mcpGatewayExtList.Items))
	for _, ext := range mcpGatewayExtList.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ext)})
	}
	return requests
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterDeployment(mcpExt *mcpv1alpha1.MCPGatewayExtension) *appsv1.Deployment {
	labels := brokerRouterLabels()
	replicas := int32(1)
	gatewayNamespace := mcpExt.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = mcpExt.Namespace
	}
	internalHost := fmt.Sprintf("%s-istio.%s.svc.cluster.local:8080", mcpExt.Spec.TargetRef.Name, gatewayNamespace)
	command := []string{"./mcp_gateway", "--mcp-broker-public-address=0.0.0.0:8080",
		"--mcp-gateway-private-host=" + internalHost,
		"--mcp-gateway-config=/config/config.yaml"}
	if r.BrokerPollInterval != "" {
		command = append(command, "--mcp-check-interval="+r.BrokerPollInterval)
	}
	// TODO add publicHost to the spec of the MCPGatewayExtension
	command = append(command, "--mcp-gateway-public-host=mcp.127-0-0-1.sslip.io")
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
									ContainerPort: 8080,
								},
								{
									Name:          "grpc",
									ContainerPort: 50051,
								},
								// TODO is this config port still used?
								{
									Name:          "config",
									ContainerPort: 8181,
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

func (r *MCPGatewayExtensionReconciler) reconcileBrokerRouter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (bool, error) {
	logger := logf.FromContext(ctx)

	// reconcile deployment
	deployment := r.buildBrokerRouterDeployment(mcpExt)
	if err := controllerutil.SetControllerReference(mcpExt, deployment, r.Scheme); err != nil {
		return false, fmt.Errorf("failed to set controller reference on deployment: %w", err)
	}

	existingDeployment := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(deployment), existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("creating broker-router deployment", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, deployment); err != nil {
				return false, fmt.Errorf("failed to create deployment: %w", err)
			}
			return false, nil // deployment just created, not ready yet
		}
		return false, fmt.Errorf("failed to get deployment: %w", err)
	}

	if deploymentNeedsUpdate(deployment, existingDeployment) {
		logger.Info("updating broker-router deployment", "namespace", mcpExt.Namespace)
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
			logger.Info("creating broker-router service", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, service); err != nil {
				return false, fmt.Errorf("failed to create service: %w", err)
			}
		} else {
			return false, fmt.Errorf("failed to get service: %w", err)
		}
	} else if serviceNeedsUpdate(service, existingService) {
		logger.Info("updating broker-router service", "namespace", mcpExt.Namespace)
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
	// check ports
	if !equality.Semantic.DeepEqual(desired.Spec.Ports, existing.Spec.Ports) {
		return true
	}
	// check selector
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

	// check image
	if desiredContainer.Image != existingContainer.Image {
		return true
	}
	// check command
	if !equality.Semantic.DeepEqual(desiredContainer.Command, existingContainer.Command) {
		return true
	}
	// check ports
	if !equality.Semantic.DeepEqual(desiredContainer.Ports, existingContainer.Ports) {
		return true
	}
	// check volume mounts
	if !equality.Semantic.DeepEqual(desiredContainer.VolumeMounts, existingContainer.VolumeMounts) {
		return true
	}
	// check volumes
	if !equality.Semantic.DeepEqual(desired.Spec.Template.Spec.Volumes, existing.Spec.Template.Spec.Volumes) {
		return true
	}
	return false
}

func (r *MCPGatewayExtensionReconciler) buildEnvoyFilter(mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway) (*istionetv1alpha3.EnvoyFilter, error) {
	// build the ext_proc filter config as a structpb.Struct
	extProcConfig, err := structpb.NewStruct(map[string]any{
		"name": "envoy.filters.http.ext_proc",
		"typed_config": map[string]any{
			"@type":              "type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor",
			"failure_mode_allow": false,
			"mutation_rules": map[string]any{
				"allow_all_routing": true,
			},
			"message_timeout": "10s",
			"processing_mode": map[string]any{
				"request_header_mode":   "SEND",
				"response_header_mode":  "SEND",
				"request_body_mode":     "BUFFERED",
				"response_body_mode":    "NONE",
				"request_trailer_mode":  "SKIP",
				"response_trailer_mode": "SKIP",
			},
			"grpc_service": map[string]any{
				"envoy_grpc": map[string]any{
					"cluster_name": fmt.Sprintf("outbound|50051||%s.%s.svc.cluster.local", brokerRouterName, mcpExt.Namespace),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ext_proc config struct: %w", err)
	}

	// name the EnvoyFilter based on the MCPGatewayExtension to support multiple gateways
	envoyFilterName := fmt.Sprintf("mcp-ext-proc-%s-gateway", mcpExt.Namespace)

	return &istionetv1alpha3.EnvoyFilter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envoyFilterName,
			Namespace: targetGateway.Namespace,
			Labels:    envoyFilterLabels(mcpExt),
		},
		Spec: istiov1alpha3.EnvoyFilter{
			TargetRefs: []*istiotypev1beta1.PolicyTargetReference{
				{
					Group: "gateway.networking.k8s.io",
					Kind:  "Gateway",
					Name:  targetGateway.Name,
				},
			},
			ConfigPatches: []*istiov1alpha3.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					ApplyTo: istiov1alpha3.EnvoyFilter_HTTP_FILTER,
					Match: &istiov1alpha3.EnvoyFilter_EnvoyConfigObjectMatch{
						Context: istiov1alpha3.EnvoyFilter_GATEWAY,
						ObjectTypes: &istiov1alpha3.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
							Listener: &istiov1alpha3.EnvoyFilter_ListenerMatch{
								// TODO the port number cannot be hard coded
								PortNumber: 8080,
								FilterChain: &istiov1alpha3.EnvoyFilter_ListenerMatch_FilterChainMatch{
									Filter: &istiov1alpha3.EnvoyFilter_ListenerMatch_FilterMatch{
										Name: "envoy.filters.network.http_connection_manager",
										SubFilter: &istiov1alpha3.EnvoyFilter_ListenerMatch_SubFilterMatch{
											Name: "envoy.filters.http.router",
										},
									},
								},
							},
						},
					},
					Patch: &istiov1alpha3.EnvoyFilter_Patch{
						Operation: istiov1alpha3.EnvoyFilter_Patch_INSERT_FIRST,
						Value:     extProcConfig,
					},
				},
			},
		},
	}, nil
}

func (r *MCPGatewayExtensionReconciler) reconcileEnvoyFilter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway) error {
	envoyFilter, err := r.buildEnvoyFilter(mcpExt, targetGateway)
	if err != nil {
		return fmt.Errorf("failed to build envoy filter: %w", err)
	}

	existingEnvoyFilter := &istionetv1alpha3.EnvoyFilter{}
	err = r.Get(ctx, client.ObjectKeyFromObject(envoyFilter), existingEnvoyFilter)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.log.Info("creating envoy filter", "namespace", envoyFilter.Namespace, "name", envoyFilter.Name)
			if err := r.Create(ctx, envoyFilter); err != nil {
				return fmt.Errorf("failed to create envoy filter: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to get envoy filter: %w", err)
	}

	if !envoyFilterNeedsUpdate(envoyFilter, existingEnvoyFilter) {
		return nil
	}

	// preserve user labels while ensuring our managed labels are set
	mergedLabels := make(map[string]string)
	maps.Copy(mergedLabels, existingEnvoyFilter.Labels)
	maps.Copy(mergedLabels, envoyFilter.Labels)
	envoyFilter.Labels = mergedLabels
	envoyFilter.ResourceVersion = existingEnvoyFilter.ResourceVersion
	envoyFilter.UID = existingEnvoyFilter.UID

	r.log.Info("updating envoy filter", "namespace", envoyFilter.Namespace, "name", envoyFilter.Name)
	return r.Update(ctx, envoyFilter)
}

// envoyFilterNeedsUpdate checks if the EnvoyFilter needs to be updated by comparing specs and managed labels
func envoyFilterNeedsUpdate(desired, existing *istionetv1alpha3.EnvoyFilter) bool {
	// check if spec changed
	if !proto.Equal(&existing.Spec, &desired.Spec) {
		return true
	}
	// check if managed labels changed
	if !managedLabelsMatch(existing.Labels, desired.Labels) {
		return true
	}
	return false
}

// managedLabelsMatch checks if the labels we manage match between existing and desired
func managedLabelsMatch(existing, desired map[string]string) bool {
	for _, key := range envoyFilterManagedLabelKeys {
		if existing[key] != desired[key] {
			return false
		}
	}
	return true
}

func (r *MCPGatewayExtensionReconciler) deleteEnvoyFilter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	envoyFilterList := &istionetv1alpha3.EnvoyFilterList{}
	if err := r.List(ctx, envoyFilterList, client.MatchingLabels{
		labelManagedBy:          labelManagedByValue,
		labelExtensionName:      mcpExt.Name,
		labelExtensionNamespace: mcpExt.Namespace,
	}); err != nil {
		return fmt.Errorf("failed to list envoy filters for cleanup: %w", err)
	}

	for _, ef := range envoyFilterList.Items {
		r.log.Info("deleting envoy filter", "namespace", ef.Namespace, "name", ef.Name)
		if err := r.Delete(ctx, ef); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete envoy filter %s/%s: %w", ef.Namespace, ef.Name, err)
		}
	}
	return nil
}

func (r *MCPGatewayExtensionReconciler) enqueueMCPGatewayExtForEnvoyFilter(_ context.Context, obj client.Object) []reconcile.Request {
	envoyFilter, ok := obj.(*istionetv1alpha3.EnvoyFilter)
	if !ok || envoyFilter.Labels == nil {
		return nil
	}

	if envoyFilter.Labels[labelManagedBy] != labelManagedByValue {
		return nil
	}

	extName := envoyFilter.Labels[labelExtensionName]
	extNamespace := envoyFilter.Labels[labelExtensionNamespace]
	if extName == "" || extNamespace == "" {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: extName, Namespace: extNamespace},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPGatewayExtensionReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	r.log = slog.New(logr.ToSlogHandler(mgr.GetLogger()))
	if err := setupIndexExtensionToGateway(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup manager %w", err)
	}

	if err := setupIndexExtensionToReferenceGrant(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup manager %w", err)
	}

	// enqueue mcpgateway extensions when the gateway changes
	// enqueue when reference grants change
	// enqueue when envoy filter changes (cross-namespace, so we use Watches instead of Owns)
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPGatewayExtension{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForGateway)).
		Watches(&gatewayv1beta1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForReferenceGrant)).
		Watches(&istionetv1alpha3.EnvoyFilter{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForEnvoyFilter)).
		Named("mcpgatewayextension").
		Complete(r)
}
