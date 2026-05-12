package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// VirtualServerConfigReaderWriter interface to write virtual server config
type VirtualServerConfigReaderWriter interface {
	WriteVirtualServerConfig(ctx context.Context, virtualServers []config.VirtualServerConfig, namespaceName types.NamespacedName) error
}

// MCPVirtualServerReconciler reconciles a MCPVirtualServer object
type MCPVirtualServerReconciler struct {
	client.Client
	DirectAPIReader    client.Reader
	Scheme             *runtime.Scheme
	log                *slog.Logger
	ConfigReaderWriter VirtualServerConfigReaderWriter
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
			logger.Info("deleting mcpvirtualserver", "name", mcpVS.Name, "namespace", mcpVS.Namespace)
			// TODO remove from config
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
				if errors.IsConflict(err) {
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
	if err := r.ConfigReaderWriter.WriteVirtualServerConfig(ctx, vsConfig, config.DefaultNamespaceName); err != nil {
		if errors.IsConflict(err) {
			logger.Info("mcpvirtualserver conflict on updating the config for virtual servers will retry in 5 seconds")
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
		return ctrl.Result{}, fmt.Errorf("mcpvirtualserver failed to write virtual server config during reconcile %w", err)
	}
	logger.V(1).Info("mcpvirtualserver reconcile complete", "name", mcpVS.Name, "namespace", mcpVS.Namespace)
	// update status of virtual server
	return ctrl.Result{}, nil
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
		Named("mcpvirtualserver").
		Complete(r)
}
