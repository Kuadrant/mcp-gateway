package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// VirtualServerConfigReaderWriter interface to write virtual server config
type VirtualServerConfigReaderWriter interface {
	WriteVirtualServerConfig(ctx context.Context, virtualServers []config.VirtualServerConfig, namespaceName types.NamespacedName) error
}

// MCPExtNamespaceLister lists namespaces of all active MCPGatewayExtensions.
type MCPExtNamespaceLister interface {
	ListMCPGatewayExtensionNamespaces(ctx context.Context) ([]string, error)
}

// MCPVirtualServerReconciler reconciles a MCPVirtualServer object
type MCPVirtualServerReconciler struct {
	client.Client
	DirectAPIReader       client.Reader
	Scheme                *runtime.Scheme
	log                   *slog.Logger
	ConfigReaderWriter    VirtualServerConfigReaderWriter
	MCPExtNamespaceLister MCPExtNamespaceLister
}

var defaultRequeueTime = time.Second * 2

// +kubebuilder:rbac:groups=mcp.kuadrant.io,resources=mcpvirtualservers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=mcp.kuadrant.io,resources=mcpvirtualservers/status,verbs=get;update
// +kubebuilder:rbac:groups=mcp.kuadrant.io,resources=mcpvirtualservers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPVirtualServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mcpVS := &mcpv1alpha1.MCPVirtualServer{}
	if err := r.Get(ctx, req.NamespacedName, mcpVS); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.Info("reconciling mcpvirtualserver", "name", mcpVS.Name, "namespace", mcpVS.Namespace)

	// handle deletion
	if !mcpVS.DeletionTimestamp.IsZero() {
		logger.Info("mcpvirtualserver is being deleted", "name", mcpVS.Name, "namespace", mcpVS.Namespace)
		if controllerutil.ContainsFinalizer(mcpVS, mcpGatewayFinalizer) {
			vsConfig, err := r.generateVirtualServerConfig(ctx)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("mcpvirtualserver failed to generate virtual server config during deletion %w", err)
			}
			if err := r.writeVirtualServerConfig(ctx, vsConfig); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, fmt.Errorf("mcpvirtualserver failed to write virtual server config during deletion %w", err)
			}
			controllerutil.RemoveFinalizer(mcpVS, mcpGatewayFinalizer)
			if err := r.Update(ctx, mcpVS); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// add finalizer if not present
	if !controllerutil.ContainsFinalizer(mcpVS, mcpGatewayFinalizer) {
		if controllerutil.AddFinalizer(mcpVS, mcpGatewayFinalizer) {
			if err := r.Update(ctx, mcpVS); err != nil {
				if apierrors.IsConflict(err) {
					logger.V(1).Info("mcpvirtualserver conflict err requeuing")
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, err
				}
				return ctrl.Result{}, err
			}
		}
	}

	logger.V(1).Info("mcpvirtualserver generating config")

	vsConfig, err := r.generateVirtualServerConfig(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("mcpvirtualserver failed to generate virtual server config during reconcile %w", err)
	}

	logger.V(1).Info("mcpvirtualserver writing config")
	if err := r.writeVirtualServerConfig(ctx, vsConfig); err != nil {
		if apierrors.IsConflict(err) {
			logger.Info("mcpvirtualserver conflict on updating the config for virtual servers will retry in 5 seconds")
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
		return ctrl.Result{}, fmt.Errorf("mcpvirtualserver failed to write virtual server config during reconcile %w", err)
	}
	logger.V(1).Info("mcpvirtualserver reconcile complete", "name", mcpVS.Name, "namespace", mcpVS.Namespace)
	// update status of virtual server
	return ctrl.Result{}, nil
}

// writeVirtualServerConfig writes vsConfig to the config secret of every MCPGatewayExtension namespace.
// MCPVirtualServer config must reach all gateway instances, not just the default namespace.
// Errors are collected and all namespaces are attempted before returning so a single bad namespace
// (quota, webhook) does not block config delivery to the rest.
func (r *MCPVirtualServerReconciler) writeVirtualServerConfig(ctx context.Context, vsConfig []config.VirtualServerConfig) error {
	namespaces, err := r.MCPExtNamespaceLister.ListMCPGatewayExtensionNamespaces(ctx)
	if err != nil {
		return fmt.Errorf("failed to list mcpgatewayextension namespaces: %w", err)
	}
	var conflicts, hard []error
	for _, ns := range namespaces {
		if err := r.ConfigReaderWriter.WriteVirtualServerConfig(ctx, vsConfig, config.NamespaceName(ns)); err != nil {
			if apierrors.IsConflict(err) {
				conflicts = append(conflicts, fmt.Errorf("namespace %s: %w", ns, err))
			} else {
				hard = append(hard, fmt.Errorf("namespace %s: %w", ns, err))
			}
		}
	}
	if len(hard) > 0 {
		return errors.Join(hard...)
	}
	return errors.Join(conflicts...)
}

func (r *MCPVirtualServerReconciler) generateVirtualServerConfig(ctx context.Context) ([]config.VirtualServerConfig, error) {
	log := log.FromContext(ctx)
	virtualServers := []config.VirtualServerConfig{}
	mcpVirtualServerList := &mcpv1alpha1.MCPVirtualServerList{}
	if err := r.List(ctx, mcpVirtualServerList); err != nil {
		log.Error(err, "Failed to list MCPVirtualServers")
		return virtualServers, err
	}
	// generate the entire virtual server config fresh rather than merge etc (future optimization)
	for _, mcpVirtualServer := range mcpVirtualServerList.Items {
		if mcpVirtualServer.DeletionTimestamp != nil {
			continue
		}
		virtualServerName := fmt.Sprintf("%s/%s", mcpVirtualServer.Namespace, mcpVirtualServer.Name)
		virtualServers = append(virtualServers, config.VirtualServerConfig{
			Name:    virtualServerName,
			Tools:   mcpVirtualServer.Spec.Tools,
			Prompts: mcpVirtualServer.Spec.Prompts,
		})
	}
	return virtualServers, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPVirtualServerReconciler) SetupWithManager(_ context.Context, mgr ctrl.Manager) error {
	r.log = slog.New(logr.ToSlogHandler(mgr.GetLogger()))

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPVirtualServer{}).
		// re-reconcile all MCPVirtualServers when an MCPGatewayExtension changes
		// so config is immediately written to any newly added namespace.
		Watches(&mcpv1alpha1.MCPGatewayExtension{},
			handler.EnqueueRequestsFromMapFunc(r.findAllMCPVirtualServers)).
		Named("mcpvirtualserver").
		Complete(r)
}

// findAllMCPVirtualServers enqueues all MCPVirtualServers for reconciliation.
// Used when MCPGatewayExtension changes so every VS writes config to the updated namespace set.
func (r *MCPVirtualServerReconciler) findAllMCPVirtualServers(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &mcpv1alpha1.MCPVirtualServerList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "failed to list MCPVirtualServers for MCPGatewayExtension watch — VS reconciles will not be triggered")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      list.Items[i].Name,
				Namespace: list.Items[i].Namespace,
			},
		})
	}
	return requests
}
