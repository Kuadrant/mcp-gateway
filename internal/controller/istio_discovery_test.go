package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

type stubAPIResourceDiscovery struct {
	resources *metav1.APIResourceList
	err       error
}

func (s stubAPIResourceDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return s.resources, s.err
}

type mutableAPIResourceDiscovery struct {
	resources *metav1.APIResourceList
	err       error
}

func (s *mutableAPIResourceDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return s.resources, s.err
}

func TestRefreshEnvoyFilterAvailability(t *testing.T) {
	discovery := &mutableAPIResourceDiscovery{
		err: apierrors.NewNotFound(schema.GroupResource{Group: "networking.istio.io", Resource: "envoyfilters"}, ""),
	}
	reconciler := &MCPGatewayExtensionReconciler{
		envoyFilterUnavailable: true,
		envoyFilterDiscovery:   discovery,
	}

	require.NoError(t, reconciler.refreshEnvoyFilterAvailability())
	require.True(t, reconciler.envoyFilterUnavailable)

	discovery.err = nil
	discovery.resources = &metav1.APIResourceList{
		APIResources: []metav1.APIResource{{Name: "envoyfilters", Kind: "EnvoyFilter"}},
	}
	require.NoError(t, reconciler.refreshEnvoyFilterAvailability())
	require.False(t, reconciler.envoyFilterUnavailable)
}

func TestReconcileUnavailableEnvoyFilter(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1.AddToScheme(scheme))
	extension := &mcpv1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{Name: "extension", Namespace: "default"},
	}
	k8sClient := fakeclient.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(extension).
		WithObjects(extension).
		Build()
	reconciler := &MCPGatewayExtensionReconciler{
		Client:                 k8sClient,
		envoyFilterUnavailable: true,
		log:                    slog.Default(),
	}

	result, err := reconciler.reconcileUnavailableEnvoyFilter(context.Background(), extension)

	require.NoError(t, err)
	require.Equal(t, envoyFilterAvailabilityRequeue, result.RequeueAfter)
	current := &mcpv1.MCPGatewayExtension{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(extension), current))
	condition := meta.FindStatusCondition(current.Status.Conditions, mcpv1.ConditionTypeReady)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, mcpv1.ConditionReasonIstioUnavailable, condition.Reason)
}

func TestEnvoyFilterAvailable(t *testing.T) {
	tests := []struct {
		name      string
		resources *metav1.APIResourceList
		err       error
		want      bool
		wantErr   error
	}{
		{
			name: "resource exists",
			resources: &metav1.APIResourceList{
				APIResources: []metav1.APIResource{{Name: "envoyfilters", Kind: "EnvoyFilter"}},
			},
			want: true,
		},
		{
			name: "resource is absent",
			resources: &metav1.APIResourceList{
				APIResources: []metav1.APIResource{{Name: "virtualservices", Kind: "VirtualService"}},
			},
			want: false,
		},
		{
			name: "api group is absent",
			err:  apierrors.NewNotFound(schema.GroupResource{Group: "networking.istio.io", Resource: "envoyfilters"}, ""),
			want: false,
		},
		{
			name:    "discovery fails",
			err:     errors.New("discovery unavailable"),
			wantErr: errors.New("failed to discover Istio EnvoyFilter resource: discovery unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := envoyFilterAvailable(stubAPIResourceDiscovery{resources: tt.resources, err: tt.err})
			if tt.wantErr != nil {
				require.EqualError(t, err, tt.wantErr.Error())
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}
func TestEnvoyFilterWatcherSwitchesFromDiscoveryToWatch(t *testing.T) {
	gvr := schema.GroupVersionResource{
		Group:    "networking.istio.io",
		Version:  "v1alpha3",
		Resource: "envoyfilters",
	}
	initial := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "networking.istio.io/v1alpha3",
		"kind":       "EnvoyFilter",
		"metadata": map[string]interface{}{
			"name":      "initial",
			"namespace": "gateway",
			"labels": map[string]interface{}{
				labelManagedBy:          labelManagedByValue,
				labelExtensionName:      "extension",
				labelExtensionNamespace: "default",
			},
		},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), initial)
	watchStream := watch.NewRaceFreeFake()
	dynamicClient.PrependWatchReactor("envoyfilters", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, watchStream, nil
	})
	discovery := &mutableAPIResourceDiscovery{
		resources: &metav1.APIResourceList{
			APIResources: []metav1.APIResource{{Name: "envoyfilters", Kind: "EnvoyFilter"}},
		},
	}
	events := make(chan event.TypedGenericEvent[client.Object], 4)
	watcher := &envoyFilterWatcher{
		discovery:     discovery,
		resource:      dynamicClient.Resource(gvr),
		events:        events,
		retryInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- watcher.Start(ctx)
	}()

	select {
	case event := <-events:
		require.Equal(t, "initial", event.Object.GetName())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial EnvoyFilter event")
	}

	updated := initial.DeepCopy()
	updated.SetResourceVersion("2")
	watchStream.Modify(updated)
	select {
	case event := <-events:
		require.Equal(t, "initial", event.Object.GetName())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for modified EnvoyFilter event")
	}

	watchStream.Delete(updated)
	select {
	case event := <-events:
		require.Equal(t, "initial", event.Object.GetName())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deleted EnvoyFilter event")
	}

	listActions := 0

	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "list" {
			listActions++
		}
	}
	require.Equal(t, 1, listActions)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}
