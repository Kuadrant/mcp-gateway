package controller

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const envoyFilterGroupVersion = "networking.istio.io/v1alpha3"

const envoyFilterAvailabilityRequeue = time.Minute

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
	r.envoyFilterUnavailable = !available
	return nil
}

func (r *MCPGatewayExtensionReconciler) reconcileUnavailableEnvoyFilter(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (ctrl.Result, error) {
	if err := r.refreshEnvoyFilterAvailability(); err != nil {
		return ctrl.Result{}, err
	}
	if !r.envoyFilterUnavailable {
		return ctrl.Result{}, nil
	}

	if err := r.updateStatus(ctx, mcpExt, metav1.ConditionFalse, mcpv1.ConditionReasonIstioUnavailable,
		"waiting for the Istio EnvoyFilter CRD to be installed"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: envoyFilterAvailabilityRequeue}, nil
}

type envoyFilterSnapshot struct {
	resourceVersion string
	labels          map[string]string
}

type envoyFilterPoller struct {
	discovery apiResourceDiscovery
	reader    client.Reader
	events    chan<- event.TypedGenericEvent[client.Object]
	log       *slog.Logger
	known     map[types.NamespacedName]envoyFilterSnapshot
}

func (p *envoyFilterPoller) Start(ctx context.Context) error {
	p.poll(ctx)
	ticker := time.NewTicker(envoyFilterAvailabilityRequeue)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *envoyFilterPoller) poll(ctx context.Context) {
	available, err := envoyFilterAvailable(p.discovery)
	if err != nil {
		if p.log != nil {
			p.log.Warn("failed to discover Istio EnvoyFilter resource", "error", err)
		}
		return
	}
	if !available {
		return
	}

	list := &istionetv1alpha3.EnvoyFilterList{}
	if err := p.reader.List(ctx, list); err != nil {
		if p.log != nil {
			p.log.Warn("failed to list Istio EnvoyFilters", "error", err)
		}
		return
	}

	current := make(map[types.NamespacedName]envoyFilterSnapshot, len(list.Items))
	for i := range list.Items {
		filter := list.Items[i]
		key := client.ObjectKeyFromObject(filter)
		snapshot := envoyFilterSnapshot{
			resourceVersion: filter.ResourceVersion,
			labels:          cloneLabels(filter.Labels),
		}
		current[key] = snapshot
		previous, ok := p.known[key]
		if !ok || previous.resourceVersion != snapshot.resourceVersion || !maps.Equal(previous.labels, snapshot.labels) {
			if !p.emit(ctx, filter) {
				return
			}
		}
	}
	for key, snapshot := range p.known {
		if _, ok := current[key]; ok {
			continue
		}
		if !p.emit(ctx, &istionetv1alpha3.EnvoyFilter{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: snapshot.labels},
		}) {
			return
		}
	}
	p.known = current
}

func (p *envoyFilterPoller) emit(ctx context.Context, object client.Object) bool {
	select {
	case p.events <- event.TypedGenericEvent[client.Object]{Object: object}:
		return true
	case <-ctx.Done():
		return false
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	clone := make(map[string]string, len(labels))
	for key, value := range labels {
		clone[key] = value
	}
	return clone
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
