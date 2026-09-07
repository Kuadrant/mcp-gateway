package controller

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const envoyFilterGroupVersion = "networking.istio.io/v1alpha3"

const envoyFilterCRDName = "envoyfilters.networking.istio.io"

type apiResourceDiscovery interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

func (r *MCPGatewayExtensionReconciler) refreshEnvoyFilterAvailability() error {
	if r.envoyFilterDiscovery == nil {
		return nil
	}
	available, err := envoyFilterAvailable(r.envoyFilterDiscovery)
	if err != nil {
		return err
	}
	r.envoyFilterUnavailable.Store(!available)
	return nil
}

func (r *MCPGatewayExtensionReconciler) reconcileUnavailableEnvoyFilter(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (ctrl.Result, error) {
	if !r.envoyFilterUnavailable.Load() {
		return ctrl.Result{}, nil
	}

	if err := r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, mcpv1.ConditionReasonIstioUnavailable,
		"waiting for the Istio EnvoyFilter CRD to be installed"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *MCPGatewayExtensionReconciler) handleEnvoyFilterCRDAvailable() {
	r.envoyFilterUnavailable.Store(false)
	if !r.restartOnEnvoyFilterAvailable {
		return
	}

	r.restartOnEnvoyFilterAvailable = false
	if r.log != nil {
		r.log.Info("Istio EnvoyFilter CRD available; restarting controller to register typed watch")
	}
	if r.Shutdown != nil {
		r.Shutdown()
	}
}

func (r *MCPGatewayExtensionReconciler) handleEnvoyFilterCRDDeleted() {
	r.envoyFilterUnavailable.Store(true)
}

func (r *MCPGatewayExtensionReconciler) shouldRestartForEnvoyFilterCRD() bool {
	return r.restartOnEnvoyFilterAvailable && r.envoyFilterUnavailable.Load()
}

func envoyFilterCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	if crd == nil || crd.Name != envoyFilterCRDName {
		return false
	}
	for i := range crd.Status.Conditions {
		condition := &crd.Status.Conditions[i]
		if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *MCPGatewayExtensionReconciler) enqueueRequestsForEnvoyFilterCRD(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	extensions := &mcpv1.MCPGatewayExtensionList{}
	if err := r.List(ctx, extensions); err != nil {
		if r.log != nil {
			r.log.Error("failed to list mcpgatewayextensions after EnvoyFilter CRD change", "error", err)
		}
		return
	}
	for i := range extensions.Items {
		queue.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&extensions.Items[i])})
	}
}

func envoyFilterAvailable(discovery apiResourceDiscovery) (bool, error) {
	resources, err := discovery.ServerResourcesForGroupVersion(envoyFilterGroupVersion)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to discover Istio EnvoyFilter resource: %w", err)
	}

	for i := range resources.APIResources {
		if resources.APIResources[i].Name == "envoyfilters" {
			return true, nil
		}
	}
	return false, nil
}
