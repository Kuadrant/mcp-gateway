/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	agenticv1alpha1 "github.com/vibhor-5/accesspolicy-controller/api/v1alpha1"
	"github.com/vibhor-5/accesspolicy-controller/internal/translator"

	authorinov1beta3 "github.com/kuadrant/authorino/api/v1beta3"
	kuadrantv1 "github.com/kuadrant/kuadrant-operator/api/v1"

	"k8s.io/apimachinery/pkg/api/meta"
)

const gatewayKind = "Gateway"

// XAccessPolicyReconciler reconciles a XAccessPolicy object
type XAccessPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentic.agentic.networking.x-k8s.io,resources=xaccesspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentic.agentic.networking.x-k8s.io,resources=xaccesspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentic.agentic.networking.x-k8s.io,resources=xaccesspolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=kuadrant.io,resources=authpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *XAccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var policy agenticv1alpha1.XAccessPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if len(policy.Spec.TargetRefs) == 0 {
		return ctrl.Result{}, nil
	}

	targetRef := policy.Spec.TargetRefs[0]
	if targetRef.Kind != gatewayKind {
		r.updateStatus(&policy, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, agenticv1alpha1.PolicyReasonInvalidTarget, "TargetRef must be Gateway")
		return ctrl.Result{}, r.Status().Update(ctx, &policy)
	}

	var gateway gatewayapiv1.Gateway
	gwName := types.NamespacedName{Namespace: policy.Namespace, Name: targetRef.Name}
	if err := r.Get(ctx, gwName, &gateway); err != nil {
		r.updateStatus(&policy, agenticv1alpha1.PolicyConditionResolvedRefs, metav1.ConditionFalse, agenticv1alpha1.PolicyReasonGatewayNotFound, "Gateway not found")
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	r.updateStatus(&policy, agenticv1alpha1.PolicyConditionResolvedRefs, metav1.ConditionTrue, agenticv1alpha1.PolicyReasonResolved, "Gateway found")

	// Fetch all XAccessPolicies targeting this gateway
	var policyList agenticv1alpha1.XAccessPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(policy.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	var validPredicates []authorinov1beta3.PatternExpressionOrRef
	allValid := true

	for i := range policyList.Items {
		p := &policyList.Items[i]
		if len(p.Spec.TargetRefs) == 0 || p.Spec.TargetRefs[0].Name != gateway.Name {
			continue
		}

		for _, rule := range p.Spec.Rules {
			if rule.Authorization.Type == "CEL" {
				translatedExpr := translator.TranslateCEL(rule.Authorization.CEL.Expression)
				validPredicates = append(validPredicates, translatedExpr)
			}
		}
	}

	var combinedPredicates []authorinov1beta3.PatternExpressionOrRef
	if len(validPredicates) > 0 {
		var anyList []authorinov1beta3.UnstructuredPatternExpressionOrRef
		for _, pred := range validPredicates {
			anyList = append(anyList, authorinov1beta3.UnstructuredPatternExpressionOrRef{
				PatternExpressionOrRef: pred,
			})
		}
		combinedPredicates = append(combinedPredicates, authorinov1beta3.PatternExpressionOrRef{
			Any: anyList,
		})
	}

	authPolicyName := fmt.Sprintf("%s-auth", gateway.Name)
	authPolicy := &kuadrantv1.AuthPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authPolicyName,
			Namespace: gateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, authPolicy, func() error {
		if authPolicy.Labels == nil {
			authPolicy.Labels = map[string]string{}
		}
		authPolicy.Labels["app.kubernetes.io/managed-by"] = "accesspolicy-controller"

		// Set owner reference to the current policy (for GC)
		if err := controllerutil.SetControllerReference(&policy, authPolicy, r.Scheme); err != nil {
			log.Error(err, "unable to set owner reference")
		}

		// Gateway target
		authPolicy.Spec.TargetRef = gatewayapiv1alpha2.LocalPolicyTargetReferenceWithSectionName{
			LocalPolicyTargetReference: gatewayapiv1alpha2.LocalPolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  gatewayKind,
				Name:  gatewayapiv1.ObjectName(gateway.Name),
			},
		}

		if authPolicy.Spec.AuthScheme == nil {
			authPolicy.Spec.AuthScheme = &kuadrantv1.AuthSchemeSpec{}
		}
		if authPolicy.Spec.AuthScheme.Authorization == nil {
			authPolicy.Spec.AuthScheme.Authorization = map[string]kuadrantv1.MergeableAuthorizationSpec{}
		}

		authPolicy.Spec.AuthScheme.Authorization["combined-rules"] = kuadrantv1.MergeableAuthorizationSpec{
			AuthorizationSpec: authorinov1beta3.AuthorizationSpec{
				AuthorizationMethodSpec: authorinov1beta3.AuthorizationMethodSpec{
					PatternMatching: &authorinov1beta3.PatternMatchingAuthorizationSpec{
						Patterns: combinedPredicates,
					},
				},
			},
		}

		return nil
	})

	if err != nil {
		r.updateStatus(&policy, agenticv1alpha1.PolicyConditionProgrammed, metav1.ConditionFalse, "ProgramError", err.Error())
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{}, err
	}

	log.Info("Reconciled AuthPolicy", "operation", op)

	if allValid {
		r.updateStatus(&policy, agenticv1alpha1.PolicyConditionProgrammed, metav1.ConditionTrue, agenticv1alpha1.PolicyReasonProgrammed, "Successfully programmed Kuadrant AuthPolicy")
		if err := r.Status().Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *XAccessPolicyReconciler) updateStatus(policy *agenticv1alpha1.XAccessPolicy, conditionType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:    conditionType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *XAccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agenticv1alpha1.XAccessPolicy{}).
		Owns(&kuadrantv1.AuthPolicy{}).
		Watches(
			&gatewayapiv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForGateway),
		).
		Complete(r)
}

func (r *XAccessPolicyReconciler) findPoliciesForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayapiv1.Gateway)
	if !ok {
		return nil
	}

	var policyList agenticv1alpha1.XAccessPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(gateway.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, p := range policyList.Items {
		if len(p.Spec.TargetRefs) > 0 && p.Spec.TargetRefs[0].Name == gateway.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      p.Name,
					Namespace: p.Namespace,
				},
			})
		}
	}
	return requests
}
