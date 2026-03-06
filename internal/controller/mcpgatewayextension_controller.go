package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/go-logr/logr"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	istiov1alpha3 "istio.io/api/networking/v1alpha3"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

const (
	mcpGatewayFinalizer = "mcp.kuadrant.io/finalizer"
	// gatewayIndexKey is the index used to improve look up of mcpgatewayextensions related to a gateway
	gatewayIndexKey  = "spec.targetRef.gateway"
	refGrantIndexKey = "spec.from.ref"

	// common labels
	labelAppName        = "app.kubernetes.io/name"
	labelManagedBy      = "app.kubernetes.io/managed-by"
	labelManagedByValue = "mcp-gateway-controller"

	// envoy filter labels
	labelExtensionName      = "mcp.kuadrant.io/extension-name"
	labelExtensionNamespace = "mcp.kuadrant.io/extension-namespace"
	// used to ensure a specific control plane reconciles this resource based on the gateway value
	labelIstioRev = "istio.io/rev"
)

func envoyFilterLabels(mcpExt *mcpv1alpha1.MCPGatewayExtension, gateway *gatewayv1.Gateway) map[string]string {
	// inherit istio.io/rev from gateway, default to "default" if not set
	istioRev := "default"
	if gateway != nil && gateway.Labels != nil {
		if rev, ok := gateway.Labels[labelIstioRev]; ok && rev != "" {
			istioRev = rev
		}
	}
	return map[string]string{
		labelAppName:            brokerRouterName,
		labelManagedBy:          labelManagedByValue,
		labelExtensionName:      mcpExt.Name,
		labelExtensionNamespace: mcpExt.Namespace,
		labelIstioRev:           istioRev,
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

// Error is the validation error message
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
}

// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpgatewayextensions/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=envoyfilters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

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

	// clean up gateway listener status
	if err := r.removeGatewayListenerStatus(ctx, mcpExt); err != nil {
		r.log.Error("failed to remove gateway listener status", "error", err)
		// don't fail deletion for status cleanup errors
	}

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
	// check for namespace conflict first - only one MCPGatewayExtension per namespace
	if err := r.checkNamespaceConflict(ctx, mcpExt); err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	targetGateway, listenerConfig, err := r.validateGatewayTarget(ctx, mcpExt)
	if err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}
	// listener port conflict check must always be done after the validation
	if err := r.checkListenerConflict(ctx, mcpExt, targetGateway, listenerConfig); err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	if err := r.ConfigWriterDeleter.EnsureConfigExists(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileTrustedHeaders(ctx, mcpExt); err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	deploymentReady, err := r.reconcileBrokerRouter(ctx, mcpExt, listenerConfig)
	if err != nil {
		var valErr *validationError
		if errors.As(err, &valErr) {
			return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, valErr.reason, valErr.message)
		}
		return ctrl.Result{}, err
	}

	if !deploymentReady {
		if err := r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonDeploymentNotReady, "broker-router deployment is not ready"); err != nil {
			return ctrl.Result{}, err
		}
		// requeue to check deployment status again since Owns watch doesn't trigger on status-only changes
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := r.reconcileEnvoyFilter(ctx, mcpExt, targetGateway, listenerConfig); err != nil {
		return ctrl.Result{}, err
	}

	// update Gateway listener status to indicate MCP Gateway is configured
	if err := r.updateGatewayListenerStatus(ctx, mcpExt, targetGateway, listenerConfig); err != nil {
		r.log.Error("failed to update gateway listener status, will retry", "error", err)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return ctrl.Result{}, r.updateStatus(ctx, mcpExt, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "successfully verified and configured")
}

