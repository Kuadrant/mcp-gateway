package controller

import (
	"context"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestUpdateHTTPRouteStatus(t *testing.T) {
	err := mcpv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add mcp scheme: %v", err)
	}
	err = gatewayv1.Install(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add gatewayv1 scheme: %v", err)
	}

	httpRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Status: gatewayv1.HTTPRouteStatus{
			RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{
					{
						Conditions: []metav1.Condition{
							{
								Type:               "Programmed",
								Status:             metav1.ConditionTrue,
								Reason:             "Accepted",
								LastTransitionTime: metav1.Now(),
							},
						},
					},
				},
			},
		},
	}

	mcpsr := &mcpv1alpha1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcpsr",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{
				Kind:      "HTTPRoute",
				Name:      "test-route",
				Namespace: "default",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithStatusSubresource(&gatewayv1.HTTPRoute{}).WithObjects(httpRoute).Build()

	reconciler := &MCPReconciler{
		Client: fakeClient,
		Scheme: scheme.Scheme,
	}

	err = reconciler.updateHTTPRouteStatus(context.Background(), mcpsr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updatedRoute := &gatewayv1.HTTPRoute{}
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-route", Namespace: "default"}, updatedRoute)
	if err != nil {
		t.Fatalf("unexpected error getting route: %v", err)
	}

	if len(updatedRoute.Status.Parents) == 0 {
		t.Fatalf("expected parents in status")
	}

	foundProgrammed := false
	for _, cond := range updatedRoute.Status.Parents[0].Conditions {
		if cond.Type == "mcp.kuadrant.io/Programmed" {
			foundProgrammed = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("expected condition status True, got %v", cond.Status)
			}
			if cond.Reason != "InUseByMCPServerRegistration" {
				t.Errorf("expected reason InUseByMCPServerRegistration, got %v", cond.Reason)
			}
		}
		if cond.Type == "Programmed" {
			if cond.Reason != "Accepted" {
				t.Errorf("expected original Programmed condition to be untouched, got %v", cond.Reason)
			}
		}
	}

	if !foundProgrammed {
		t.Errorf("expected mcp.kuadrant.io/Programmed condition to be set")
	}
}
