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

// fakeVSConfigWriter records WriteVirtualServerConfig calls for assertion.
type fakeVSConfigWriter struct {
	calls []vsWriteCall
}

type vsWriteCall struct {
	namespaceName types.NamespacedName
	configs       []config.VirtualServerConfig
}

func (f *fakeVSConfigWriter) WriteVirtualServerConfig(_ context.Context, virtualServers []config.VirtualServerConfig, nn types.NamespacedName) error {
	f.calls = append(f.calls, vsWriteCall{namespaceName: nn, configs: virtualServers})
	return nil
}

// fakeMCPExtLister returns a fixed set of MCPGatewayExtension namespaces.
type fakeMCPExtLister struct {
	namespaces []string
}

func (f *fakeMCPExtLister) ListMCPGatewayExtensionNamespaces(_ context.Context) ([]string, error) {
	return f.namespaces, nil
}

func newVirtualServerReconciler(writer *fakeVSConfigWriter, lister *fakeMCPExtLister, objs ...client.Object) *MCPVirtualServerReconciler {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	return &MCPVirtualServerReconciler{
		Client:                fakeClient,
		Scheme:                scheme,
		ConfigReaderWriter:    writer,
		MCPExtNamespaceLister: lister,
	}
}

func TestMCPVirtualServerReconciler_writesConfigToAllExtensionNamespaces(t *testing.T) {
	writer := &fakeVSConfigWriter{}
	lister := &fakeMCPExtLister{namespaces: []string{"team-a", "team-b"}}

	r := &MCPVirtualServerReconciler{
		ConfigReaderWriter:    writer,
		MCPExtNamespaceLister: lister,
	}

	if err := r.writeVirtualServerConfig(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(writer.calls) != 2 {
		t.Fatalf("expected 2 writes, got %d", len(writer.calls))
	}

	namespaces := map[string]bool{}
	for _, c := range writer.calls {
		namespaces[c.namespaceName.Namespace] = true
	}
	if !namespaces["team-a"] {
		t.Error("expected write to team-a namespace")
	}
	if !namespaces["team-b"] {
		t.Error("expected write to team-b namespace")
	}
}

func TestMCPVirtualServerReconciler_doesNotWriteToDefaultNamespace(t *testing.T) {
	writer := &fakeVSConfigWriter{}
	lister := &fakeMCPExtLister{namespaces: []string{"team-a"}}

	r := &MCPVirtualServerReconciler{
		ConfigReaderWriter:    writer,
		MCPExtNamespaceLister: lister,
	}

	if err := r.writeVirtualServerConfig(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range writer.calls {
		if c.namespaceName == config.DefaultNamespaceName {
			t.Errorf("wrote to hardcoded default namespace %v, should only write to MCPGatewayExtension namespaces", config.DefaultNamespaceName)
		}
	}
}

// TestReconcile_VirtualServerDeletion_WritesConfig verifies that deleting an
// MCPVirtualServer triggers a config write that excludes the deleted resource.
func TestReconcile_VirtualServerDeletion_WritesConfig(t *testing.T) {
	now := metav1.NewTime(time.Now())

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

	remaining := &mcpv1alpha1.MCPVirtualServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vs-remaining",
			Namespace: "ns",
		},
		Spec: mcpv1alpha1.MCPVirtualServerSpec{
			Tools: []string{"tool_b"},
		},
	}

	writer := &fakeVSConfigWriter{}
	lister := &fakeMCPExtLister{namespaces: []string{"mcp-system"}}
	r := newVirtualServerReconciler(writer, lister, deleting, remaining)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "vs-deleting", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.Len(t, writer.calls, 1, "expected WriteVirtualServerConfig to be called once during deletion")

	written := writer.calls[0].configs
	for _, vs := range written {
		require.NotEqual(t, "ns/vs-deleting", vs.Name, "deleted virtual server must not appear in written config")
	}

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

	writer := &fakeVSConfigWriter{}
	lister := &fakeMCPExtLister{namespaces: []string{"mcp-system"}}
	r := newVirtualServerReconciler(writer, lister, last)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "vs-last", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.Len(t, writer.calls, 1, "expected WriteVirtualServerConfig to be called once during deletion")
	require.Empty(t, writer.calls[0].configs, "config must be empty when the last virtual server is deleted")
}