func (r *MCPGatewayExtensionReconciler) validateGatewayTarget(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) (*gatewayv1.Gateway, *mcpv1alpha1.ListenerConfig, error) {
	targetGateway, err := r.gatewayTarget(ctx, mcpExt.Spec.TargetRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, newValidationError(mcpv1alpha1.ConditionReasonInvalid,
				fmt.Sprintf("invalid: gateway target %s/%s not found", mcpExt.Spec.TargetRef.Namespace, mcpExt.Spec.TargetRef.Name))
		}
		return nil, nil, err
	}

	// validate sectionName references an existing listener
	listenerConfig, err := findListenerConfigByName(targetGateway, mcpExt.Spec.TargetRef.SectionName)
	if err != nil {
		return nil, nil, err
	}

	// a resolvable hostname is required: either the listener must have one or spec.publicHost must be set
	if listenerConfig.Hostname == "" && mcpExt.Spec.PublicHost == "" {
		return nil, nil, newValidationError(mcpv1alpha1.ConditionReasonInvalid,
			fmt.Sprintf("listener %q on gateway %s/%s has no hostname and spec.publicHost is not set",
				mcpExt.Spec.TargetRef.SectionName, targetGateway.Namespace, targetGateway.Name))
	}

	// fetch the namespace directly to avoid setting up an informer via the cached client
	ns := &corev1.Namespace{}
	if err := r.DirectAPIReader.Get(ctx, types.NamespacedName{Name: mcpExt.Namespace}, ns); err != nil {
		return nil, nil, fmt.Errorf("failed to get namespace %s: %w", mcpExt.Namespace, err)
	}

	// validate that the MCPGatewayExtension namespace is allowed by the listener
	for _, listener := range targetGateway.Spec.Listeners {
		if string(listener.Name) == mcpExt.Spec.TargetRef.SectionName {
			if !listenerAllowsNamespace(&listener, mcpExt.Namespace, targetGateway.Namespace, ns.Labels) {
				return nil, nil, newValidationError(mcpv1alpha1.ConditionReasonInvalid,
					fmt.Sprintf("listener %q on gateway %s/%s does not allow routes from namespace %q",
						mcpExt.Spec.TargetRef.SectionName, targetGateway.Namespace, targetGateway.Name, mcpExt.Namespace))
			}
			break
		}
	}

	// cross-namespace reference requires ReferenceGrant
	if mcpExt.Spec.TargetRef.Namespace != mcpExt.Namespace {
		hasGrant, err := r.MCPExtFinderValidator.HasValidReferenceGrant(ctx, mcpExt)
		if err != nil {
			return nil, nil, err
		}

		if !hasGrant {
			r.log.Info("no valid ReferenceGrant for cross-namespace reference",
				"extension", mcpExt.Name, "extension-namespace", mcpExt.Namespace,
				"gateway-namespace", mcpExt.Spec.TargetRef.Namespace)
			if err := r.ConfigWriterDeleter.WriteEmptyConfig(ctx, config.NamespaceName(mcpExt.Namespace)); err != nil {
				return nil, nil, err
			}
			return nil, nil, newValidationError(mcpv1alpha1.ConditionReasonRefGrantRequired,
				fmt.Sprintf("invalid: ReferenceGrant required in %s to allow cross-namespace reference from %s", mcpExt.Spec.TargetRef.Namespace, mcpExt.Namespace))
		}
	}

	return targetGateway, listenerConfig, nil
}

// findListenerConfigByName finds listener configuration by name.
func findListenerConfigByName(gateway *gatewayv1.Gateway, sectionName string) (*mcpv1alpha1.ListenerConfig, error) {
	for _, listener := range gateway.Spec.Listeners {
		if string(listener.Name) == sectionName {
			hostname := ""
			if listener.Hostname != nil {
				hostname = string(*listener.Hostname)
			}
			port := uint32(listener.Port) // #nosec G115
			return &mcpv1alpha1.ListenerConfig{
				Port:     port,
				Hostname: hostname,
				Name:     sectionName,
			}, nil
		}
	}
	return nil, newValidationError(mcpv1alpha1.ConditionReasonInvalid,
		fmt.Sprintf("listener %q not found on gateway %s/%s", sectionName, gateway.Namespace, gateway.Name))
}

