package controller

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const envoyFilterGroupVersion = "networking.istio.io/v1alpha3"

const envoyFilterAvailabilityRequeue = time.Minute

var envoyFilterGVR = schema.GroupVersionResource{
	Group:    "networking.istio.io",
	Version:  "v1alpha3",
	Resource: "envoyfilters",
}

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

func snapshotEnvoyFilter(object *unstructured.Unstructured) envoyFilterSnapshot {
	return envoyFilterSnapshot{
		resourceVersion: object.GetResourceVersion(),
		labels:          object.GetLabels(),
	}
}

func deletedEnvoyFilter(key types.NamespacedName, snapshot envoyFilterSnapshot) *unstructured.Unstructured {
	labels := make(map[string]interface{}, len(snapshot.labels))
	for name, value := range snapshot.labels {
		labels[name] = value
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": envoyFilterGroupVersion,
		"kind":       "EnvoyFilter",
		"metadata": map[string]interface{}{
			"name":      key.Name,
			"namespace": key.Namespace,
			"labels":    labels,
		},
	}}
}

type envoyFilterWatcher struct {
	discovery     apiResourceDiscovery
	resource      dynamic.ResourceInterface
	events        chan<- event.TypedGenericEvent[client.Object]
	log           *slog.Logger
	retryInterval time.Duration
	known         map[types.NamespacedName]envoyFilterSnapshot
}

func (w *envoyFilterWatcher) Start(ctx context.Context) error {
	retryInterval := w.retryInterval
	if retryInterval <= 0 {
		retryInterval = envoyFilterAvailabilityRequeue
	}

	for {
		if err := w.run(ctx); err != nil && ctx.Err() == nil {
			if w.log != nil {
				w.log.Warn("failed to watch Istio EnvoyFilters", "error", err)
			}
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

func (w *envoyFilterWatcher) run(ctx context.Context) error {
	available, err := envoyFilterAvailable(w.discovery)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}

	list, err := w.resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list Istio EnvoyFilters: %w", err)
	}
	current := make(map[types.NamespacedName]envoyFilterSnapshot, len(list.Items))
	for i := range list.Items {
		object := &list.Items[i]
		key := client.ObjectKeyFromObject(object)
		snapshot := snapshotEnvoyFilter(object)
		current[key] = snapshot
		previous, ok := w.known[key]
		if !ok || previous.resourceVersion != snapshot.resourceVersion || !maps.Equal(previous.labels, snapshot.labels) {
			if !w.emit(ctx, object) {
				return nil
			}
		}
	}
	for key, snapshot := range w.known {
		if _, ok := current[key]; ok {
			continue
		}
		if !w.emit(ctx, deletedEnvoyFilter(key, snapshot)) {
			return nil
		}
	}
	w.known = current

	stream, err := w.resource.Watch(ctx, metav1.ListOptions{
		ResourceVersion: list.GetResourceVersion(),
	})
	if err != nil {
		return fmt.Errorf("failed to watch Istio EnvoyFilters: %w", err)
	}
	defer stream.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case watchEvent, ok := <-stream.ResultChan():
			if !ok {
				return fmt.Errorf("istio EnvoyFilter watch closed")
			}
			switch watchEvent.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				object, ok := watchEvent.Object.(*unstructured.Unstructured)
				if !ok {
					return fmt.Errorf("unexpected Istio EnvoyFilter watch object type %T", watchEvent.Object)
				}
				if !w.emit(ctx, object) {
					return nil
				}
				if w.known == nil {
					w.known = make(map[types.NamespacedName]envoyFilterSnapshot)
				}
				key := client.ObjectKeyFromObject(object)
				if watchEvent.Type == watch.Deleted {
					delete(w.known, key)
				} else {
					w.known[key] = snapshotEnvoyFilter(object)
				}
			case watch.Error:
				if err := apierrors.FromObject(watchEvent.Object); err != nil {
					return err
				}
				return fmt.Errorf("istio EnvoyFilter watch returned an error event")
			case watch.Bookmark:
				continue
			}
		}
	}
}

func (w *envoyFilterWatcher) emit(ctx context.Context, object client.Object) bool {
	select {
	case w.events <- event.TypedGenericEvent[client.Object]{Object: object}:
		return true
	case <-ctx.Done():
		return false
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
