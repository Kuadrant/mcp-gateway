package controller

import (
	"context"
	"testing"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockVirtualServerConfigReaderWriter records WriteVirtualServerConfig calls.
type mockVirtualServerConfigReaderWriter struct {
	writtenConfigs [][]config.VirtualServerConfig
}

func (m *mockVirtualServerConfigReaderWriter) WriteVirtualServerConfig(_ context.Context, virtualServers []config.VirtualServerConfig, _ types.NamespacedName) error {
	m.writtenConfigs = append(m.writtenConfigs, virtualServers)
	return nil
}

func newVirtualServerReconciler(mock *mockVirtualServerConfigReaderWriter, objs ...client.Object) *MCPVirtualServerReconciler {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	return &MCPVirtualServerReconciler{
		Client:             fakeClient,
		Scheme:             scheme,
		ConfigReaderWriter: mock,
	}
}

// TestReconcile_VirtualServerDeletion_WritesConfig verifies that deleting an
// MCPVirtualServer triggers a config write that excludes the deleted resource.
func TestReconcile_VirtualServerDeletion_WritesConfig(t *testing.T) {
	now := metav1.NewTime(time.Now())

	// the virtual server being deleted — has DeletionTimestamp and the finalizer
	deleting := &mcpv1alpha1.MCPVirtualServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "vs-deleting",
			Namespace:         "ns",
			DeletionTimestamp: &now,
			Finalizers:        []string{mcpGatewayFinalizer},
		},
		Spec: mcpv1alpha1.MCPVirtualServerSpec{
			Tools: []string{"tool_a"},
		},
	}

	// a second virtual server that should remain
	remaining := &mcpv1alpha1.MCPVirtualServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vs-remaining",
			Namespace: "ns",
		},
		Spec: mcpv1alpha1.MCPVirtualServerSpec{
			Tools: []string{"tool_b"},
		},
	}

	mock := &mockVirtualServerConfigReaderWriter{}
	r := newVirtualServerReconciler(mock, deleting, remaining)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "vs-deleting", Namespace: "ns"},
	})
	require.NoError(t, err)

	// WriteVirtualServerConfig must have been called exactly once during deletion
	require.Len(t, mock.writtenConfigs, 1, "expected WriteVirtualServerConfig to be called once during deletion")

	written := mock.writtenConfigs[0]

	// the written config must not contain the deleted virtual server
	for _, vs := range written {
		require.NotEqual(t, "ns/vs-deleting", vs.Name, "deleted virtual server must not appear in written config")
	}

	// the written config must still contain the surviving virtual server
	found := false
	for _, vs := range written {
		if vs.Name == "ns/vs-remaining" {
			found = true
			break
		}
	}
	require.True(t, found, "surviving virtual server must appear in written config")
}

// TestReconcile_VirtualServerDeletion_EmptyConfigWhenLast verifies that when
// the last MCPVirtualServer is deleted, the written config is empty (not omitted).
func TestReconcile_VirtualServerDeletion_EmptyConfigWhenLast(t *testing.T) {
	now := metav1.NewTime(time.Now())

	last := &mcpv1alpha1.MCPVirtualServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "vs-last",
			Namespace:         "ns",
			DeletionTimestamp: &now,
			Finalizers:        []string{mcpGatewayFinalizer},
		},
		Spec: mcpv1alpha1.MCPVirtualServerSpec{
			Tools: []string{"tool_a"},
		},
	}

	mock := &mockVirtualServerConfigReaderWriter{}
	r := newVirtualServerReconciler(mock, last)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "vs-last", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.Len(t, mock.writtenConfigs, 1, "expected WriteVirtualServerConfig to be called once during deletion")
	require.Empty(t, mock.writtenConfigs[0], "config must be empty when the last virtual server is deleted")
}
