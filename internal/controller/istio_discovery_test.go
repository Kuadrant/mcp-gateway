package controller

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

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
		envoyFilterDiscovery: discovery,
	}
	reconciler.envoyFilterUnavailable.Store(true)

	require.NoError(t, reconciler.refreshEnvoyFilterAvailability())
	require.True(t, reconciler.envoyFilterUnavailable.Load())

	discovery.err = nil
	discovery.resources = &metav1.APIResourceList{
		APIResources: []metav1.APIResource{{Name: "envoyfilters", Kind: "EnvoyFilter"}},
	}
	require.NoError(t, reconciler.refreshEnvoyFilterAvailability())
	require.False(t, reconciler.envoyFilterUnavailable.Load())
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
		Client: k8sClient,
		log:    slog.Default(),
	}
	reconciler.envoyFilterUnavailable.Store(true)

	result, err := reconciler.reconcileUnavailableEnvoyFilter(context.Background(), extension)

	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	current := &mcpv1.MCPGatewayExtension{}
	require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(extension), current))
	condition := meta.FindStatusCondition(current.Status.Conditions, mcpv1.ConditionTypeReady)
	require.Equal(t, metav1.ConditionFalse, condition.Status)
	require.Equal(t, mcpv1.ConditionReasonIstioUnavailable, condition.Reason)
}

func TestReconcileActiveStopsBeforeValidationWhenEnvoyFilterUnavailable(t *testing.T) {
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
		Client: k8sClient,
		log:    slog.Default(),
	}
	reconciler.envoyFilterUnavailable.Store(true)

	_, err := reconciler.reconcileActive(context.Background(), extension)

	require.NoError(t, err)
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
func TestHandleEnvoyFilterCRDAvailableRestartsController(t *testing.T) {
	restarted := false
	reconciler := &MCPGatewayExtensionReconciler{
		restartOnEnvoyFilterAvailable: true,
		Shutdown: func() {
			restarted = true
		},
	}
	reconciler.envoyFilterUnavailable.Store(true)

	reconciler.handleEnvoyFilterCRDAvailable()

	require.True(t, restarted)
	require.False(t, reconciler.envoyFilterUnavailable.Load())
}

func TestHandleEnvoyFilterCRDAvailableDoesNotRestartAfterStartup(t *testing.T) {
	restarted := false
	reconciler := &MCPGatewayExtensionReconciler{
		Shutdown: func() {
			restarted = true
		},
	}
	reconciler.envoyFilterUnavailable.Store(true)

	reconciler.handleEnvoyFilterCRDAvailable()

	require.False(t, restarted)
	require.False(t, reconciler.envoyFilterUnavailable.Load())
}

func TestHandleEnvoyFilterCRDDeletedMarksUnavailable(t *testing.T) {
	reconciler := &MCPGatewayExtensionReconciler{}

	reconciler.handleEnvoyFilterCRDDeleted()

	require.True(t, reconciler.envoyFilterUnavailable.Load())
}

func TestEnvoyFilterCRDEstablished(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: envoyFilterCRDName},
	}
	require.False(t, envoyFilterCRDEstablished(crd))

	crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{
		Type:   apiextensionsv1.Established,
		Status: apiextensionsv1.ConditionTrue,
	}}
	require.True(t, envoyFilterCRDEstablished(crd))
}
