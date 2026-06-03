package controller

import (
	"context"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateVirtualServerConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name              string
		virtualServers    []*mcpv1alpha1.MCPVirtualServer
		expectedTools     []string
		expectedPrompts   []string
		expectedResources []string
	}{
		{
			name: "maps tools, prompts, and resources",
			virtualServers: []*mcpv1alpha1.MCPVirtualServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vs1", Namespace: "test-ns"},
					Spec: mcpv1alpha1.MCPVirtualServerSpec{
						Tools:     []string{"tool1", "tool2"},
						Prompts:   []string{"prompt1"},
						Resources: []string{"test://r1", "test://r2"},
					},
				},
			},
			expectedTools:     []string{"tool1", "tool2"},
			expectedPrompts:   []string{"prompt1"},
			expectedResources: []string{"test://r1", "test://r2"},
		},
		{
			name: "omitted resources maps to nil",
			virtualServers: []*mcpv1alpha1.MCPVirtualServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vs2", Namespace: "test-ns"},
					Spec: mcpv1alpha1.MCPVirtualServerSpec{
						Tools:   []string{"tool1"},
						Prompts: []string{"prompt1"},
					},
				},
			},
			expectedTools:     []string{"tool1"},
			expectedPrompts:   []string{"prompt1"},
			expectedResources: nil,
		},
		{
			name:              "empty list returns no configs",
			virtualServers:    []*mcpv1alpha1.MCPVirtualServer{},
			expectedTools:     nil,
			expectedPrompts:   nil,
			expectedResources: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, vs := range tt.virtualServers {
				builder = builder.WithObjects(vs)
			}

			reconciler := &MCPVirtualServerReconciler{
				Client: builder.Build(),
			}

			result, err := reconciler.generateVirtualServerConfig(context.Background())
			require.NoError(t, err)

			if len(tt.virtualServers) == 0 {
				assert.Empty(t, result)
				return
			}

			require.Len(t, result, len(tt.virtualServers))
			assert.Equal(t, tt.expectedTools, result[0].Tools)
			assert.Equal(t, tt.expectedPrompts, result[0].Prompts)
			assert.Equal(t, tt.expectedResources, result[0].Resources)
		})
	}
}
