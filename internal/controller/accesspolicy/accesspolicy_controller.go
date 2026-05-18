package accesspolicy

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/Kuadrant/mcp-gateway/api/agentic/v1alpha1"
)

type AccessPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

func (r *AccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("accesspolicy", req.NamespacedName)

	var policy agenticv1alpha1.AccessPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, ref := range policy.Spec.TargetRefs {
		var backend agenticv1alpha1.Backend
		key := types.NamespacedName{Name: string(ref.Name), Namespace: policy.Namespace}
		if err := r.Get(ctx, key, &backend); err != nil {
			log.Error(err, "Backend not found", "backend", ref.Name)
			return ctrl.Result{}, r.setCondition(ctx, &policy,
				"TargetNotFound", metav1.ConditionFalse,
				fmt.Sprintf("Backend %s not found", ref.Name))
		}
	}

	authPolicy := r.buildAuthPolicy(&policy)
	if authPolicy == nil {
		return ctrl.Result{}, r.setCondition(ctx, &policy,
			"Invalid", metav1.ConditionFalse, "AccessPolicy has no targetRefs")
	}

	if err := ctrl.SetControllerReference(&policy, authPolicy, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existing := &AuthPolicy{}
	err := r.Get(ctx, types.NamespacedName{
		Name: authPolicy.Name, Namespace: authPolicy.Namespace,
	}, existing)

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, authPolicy); err != nil {
			return ctrl.Result{}, err
		}
	} else if err == nil {
		existing.Spec = authPolicy.Spec
		if err := r.Update(ctx, existing); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.setCondition(ctx, &policy,
		"Enforced", metav1.ConditionTrue, "AuthPolicy generated and applied")
}

func (r *AccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agenticv1alpha1.AccessPolicy{}).
		Owns(&AuthPolicy{}).
		Complete(r)
}

func (r *AccessPolicyReconciler) setCondition(
	ctx context.Context,
	policy *agenticv1alpha1.AccessPolicy,
	condType string,
	status metav1.ConditionStatus,
	message string,
) error {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: policy.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             condType,
		Message:            message,
	})
	return r.Status().Update(ctx, policy)
}