// listenerAllowsNamespace checks if the listener's allowedRoutes configuration permits
// routes from the given namespace. This follows Gateway API semantics:
// - "All": allows routes from all namespaces
// - "Same": allows routes only from the Gateway's namespace (default if not specified)
// - "Selector": allows routes from namespaces matching the label selector
// nsLabels are the labels on the namespace being checked.
func listenerAllowsNamespace(listener *gatewayv1.Listener, namespace, gatewayNamespace string, nsLabels map[string]string) bool {
	// if allowedRoutes is not specified, default behavior is "Same" namespace
	if listener.AllowedRoutes == nil || listener.AllowedRoutes.Namespaces == nil {
		return namespace == gatewayNamespace
	}

	namespaces := listener.AllowedRoutes.Namespaces

	// if From is not specified, default is "Same"
	if namespaces.From == nil {
		return namespace == gatewayNamespace
	}

	switch *namespaces.From {
	case gatewayv1.NamespacesFromAll:
		return true
	case gatewayv1.NamespacesFromSame:
		return namespace == gatewayNamespace
	case gatewayv1.NamespacesFromSelector:
		if namespaces.Selector == nil {
			return false
		}
		selector, err := metav1.LabelSelectorAsSelector(namespaces.Selector)
		if err != nil {
			return false
		}
		return selector.Matches(labels.Set(nsLabels))
	case gatewayv1.NamespacesFromNone:
		// no routes allowed from any namespace
		return false
	}
	// unreachable for known values, but be conservative for future values
	return false
}

// checkNamespaceConflict checks if there are multiple MCPGatewayExtensions in the same namespace.
// Only one MCPGatewayExtension is allowed per namespace. The oldest (by creation timestamp) wins.
func (r *MCPGatewayExtensionReconciler) checkNamespaceConflict(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	extList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, extList, client.InNamespace(mcpExt.Namespace)); err != nil {
		return fmt.Errorf("failed to list extensions in namespace: %w", err)
	}

	// filter out extensions being deleted
	var activeExts []mcpv1alpha1.MCPGatewayExtension
	for _, ext := range extList.Items {
		if ext.DeletionTimestamp == nil {
			activeExts = append(activeExts, ext)
		}
	}

	if len(activeExts) <= 1 {
		return nil
	}

	oldest := findOldestExtension(activeExts)
	if oldest.GetUID() == mcpExt.GetUID() {
		return nil // this is the oldest one, it's valid
	}

	return newValidationError(mcpv1alpha1.ConditionReasonInvalid,
		fmt.Sprintf("conflict: namespace %s already has MCPGatewayExtension %s (only one per namespace allowed)",
			mcpExt.Namespace, oldest.Name))
}

// checkListenerConflict checks if there are multiple MCPGatewayExtensions targeting listeners
// that share the same port on the same Gateway. This is invalid because only one ext_proc
// can handle a given port.
func (r *MCPGatewayExtensionReconciler) checkListenerConflict(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway, listenerConfig *mcpv1alpha1.ListenerConfig) error {
	existingExts, err := r.listMCPGatewayExtsForGateway(ctx, targetGateway)
	if err != nil {
		return fmt.Errorf("failed to list extensions for gateway: %w", err)
	}

	if len(existingExts.Items) <= 1 {
		return nil
	}

	// check for conflicting extensions targeting the same port
	for _, ext := range existingExts.Items {
		if ext.GetUID() == mcpExt.GetUID() {
			continue
		}
		// find the listener config for this extension (skip namespace validation here,
		// as we only need the port for conflict detection)
		extListenerConfig, err := findListenerConfigByName(targetGateway, ext.Spec.TargetRef.SectionName)
		if err != nil {
			// if we can't find the listener, skip this extension (it will fail its own validation)
			continue
		}
		if extListenerConfig.Port == listenerConfig.Port {
			// conflict: same port on same gateway
			oldest := findOldestExtension([]mcpv1alpha1.MCPGatewayExtension{*mcpExt, ext})
			if oldest.GetUID() != mcpExt.GetUID() {
				return newValidationError(mcpv1alpha1.ConditionReasonInvalid,
					fmt.Sprintf("conflict: listener port %d on gateway %s/%s is already configured by MCPGatewayExtension %s/%s (listener %s)",
						listenerConfig.Port, targetGateway.Namespace, targetGateway.Name, ext.Namespace, ext.Name, ext.Spec.TargetRef.SectionName))
			}
		}
	}

	return nil
}

