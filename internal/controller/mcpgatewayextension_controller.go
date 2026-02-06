package controller

import (
	"context"
	"fmt"
	"log/slog"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
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
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	mcpGatewayFinalizer = "mcp.kagenti.com/finalizer"
	// gatewayIndexKey is the index used to improve look up of mcpgatewayextensions related to a gateway
	gatewayIndexKey  = "spec.targetRef.gateway"
	refGrantIndexKey = "spec.from.ref"

	// broker-router deployment constants
	brokerRouterName    = "mcp-broker-router"
	labelAppName        = "app.kubernetes.io/name"
	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelManagedByValue = "mcp-gateway-controller"

	// DefaultBrokerRouterImage is the default image for the broker-router deployment
	DefaultBrokerRouterImage = "ghcr.io/kuadrant/mcp-gateway:latest"
)

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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MCPGatewayExtension object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *MCPGatewayExtensionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	mcpExt := &mcpv1alpha1.MCPGatewayExtension{}
	if err := r.Get(ctx, req.NamespacedName, mcpExt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.Info("reconciling mcpgatewayextension", "name", mcpExt.Name)

	// handle deletion
	if !mcpExt.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(mcpExt, mcpGatewayFinalizer) {
			logger.Info("deleting mcpgatewayextension", "name", mcpExt.Name, "namespace", mcpExt.Namespace)
			// TODO we write an empty config as deleting it currently would cause the gateway extension instance to crash. Once we have an operator in place deleting the mcpgatewayextension will remove the gateway extension instance also
			if err := r.ConfigWriterDeleter.WriteEmptyConfig(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(mcpExt, mcpGatewayFinalizer)
			if err := r.Update(ctx, mcpExt); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// add finalizer if not present
	if !controllerutil.ContainsFinalizer(mcpExt, mcpGatewayFinalizer) {
		controllerutil.AddFinalizer(mcpExt, mcpGatewayFinalizer)
		err := r.Update(ctx, mcpExt)
		return ctrl.Result{}, err
	}
	// main reconcile status logic
	// TODO ensure there is actually a mcpgateway instance in the mcpgatewayextension namespace

	// check which gateway this resource is targeting and if in same namespace
	inSameNS := mcpExt.Spec.TargetRef.Namespace == mcpExt.Namespace
	targetGateway, err := r.gatewayTarget(ctx, mcpExt.Spec.TargetRef)
	if err != nil {
		if errors.IsNotFound(err) {
			err := r.setReadyConditionAndUpdateStatus(ctx, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonInvalid, "invalid", mcpExt)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, err
	}

	// not in same namespace - check for a valid ReferenceGrant
	if !inSameNS {
		hasGrant, err := r.MCPExtFinderValidator.HasValidReferenceGrant(ctx, mcpExt)
		if err != nil {
			return ctrl.Result{}, err
		}

		if !hasGrant {
			logger.Info("no valid ReferenceGrant found for cross-namespace reference. Removing config and setting status",
				"mcpgatewayextension", mcpExt.Name,
				"mcpgatewayextension-namespace", mcpExt.Namespace,
				"gateway-namespace", mcpExt.Spec.TargetRef.Namespace)
			if err := r.ConfigWriterDeleter.WriteEmptyConfig(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
				return ctrl.Result{}, err
			}
			err := r.setReadyConditionAndUpdateStatus(ctx, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonRefGrantRequired,
				fmt.Sprintf("ReferenceGrant required in namespace %s to allow cross-namespace reference", mcpExt.Spec.TargetRef.Namespace), mcpExt)
			return ctrl.Result{}, err
		}
	}

	// check if this gateway is already targeted by another MCPGatewayExtension
	existingExts, err := r.listMCPGatewayExtsForGateway(ctx, targetGateway)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(existingExts.Items) > 1 {
		owner := mcpExt
		for i := range existingExts.Items {
			if owner.CreationTimestamp.After(existingExts.Items[i].CreationTimestamp.Time) {
				owner = &existingExts.Items[i]
			}
		}
		if owner.GetUID() != mcpExt.GetUID() {
			if err := r.setReadyConditionAndUpdateStatus(ctx, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonInvalid, "conflict. Gateway target already configured as MCP Gateway", mcpExt); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}
	// ensure config secret exists before deploying broker-router
	// this prevents the broker-router from failing to start if it expects the config to be mounted
	if err := r.ConfigWriterDeleter.EnsureConfigExists(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	// valid configuration - reconcile broker-router deployment and service
	deploymentReady, err := r.reconcileBrokerRouter(ctx, mcpExt)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !deploymentReady {
		err = r.setReadyConditionAndUpdateStatus(ctx, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonDeploymentNotReady, "broker-router deployment is not ready", mcpExt)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	err = r.setReadyConditionAndUpdateStatus(ctx, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "successfully verified and configured", mcpExt)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *MCPGatewayExtensionReconciler) setReadyConditionAndUpdateStatus(ctx context.Context, conditionStatus metav1.ConditionStatus, conditionReason, message string, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	// copy existing condition before modifying (FindStatusCondition returns a pointer that gets modified in place)
	var existingCondition *metav1.Condition
	if existing := meta.FindStatusCondition(mcpExt.Status.Conditions, mcpv1alpha1.ConditionTypeReady); existing != nil {
		copied := *existing
		existingCondition = &copied
	}
	mcpExt.SetReadyCondition(conditionStatus, conditionReason, message)
	updatedCondition := meta.FindStatusCondition(mcpExt.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
	if !equality.Semantic.DeepEqual(existingCondition, updatedCondition) {
		if err := r.Status().Update(ctx, mcpExt); err != nil {
			return err
		}
	}
	return nil
}

func (r *MCPGatewayExtensionReconciler) gatewayTarget(ctx context.Context, target mcpv1alpha1.MCPGatewayExtensionTargetReference) (*gatewayv1.Gateway, error) {
	g := &gatewayv1.Gateway{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name}, g); err != nil {
		// return NotFound errors unwrapped so caller can check with errors.IsNotFound
		if errors.IsNotFound(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to find gateway targeted by the mcpgatewayextension %w", err)
	}
	return g, nil
}

func mcpExtToRefGrantIndexValue(mext mcpv1alpha1.MCPGatewayExtension) string {
	return fmt.Sprintf("%s%s%s", "mcp.kagenti.com", "MCPGatewayExtension", mext.Namespace)

}

func mcpExtToGatewayIndexValue(mext mcpv1alpha1.MCPGatewayExtension) string {
	return fmt.Sprintf("%s%s", mext.Spec.TargetRef.Name, mext.Spec.TargetRef.Namespace)
}

func gatewayToMCPExtIndexValue(g gatewayv1.Gateway) string {
	return fmt.Sprintf("%s%s", g.Name, g.Namespace)
}

func refGrantToMCPExtIndexValue(r gatewayv1beta1.ReferenceGrant) string {
	// the spec.from is a required field so will be set
	f := r.Spec.From[0]
	return fmt.Sprintf("%s%s%s", f.Group, f.Kind, f.Namespace)
}

// setupIndexExtensionToGateway creates an index for the gateway targeted by an MCPGatewayExtension
// This improves discovery when the controller receives a gateway change event
func setupIndexExtensionToGateway(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(
		ctx,
		&mcpv1alpha1.MCPGatewayExtension{},
		gatewayIndexKey,
		func(obj client.Object) []string {
			mcpExt, ok := obj.(*mcpv1alpha1.MCPGatewayExtension)
			if ok {
				return []string{mcpExtToGatewayIndexValue(*mcpExt)}
			}
			return []string{}
		},
	); err != nil {
		return fmt.Errorf("failed to setup index mcpgatewayextension to gateway %w", err)
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

func (r *MCPGatewayExtensionReconciler) findMCPGatewayExtForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
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

// setupIndexExtensionToGateway creates an index for the ReferenceGrant allowing an MCPGatewayExtension to target a gateway
// This improves discovery when the controller receives a ReferenceGrant change event
func setupIndexExtensionToReferenceGrant(ctx context.Context, indexer client.FieldIndexer) error {
	logger := logf.FromContext(ctx)
	if err := indexer.IndexField(
		ctx,
		&mcpv1alpha1.MCPGatewayExtension{},
		refGrantIndexKey,
		func(obj client.Object) []string {
			mcpGatewayExt, ok := obj.(*mcpv1alpha1.MCPGatewayExtension)
			if ok {
				value := mcpExtToRefGrantIndexValue(*mcpGatewayExt)
				logger.V(1).Info("setupIndexExtensionToReferenceGrant", "ext", mcpGatewayExt.Name, "index", value)
				return []string{value}
			}

			return nil
		},
	); err != nil {
		return fmt.Errorf("failed to setup index mcpgatewayextension to reference grants %w", err)
	}
	return nil
}

func (r *MCPGatewayExtensionReconciler) findMCPGatewayExtForReferenceGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	ref, ok := obj.(*gatewayv1beta1.ReferenceGrant)
	requests := []reconcile.Request{}
	if !ok {
		return requests
	}
	r.log.Debug("findMCPGatewayExtForReferenceGrant", "ref grant", ref.Spec.From)
	if len(ref.Spec.From) == 0 {
		return requests
	}
	r.log.Debug("findMCPGatewayExtForReferenceGrant", "ref grant", refGrantToMCPExtIndexValue(*ref))
	mcpGatewayExtList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{refGrantIndexKey: refGrantToMCPExtIndexValue(*ref)},
	); err != nil {
		r.log.Error("failed to findMCPGatewayExtForGateway", "error", err)
		return nil
	}
	r.log.Debug("found mcpgatewayextensions by refgrant", "total", len(mcpGatewayExtList.Items), "referencegrant", ref.Name)
	for _, ext := range mcpGatewayExtList.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ext)})
	}
	return requests
}

func (r *MCPGatewayExtensionReconciler) buildBrokerRouterDeployment(mcpExt *mcpv1alpha1.MCPGatewayExtension) *appsv1.Deployment {
	labels := map[string]string{
		labelAppName:   brokerRouterName,
		labelManagedBy: labelManagedByValue,
	}

	replicas := int32(1)

	var command = []string{"- ./mcp_gateway",
		"- --mcp-broker-public-address=0.0.0.0:8080"}
	if r.BrokerPollInterval != "" {
		command = append(command, "--mcp-check-interval="+r.BrokerPollInterval)
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
					Containers: []corev1.Container{
						{
							Name:    brokerRouterName,
							Image:   r.BrokerRouterImage,
							Command: command,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 8080,
								},
								{
									Name:          "grpc",
									ContainerPort: 50051,
								},
								{
									Name:          "config",
									ContainerPort: 8181,
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
	labels := map[string]string{
		labelAppName:   brokerRouterName,
		labelManagedBy: labelManagedByValue,
	}

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
		if errors.IsNotFound(err) {
			logger.Info("creating broker-router deployment", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, deployment); err != nil {
				return false, fmt.Errorf("failed to create deployment: %w", err)
			}
			return false, nil // deployment just created, not ready yet
		}
		return false, fmt.Errorf("failed to get deployment: %w", err)
	}

	if !equality.Semantic.DeepEqual(existingDeployment.Spec, deployment) {
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
		if errors.IsNotFound(err) {
			logger.Info("creating broker-router service", "namespace", mcpExt.Namespace)
			if err := r.Create(ctx, service); err != nil {
				return false, fmt.Errorf("failed to create service: %w", err)
			}
		} else {
			return false, fmt.Errorf("failed to get service: %w", err)
		}
	}

	// check deployment readiness
	deploymentReady := existingDeployment.Status.ReadyReplicas > 0 &&
		existingDeployment.Status.ReadyReplicas == existingDeployment.Status.Replicas

	return deploymentReady, nil
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPGatewayExtension{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.findMCPGatewayExtForGateway)).
		Watches(&gatewayv1beta1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.findMCPGatewayExtForReferenceGrant)).
		Named("mcpgatewayextension").
		Complete(r)
}
