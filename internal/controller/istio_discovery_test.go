package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"

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

func TestEnvoyFilterPollerEmitsDeletedFilter(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, istionetv1alpha3.AddToScheme(scheme))
	envoyFilter := &istionetv1alpha3.EnvoyFilter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "filter",
			Namespace: "gateway",
			Labels: map[string]string{
				labelManagedBy:          labelManagedByValue,
				labelExtensionName:      "extension",
				labelExtensionNamespace: "default",
			},
		},
	}
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(envoyFilter).Build()
	discovery := &mutableAPIResourceDiscovery{
		resources: &metav1.APIResourceList{
			APIResources: []metav1.APIResource{{Name: "envoyfilters", Kind: "EnvoyFilter"}},
		},
	}
	events := make(chan event.TypedGenericEvent[client.Object], 2)
	poller := &envoyFilterPoller{
		discovery: discovery,
		reader:    k8sClient,
		events:    events,
	}

	poller.poll(context.Background())
	<-events
	poller.poll(context.Background())
	select {
	case event := <-events:
		t.Fatalf("unchanged EnvoyFilter emitted event: %#v", event.Object)
	default:
	}
	require.NoError(t, k8sClient.Delete(context.Background(), envoyFilter))
	poller.poll(context.Background())

	deleted := (<-events).Object.(*istionetv1alpha3.EnvoyFilter)
	require.Equal(t, envoyFilter.Name, deleted.Name)
	require.Equal(t, envoyFilter.Namespace, deleted.Namespace)
	require.Equal(t, envoyFilter.Labels, deleted.Labels)
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