func findOldestExtension(exts []mcpv1alpha1.MCPGatewayExtension) *mcpv1alpha1.MCPGatewayExtension {
	oldest := &exts[0]
	for i := 1; i < len(exts); i++ {
		if exts[i].CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = &exts[i]
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

// GatewayListenerConditionType is the condition type for MCP Gateway Extension
const GatewayListenerConditionType gatewayv1.ListenerConditionType = "MCPGatewayExtension"

// updateGatewayListenerStatus updates the Gateway listener status with a condition
// indicating that an MCP Gateway Extension is configured for this listener
func (r *MCPGatewayExtensionReconciler) updateGatewayListenerStatus(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, gateway *gatewayv1.Gateway, listenerConfig *mcpv1alpha1.ListenerConfig) error {
	// get fresh copy of gateway to avoid conflicts
	freshGateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(gateway), freshGateway); err != nil {
		return fmt.Errorf("failed to get gateway for status update: %w", err)
	}

	// find or create status for the listener
	listenerName := gatewayv1.SectionName(listenerConfig.Name)
	var listenerStatus *gatewayv1.ListenerStatus
	for i := range freshGateway.Status.Listeners {
		if freshGateway.Status.Listeners[i].Name == listenerName {
			listenerStatus = &freshGateway.Status.Listeners[i]
			break
		}
	}

	// we don't own the gateway so we should not create listener status entries;
	// return an error to requeue and wait for the gateway controller to create it
	if listenerStatus == nil {
		return fmt.Errorf("listener status for %q not yet present on gateway %s/%s",
			listenerConfig.Name, freshGateway.Namespace, freshGateway.Name)
	}

	envoyFilterName, envoyFilterNamespace := envoyFilterNameAndNamespace(mcpExt)
	conditionMessage := fmt.Sprintf("listener in use by MCP Gateway: %s/%s EnvoyFilter: %s/%s",
		mcpExt.Namespace, mcpExt.Name, envoyFilterNamespace, envoyFilterName)

	newCondition := metav1.Condition{
		Type:               string(GatewayListenerConditionType),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: freshGateway.Generation,
		Reason:             "Programmed",
		Message:            conditionMessage,
		LastTransitionTime: metav1.Now(),
	}

	// check if condition already exists and is unchanged
	existingCondition := meta.FindStatusCondition(listenerStatus.Conditions, string(GatewayListenerConditionType))
	if existingCondition != nil {
		if existingCondition.Status == newCondition.Status &&
			existingCondition.Reason == newCondition.Reason &&
			existingCondition.Message == newCondition.Message {
			return nil // no update needed
		}
		// preserve LastTransitionTime if status didn't change
		if existingCondition.Status == newCondition.Status {
			newCondition.LastTransitionTime = existingCondition.LastTransitionTime
		}
	}

	meta.SetStatusCondition(&listenerStatus.Conditions, newCondition)
	r.log.Info("updating gateway listener status",
		"gateway", fmt.Sprintf("%s/%s", freshGateway.Namespace, freshGateway.Name),
		"listener", listenerConfig.Name,
		"extension", fmt.Sprintf("%s/%s", mcpExt.Namespace, mcpExt.Name))

	return r.Status().Update(ctx, freshGateway)
}

// removeGatewayListenerStatus removes the MCP Gateway Extension condition from
// the Gateway listener status during cleanup
func (r *MCPGatewayExtensionReconciler) removeGatewayListenerStatus(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	gatewayNamespace := mcpExt.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = mcpExt.Namespace
	}

	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: gatewayNamespace,
		Name:      mcpExt.Spec.TargetRef.Name,
	}, gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // gateway doesn't exist, nothing to clean up
		}
		return fmt.Errorf("failed to get gateway for status cleanup: %w", err)
	}

	// find the listener status
	listenerName := gatewayv1.SectionName(mcpExt.Spec.TargetRef.SectionName)
	var listenerStatusIdx = -1
	for i := range gateway.Status.Listeners {
		if gateway.Status.Listeners[i].Name == listenerName {
			listenerStatusIdx = i
			break
		}
	}

	if listenerStatusIdx == -1 {
		return nil // listener status doesn't exist, nothing to clean up
	}

	// remove the condition
	listenerStatus := &gateway.Status.Listeners[listenerStatusIdx]
	existingCondition := meta.FindStatusCondition(listenerStatus.Conditions, string(GatewayListenerConditionType))
	if existingCondition == nil {
		return nil // condition doesn't exist, nothing to clean up
	}

	meta.RemoveStatusCondition(&listenerStatus.Conditions, string(GatewayListenerConditionType))
	r.log.Info("removing gateway listener status",
		"gateway", fmt.Sprintf("%s/%s", gateway.Namespace, gateway.Name),
		"listener", mcpExt.Spec.TargetRef.SectionName,
		"extension", fmt.Sprintf("%s/%s", mcpExt.Namespace, mcpExt.Name))

	return r.Status().Update(ctx, gateway)
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
	return fmt.Sprintf("%s/MCPGatewayExtension/%s", mcpv1alpha1.GroupVersion.Group, mext.Namespace)
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

	r.log.Debug("processing reference grant change", "name", ref.Name, "namespace", ref.Namespace)

	mcpGatewayExtList := &mcpv1alpha1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{refGrantIndexKey: refGrantToMCPExtIndexValue(*ref)},
	); err != nil {
		r.log.Error("failed to list mcpgatewayextensions for reference grant", "error", err)
		return nil
	}

	r.log.Debug("found mcpgatewayextensions for reference grant", "count", len(mcpGatewayExtList.Items), "refgrant", ref.Name)

	requests := make([]reconcile.Request, 0, len(mcpGatewayExtList.Items))
	for _, ext := range mcpGatewayExtList.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&ext)})
	}
	return requests
}

func (r *MCPGatewayExtensionReconciler) buildEnvoyFilter(mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway, listenerConfig *mcpv1alpha1.ListenerConfig) (*istionetv1alpha3.EnvoyFilter, error) {
	// build the ext_proc filter config as a structpb.Struct
	extProcConfig, err := structpb.NewStruct(map[string]any{
		"name": "envoy.filters.http.ext_proc",
		"typed_config": map[string]any{
			"@type":               "type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor",
			"failure_mode_allow":  false,
			"allow_mode_override": true,
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
					"cluster_name": fmt.Sprintf("outbound|%d||%s.%s.svc.cluster.local", brokerGRPCPort, brokerRouterName, mcpExt.Namespace),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ext_proc config struct: %w", err)
	}

	envoyFilterName, _ := envoyFilterNameAndNamespace(mcpExt)

	return &istionetv1alpha3.EnvoyFilter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envoyFilterName,
			Namespace: targetGateway.Namespace,
			Labels:    envoyFilterLabels(mcpExt, targetGateway),
		},
		Spec: istiov1alpha3.EnvoyFilter{
			WorkloadSelector: &istiov1alpha3.WorkloadSelector{
				Labels: map[string]string{
					"gateway.networking.k8s.io/gateway-name": targetGateway.Name,
				},
			},
			ConfigPatches: []*istiov1alpha3.EnvoyFilter_EnvoyConfigObjectPatch{
				{
					ApplyTo: istiov1alpha3.EnvoyFilter_HTTP_FILTER,
					Match: &istiov1alpha3.EnvoyFilter_EnvoyConfigObjectMatch{
						Context: istiov1alpha3.EnvoyFilter_GATEWAY,
						ObjectTypes: &istiov1alpha3.EnvoyFilter_EnvoyConfigObjectMatch_Listener{
							Listener: &istiov1alpha3.EnvoyFilter_ListenerMatch{
								PortNumber: listenerConfig.Port,
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

func (r *MCPGatewayExtensionReconciler) reconcileEnvoyFilter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension, targetGateway *gatewayv1.Gateway, listenerConfig *mcpv1alpha1.ListenerConfig) error {
	envoyFilter, err := r.buildEnvoyFilter(mcpExt, targetGateway, listenerConfig)
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

	needsUpdate, reason := envoyFilterNeedsUpdate(envoyFilter, existingEnvoyFilter)
	if !needsUpdate {
		return nil
	}

	// preserve user labels while ensuring our managed labels are set
	mergedLabels := make(map[string]string)
	maps.Copy(mergedLabels, existingEnvoyFilter.Labels)
	maps.Copy(mergedLabels, envoyFilter.Labels)
	envoyFilter.Labels = mergedLabels
	envoyFilter.ResourceVersion = existingEnvoyFilter.ResourceVersion
	envoyFilter.UID = existingEnvoyFilter.UID

	r.log.Info("updating envoy filter", "namespace", envoyFilter.Namespace, "name", envoyFilter.Name, "reason", reason)
	return r.Update(ctx, envoyFilter)
}

// envoyFilterNeedsUpdate checks if the EnvoyFilter needs to be updated by comparing specs and managed labels
// returns (needsUpdate, reason) where reason describes what changed
func envoyFilterNeedsUpdate(desired, existing *istionetv1alpha3.EnvoyFilter) (bool, string) {
	// check if spec changed
	if !proto.Equal(&existing.Spec, &desired.Spec) {
		return true, "spec changed"
	}
	// check if managed labels changed
	if reason := managedLabelsDiff(existing.Labels, desired.Labels); reason != "" {
		return true, reason
	}
	return false, ""
}

// managedLabelsDiff returns a reason string if managed labels differ, empty string if they match
func managedLabelsDiff(existing, desired map[string]string) string {
	for _, key := range envoyFilterManagedLabelKeys {
		if existing[key] != desired[key] {
			return fmt.Sprintf("label %s changed: %q -> %q", key, existing[key], desired[key])
		}
	}
	return ""
}

func (r *MCPGatewayExtensionReconciler) deleteEnvoyFilter(ctx context.Context, mcpExt *mcpv1alpha1.MCPGatewayExtension) error {
	name, namespace := envoyFilterNameAndNamespace(mcpExt)
	envoyFilter := &istionetv1alpha3.EnvoyFilter{}
	if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, envoyFilter); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get envoy filter for cleanup: %w", err)
	}

	r.log.Info("deleting envoy filter", "namespace", namespace, "name", name)
	if err := r.Delete(ctx, envoyFilter); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete envoy filter %s/%s: %w", namespace, name, err)
	}
	return nil
}

func envoyFilterNameAndNamespace(mcpExt *mcpv1alpha1.MCPGatewayExtension) (name, namespace string) {
	name = fmt.Sprintf("mcp-ext-proc-%s-gateway", mcpExt.Namespace)
	namespace = mcpExt.Spec.TargetRef.Namespace
	if namespace == "" {
		namespace = mcpExt.Namespace
	}
	return name, namespace
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
		Owns(&corev1.ServiceAccount{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForGateway)).
		Watches(&gatewayv1beta1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForReferenceGrant)).
		Watches(&istionetv1alpha3.EnvoyFilter{}, handler.EnqueueRequestsFromMapFunc(r.enqueueMCPGatewayExtForEnvoyFilter)).
		Named("mcpgatewayextension").
		Complete(r)
}
