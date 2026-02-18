package controller

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (

	// CredentialSecretLabel is the required label for credential secrets
	CredentialSecretLabel = "mcp.kuadrant.io/credential" //nolint:gosec // not a credential, just a label name
	// CredentialSecretValue is the required value for credential secrets
	CredentialSecretValue = "true"
	// HTTPRouteIndex used to find MCPServerRegistrations
	HTTPRouteIndex = "spec.targetRef.httproute"
	// ProgrammedHTTPRouteIndex used to find programmed httproutes
	ProgrammedHTTPRouteIndex = "status.hasProgrammedCondition"
)

// ServerInfo holds server information
type ServerInfo struct {
	ID                 string
	Endpoint           string
	Hostname           string
	ToolPrefix         string
	HTTPRouteName      string
	HTTPRouteNamespace string
	Credential         string
}

// MCPServerConfigReaderWriter adds and removes MCPServers to the config
type MCPServerConfigReaderWriter interface {
	UpsertMCPServer(ctx context.Context, server config.MCPServer, namespaceName types.NamespacedName) error
	// RemoveMCPServer removes a server from all config secrets cluster-wide
	RemoveMCPServer(ctx context.Context, serverName string) error
}

// MCPReconciler reconciles both MCPServerRegistration and MCPVirtualServer resources
type MCPReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	DirectAPIReader       client.Reader // uncached reader for fetching secrets
	ConfigReaderWriter    MCPServerConfigReaderWriter
	MCPExtFinderValidator MCPGatewayExtensionFinderValidator
}

// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpserverregistrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpserverregistrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpvirtualservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// TODO: consider making targetRef immutable since changing it is not currently handled

// Reconcile reconciles both MCPServerRegistration and MCPVirtualServer resources
func (r *MCPReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := logf.FromContext(ctx).WithValues("resource", "mcpserverregistration")
	logger.V(1).Info("Reconciling", "mcpregistrationname", req.Name, "namespace", req.Namespace)

	mcpsr := &mcpv1alpha1.MCPServerRegistration{}
	if err := r.Get(ctx, req.NamespacedName, mcpsr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.V(1).Info("found", "mcpregistrationname", mcpsr.Name, "namespace", mcpsr.Namespace)

	// handle deletion
	if !mcpsr.DeletionTimestamp.IsZero() {
		logger.Info("deleting", "mcpregistrationname", mcpsr.Name, "namespace", mcpsr.Namespace)
		if controllerutil.ContainsFinalizer(mcpsr, mcpGatewayFinalizer) {
			if err := r.ConfigReaderWriter.RemoveMCPServer(ctx, mcpServerName(mcpsr)); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.updateHTTPRouteStatus(ctx, mcpsr); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(mcpsr, mcpGatewayFinalizer)
			if err := r.Update(ctx, mcpsr); err != nil {
				if errors.IsConflict(err) {
					logger.V(1).Info("conflict err requeuing to retry")
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// add finalizer if not present
	if !controllerutil.ContainsFinalizer(mcpsr, mcpGatewayFinalizer) {
		logger.V(1).Info("no finalizer adding", "mcpregistrationname", mcpsr.Name)
		if controllerutil.AddFinalizer(mcpsr, mcpGatewayFinalizer) {
			if err := r.Update(ctx, mcpsr); err != nil {
				if errors.IsConflict(err) {
					logger.V(1).Info("conflict err requeuing to retry")
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, err
			}
			logger.V(1).Info("finalizer added", "mcpregistrationname", mcpsr.Name)
			return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
		}
	}
	logger.Info("main reconcile logic starting for", "mcpregistrationname", mcpsr.Name)

	// get the HTTPRoute and gateway(s) this MCPServerRegistration targets
	targetRoute, err := r.getTargetHTTPRoute(ctx, mcpsr)
	if err != nil {
		if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
			if errors.IsConflict(err) {
				// don't log these as they are just noise
				return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
		}
		return ctrl.Result{}, fmt.Errorf("reconcile failed %w", err)
	}
	logger.Info("target route found ", "mcpregistrationname", targetRoute.Name)

	// find gateways that have accepted the httproute
	validGateways, err := r.findValidGatewaysForMCPServer(ctx, targetRoute)
	if err != nil {
		if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
			if errors.IsConflict(err) {
				// don't log these as they are just noise
				return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
		}
		return ctrl.Result{}, fmt.Errorf("reconcile failed %w", err)
	}

	// no valid gateways found, exit with error
	if len(validGateways) == 0 {
		err := fmt.Errorf("no valid gateways for httproute")
		logger.Error(err, "failed to find any valid gateways", "route", targetRoute)
		if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
			if errors.IsConflict(err) {
				// don't log these as they are just noise
				return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
		}
		return ctrl.Result{}, fmt.Errorf("reconcile failed %w", err)
	}
	logger.Info("valid gateways discovered ", "total", len(validGateways), "mcpregistrationname", mcpsr.Name)
	// check for valid MCPGatewayExtension
	validNamespaces := []string{}
	for _, vg := range validGateways {
		mcpGatewayExtensions, err := r.MCPExtFinderValidator.FindValidMCPGatewayExtsForGateway(ctx, vg)
		if err != nil {
			logger.Error(err, "failed to find valid mcpgatewayextension ", "gateway", vg, "mcpserverregistration", mcpsr)
			if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
				if errors.IsConflict(err) {
					// don't log these as they are just noise
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
			}
		}
		if len(mcpGatewayExtensions) == 0 {
			// this is not an error so we are going to exit
			if err := r.updateStatus(ctx, mcpsr, false, "no valid mcpgatewayextensions configured", 0); err != nil {
				if errors.IsConflict(err) {
					// don't log these as they are just noise
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		for _, vext := range mcpGatewayExtensions {
			validNamespaces = append(validNamespaces, vext.Namespace)
		}
	}

	mcpServerconfig, err := r.buildMCPServerConfig(ctx, targetRoute, mcpsr)
	if err != nil {
		if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
			if errors.IsConflict(err) {
				// don't log these as they are just noise
				return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
		}
		return reconcile.Result{}, fmt.Errorf("failed to reconcile %s %w", mcpsr.Name, err)
	}
	for _, configNs := range validNamespaces {
		if err := r.ConfigReaderWriter.UpsertMCPServer(ctx, *mcpServerconfig, config.NamespaceName(configNs)); err != nil {
			if err := r.updateStatus(ctx, mcpsr, false, err.Error(), 0); err != nil {
				if errors.IsConflict(err) {
					// don't log these as they are just noise
					return ctrl.Result{RequeueAfter: defaultRequeueTime}, nil
				}
				return ctrl.Result{}, fmt.Errorf("reconcile failed: status update failed %w", err)
			}
			return reconcile.Result{}, fmt.Errorf("failed to reconcile %s %w", mcpsr.Name, err)
		}
	}

	// Everything is in place now so we will now poll the gateway to check the registration status of the mcpserver
	// NOTE We loop here but there should only ever be one
	for _, mcpExtensionNS := range validNamespaces {
		if err := r.setMCPServerRegistrationStatus(ctx, mcpExtensionNS, mcpsr, string(mcpServerconfig.ID())); err != nil {
			if strings.Contains(err.Error(), "mcp server is not present in gateway yet") {
				logger.V(1).Info("config not loaded in gateway yet. Will retry status check", "mcpserverregistration", mcpsr.Name)
				// no point hammering the gateway when we know we are waiting for the config to be loaded
				return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
			}
			logger.Error(err, "failed to set mcpserverregistration status", "mcpserverregistration", mcpsr.Name)
			// TODO: handle persistent failures with specific error types
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil

}

// TODO: share this format with the broker package
func mcpServerName(mcp *mcpv1alpha1.MCPServerRegistration) string {
	return fmt.Sprintf(
		"%s/%s",
		mcp.Namespace,
		mcp.Name,
	)
}

// findValidGatewaysForMCPServer returns the gateways the httproute targeted by the MCPServerRegistration is the child of
func (r *MCPReconciler) findValidGatewaysForMCPServer(ctx context.Context, targetHTTPRoute *gatewayv1.HTTPRoute) ([]*gatewayv1.Gateway, error) {
	logger := logf.FromContext(ctx).WithName("findValidGatewaysForMCPServer")
	var validGateways = []*gatewayv1.Gateway{}
	if len(targetHTTPRoute.Status.Parents) > 0 {
		// lets fetch the parents that are valid
		for _, parent := range targetHTTPRoute.Status.Parents {
			acceptedCond := meta.FindStatusCondition(parent.Conditions, string(gatewayv1.GatewayConditionAccepted))
			if acceptedCond.Status == metav1.ConditionTrue {
				pr := parent.ParentRef.DeepCopy()
				if pr.Namespace == nil {
					pr.Namespace = ptr.To(gatewayv1.Namespace(targetHTTPRoute.Namespace))
				}
				pg, err := r.getTargetGatewaysFromParentRef(ctx, pr)
				if err != nil {
					if errors.IsNotFound(err) {
						// log but continue we will handle no gateways found later
						logger.Error(err, "failed to find parent for httproute")
						continue
					}
					//unexpected error
					return validGateways, fmt.Errorf("unexpected error getting gateway from httproute %w", err)
				}
				// as this httproute was accepted by the gateway lets add it valid gateways
				validGateways = append(validGateways, pg)
			}
		}
	}
	return validGateways, nil
}

func (r *MCPReconciler) getTargetHTTPRoute(ctx context.Context, mcpsr *mcpv1alpha1.MCPServerRegistration) (*gatewayv1.HTTPRoute, error) {
	namespaceName := types.NamespacedName{Namespace: mcpsr.Namespace, Name: mcpsr.Spec.TargetRef.Name}
	logger := logf.FromContext(ctx).WithValues("method", "getTargetHTTPRoute")
	logger.V(1).Info("httproute target ", "namespacename ", namespaceName)
	targetRoute := &gatewayv1.HTTPRoute{}
	if err := r.Get(ctx, namespaceName, targetRoute); err != nil {
		return nil, fmt.Errorf("failed to get targeted httproute %w", err)
	}
	return targetRoute, nil

}

func (r *MCPReconciler) getTargetGatewaysFromParentRef(ctx context.Context, parent *gatewayv1.ParentReference) (*gatewayv1.Gateway, error) {
	namespaceName := types.NamespacedName{Namespace: string(*parent.Namespace), Name: string(parent.Name)}
	g := &gatewayv1.Gateway{}
	if err := r.Get(ctx, namespaceName, g); err != nil {
		return nil, fmt.Errorf("failed to get parent gateway for httproute: %w", err)
	}
	return g, nil
}

// setMCPServerRegistrationStatus polls the broker to check registration status and updates the MCPServerRegistration status
func (r *MCPReconciler) setMCPServerRegistrationStatus(ctx context.Context, mcpGatewayExtNS string, mcpsr *mcpv1alpha1.MCPServerRegistration, serverID string) error {
	log := logf.FromContext(ctx)
	log.V(1).Info("setMCPServerRegistrationStatus", "mcpregistrationname", mcpsr.Name, "valid gateway extension namespace", mcpGatewayExtNS)

	validator := NewServerValidator(r.Client)
	// TODO this currently lists all servers in the extension
	statusResponse, err := validator.ValidateServers(ctx, mcpGatewayExtNS)
	if err != nil {
		log.Error(err, "Failed to validate server status via broker")
		ready, message := false, fmt.Sprintf("Validation failed: %v", err)
		if err := r.updateStatus(ctx, mcpsr, ready, message, 0); err != nil {
			log.Error(err, "Failed to update status")
			return err
		}
		return err
	}

	var gatewayServerStatus upstream.ServerValidationStatus
	for _, sr := range statusResponse.Servers {
		log.Info("server response ", "id", sr.ID, "controller id ", serverID)
		if sr.ID == serverID {
			gatewayServerStatus = sr
			break
		}
	}

	log.Info("server status ", "mcpregistrationname", mcpsr.Name, "status", gatewayServerStatus)
	message := "mcp server is not present in gateway yet"
	// if there is an id that matches then the gateway is registering the mcp
	if gatewayServerStatus.ID != "" {
		if err := r.updateStatus(ctx, mcpsr, gatewayServerStatus.Ready, gatewayServerStatus.Message, gatewayServerStatus.TotalTools); err != nil {
			log.Error(err, "Failed to update status")
			return err
		}

		if err := r.updateHTTPRouteStatus(ctx, mcpsr); err != nil {
			log.Error(err, "Failed to update HTTPRoute status")
		}
		if !gatewayServerStatus.Ready {
			return fmt.Errorf("%s", message)
		}
		log.V(1).Info("server is ready")
		return nil
	}
	// otherwise it hasn't picked up the config yet

	if err := r.updateStatus(ctx, mcpsr, gatewayServerStatus.Ready, message, 0); err != nil {
		return err
	}

	return fmt.Errorf("%s", message)
}

func (r *MCPReconciler) buildMCPServerConfig(ctx context.Context, targetRoute *gatewayv1.HTTPRoute, mcpsr *mcpv1alpha1.MCPServerRegistration) (*config.MCPServer, error) {
	if mcpsr.DeletionTimestamp != nil {
		// don't add deleting mcpserver
		return nil, fmt.Errorf("cant generate config for deleting server %s/%s", mcpsr.Namespace, mcpsr.Name)
	}
	serverInfo, err := r.buildServerInfoFromHTTPRoute(ctx, targetRoute, mcpsr.Spec.Path)
	if err != nil {
		return nil, err
	}

	serverName := mcpServerName(mcpsr)
	serverConfig := config.MCPServer{
		Name:       serverName,
		URL:        serverInfo.Endpoint,
		Hostname:   serverInfo.Hostname,
		ToolPrefix: mcpsr.Spec.ToolPrefix,
		// TODO implement add to MCPServerRegistration CRD
		Enabled: true,
	}

	// add credential env var if configured
	if mcpsr.Spec.CredentialRef != nil {
		secret := &corev1.Secret{}
		err := r.DirectAPIReader.Get(ctx, types.NamespacedName{
			Name:      mcpsr.Spec.CredentialRef.Name,
			Namespace: mcpsr.Namespace,
		}, secret)
		if err != nil {
			if errors.IsNotFound(err) {
				return nil, fmt.Errorf("credential secret %s not found", mcpsr.Spec.CredentialRef.Name)
			}
			return nil, fmt.Errorf("failed to get credential secret: %w", err)
		}

		// check for required label
		if secret.Labels == nil || secret.Labels[CredentialSecretLabel] != CredentialSecretValue {
			return nil, fmt.Errorf("credential secret %s is missing required label %s=%s",
				mcpsr.Spec.CredentialRef.Name, CredentialSecretLabel, CredentialSecretValue)
		}

		val, ok := secret.Data[mcpsr.Spec.CredentialRef.Key]
		if !ok {
			return nil, fmt.Errorf("credential secret %s missing key %s", mcpsr.Spec.CredentialRef.Name, mcpsr.Spec.CredentialRef.Key)
		}
		serverConfig.Credential = string(val)

	}
	return &serverConfig, nil
}

func (r *MCPReconciler) buildServerInfoFromHTTPRoute(ctx context.Context, httpRoute *gatewayv1.HTTPRoute, path string) (*ServerInfo, error) {
	route := WrapHTTPRoute(httpRoute)

	if err := route.Validate(); err != nil {
		return nil, err
	}

	var endpoint, routingHostname string

	if route.IsHostnameBackend() {
		logf.FromContext(ctx).V(1).Info("processing external service via Hostname backendRef", "host", route.BackendName())

		if !isValidHostname(route.BackendName()) {
			return nil, fmt.Errorf("invalid hostname in backendRef: %s", route.BackendName())
		}

		port := "443"
		if route.BackendPort() != nil {
			port = fmt.Sprintf("%d", *route.BackendPort())
		}

		endpoint = fmt.Sprintf("https://%s%s", net.JoinHostPort(route.BackendName(), port), path)
		routingHostname = route.FirstHostname()

	} else if route.IsServiceBackend() {
		service := &corev1.Service{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      route.BackendName(),
			Namespace: route.BackendNamespace(),
		}, service); err != nil {
			return nil, fmt.Errorf("failed to get service %s: %w", route.BackendName(), err)
		}

		endpoint, routingHostname = r.buildServiceEndpoint(route, service, path)

	} else {
		return nil, fmt.Errorf("unsupported backend reference kind: %s", route.BackendKind())
	}

	return &ServerInfo{
		Endpoint:           endpoint,
		Hostname:           routingHostname,
		HTTPRouteName:      route.Name,
		HTTPRouteNamespace: route.Namespace,
		Credential:         "",
	}, nil
}

// buildServiceEndpoint builds the endpoint URL and routing hostname for a Service backend
func (r *MCPReconciler) buildServiceEndpoint(route *HTTPRouteWrapper, service *corev1.Service, path string) (endpoint, routingHostname string) {
	isExternal := service.Spec.Type == corev1.ServiceTypeExternalName

	var hostAndPort string
	if isExternal {
		hostAndPort = service.Spec.ExternalName
	} else {
		hostAndPort = fmt.Sprintf("%s.%s.svc.cluster.local", route.BackendName(), route.BackendNamespace())
	}

	if route.BackendPort() != nil {
		hostAndPort = fmt.Sprintf("%s:%d", hostAndPort, *route.BackendPort())
	}

	protocol := r.determineProtocol(route, service, isExternal)
	endpoint = fmt.Sprintf("%s://%s%s", protocol, hostAndPort, path)

	if isExternal {
		if idx := strings.LastIndex(hostAndPort, ":"); idx != -1 {
			routingHostname = hostAndPort[:idx]
		} else {
			routingHostname = hostAndPort
		}
	} else {
		routingHostname = route.FirstHostname()
	}

	return endpoint, routingHostname
}

// determineProtocol determines the protocol (http/https) for the service endpoint
func (r *MCPReconciler) determineProtocol(route *HTTPRouteWrapper, service *corev1.Service, isExternal bool) string {
	if isExternal {
		for _, port := range service.Spec.Ports {
			if route.BackendPort() != nil && port.Port == *route.BackendPort() {
				if port.AppProtocol != nil && strings.ToLower(*port.AppProtocol) == "https" {
					return "https"
				}
				break
			}
		}
		return "http"
	}

	if route.UsesHTTPS() {
		return "https"
	}
	return "http"
}

// isValidHostname validates the hostname to prevent path injection
func isValidHostname(hostname string) bool {
	if hostname == "" {
		return false
	}
	u, err := url.Parse("//" + hostname)
	if err != nil {
		return false
	}
	return u.Host == hostname
}

func (r *MCPReconciler) updateHTTPRouteStatus(ctx context.Context, mcpsr *mcpv1alpha1.MCPServerRegistration) error {
	targetRef := mcpsr.Spec.TargetRef

	if targetRef.Kind != "HTTPRoute" {
		return nil
	}

	namespace := mcpsr.Namespace
	if targetRef.Namespace != "" {
		namespace = targetRef.Namespace
	}

	httpRoute := &gatewayv1.HTTPRoute{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      targetRef.Name,
		Namespace: namespace,
	}, httpRoute)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get HTTPRoute: %w", err)
	}

	condition := metav1.Condition{
		Type:               "Programmed",
		ObservedGeneration: httpRoute.Generation,
		LastTransitionTime: metav1.Now(),
	}

	condition.Status = metav1.ConditionTrue
	condition.Reason = "InUseByMCPServerRegistration"
	// We don't include the MCP Server in the status because >1 MCPServerRegistration may reference the same HTTPRoute
	condition.Message = "HTTPRoute is referenced by at least one MCPServerRegistration"
	var changed bool
	for i := range httpRoute.Status.Parents {
		if mcpsr.DeletionTimestamp != nil {
			changed = meta.RemoveStatusCondition(&httpRoute.Status.Parents[i].Conditions, "Programmed")
		} else {
			changed = meta.SetStatusCondition(&httpRoute.Status.Parents[i].Conditions, condition)
		}
		if changed {
			if err := r.Status().Update(ctx, httpRoute); err != nil {
				return err
			}
		}
	}
	return nil

}

func (r *MCPReconciler) updateStatus(
	ctx context.Context,
	mcpsr *mcpv1alpha1.MCPServerRegistration,
	ready bool,
	message string,
	toolCount int,
) error {
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "NotReady",
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	if ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Ready"
	}

	statusChanged := false
	found := false
	for i, cond := range mcpsr.Status.Conditions {
		if cond.Type == condition.Type {
			// only update LastTransitionTime if the STATUS actually changed (True->False or False->True)
			// this ensures we track the time when we first entered a state, not when messages change
			if cond.Status == condition.Status {
				// status hasn't changed, preserve existing LastTransitionTime
				condition.LastTransitionTime = cond.LastTransitionTime
			}
			// check if anything actually changed
			if cond.Status != condition.Status || cond.Reason != condition.Reason || cond.Message != condition.Message {
				statusChanged = true
			}
			mcpsr.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		mcpsr.Status.Conditions = append(mcpsr.Status.Conditions, condition)
		statusChanged = true
	}
	if mcpsr.Status.DiscoveredTools != toolCount {
		mcpsr.Status.DiscoveredTools = toolCount
		statusChanged = true
	}

	// only update if something actually changed
	if !statusChanged {
		return nil
	}

	return r.Status().Update(ctx, mcpsr)
}

// SetupWithManager sets up the reconciler
func (r *MCPReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := setupIndexMCPRegistrationToHTTPRoute(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup required index from MCPServerRegistration to httproutes %w", err)
	}

	if err := setupIndexProgrammedHTTPRoutes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to setup required index for programmed httproutes %w", err)
	}

	controller := ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServerRegistration{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&gatewayv1.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findMCPServerRegistrationsForHTTPRoute),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findMCPServerRegistrationsForSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// TODO add a cache filter
				secret := obj.(*corev1.Secret)
				return secret.Labels != nil && secret.Labels[CredentialSecretLabel] == CredentialSecretValue
			})),
		).
		Watches(
			&mcpv1alpha1.MCPGatewayExtension{},
			handler.EnqueueRequestsFromMapFunc(r.findMCPServerRegistrationsForMCPGatewayExtension),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		)

	return controller.Complete(r)
}

func httpRouteIndexValue(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func setupIndexProgrammedHTTPRoutes(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &gatewayv1.HTTPRoute{}, ProgrammedHTTPRouteIndex, func(rawObj client.Object) []string {
		httpRoute := rawObj.(*gatewayv1.HTTPRoute)
		for _, parentStatus := range httpRoute.Status.Parents {
			for _, condition := range parentStatus.Conditions {
				if condition.Type == "Programmed" && condition.Status == metav1.ConditionTrue {
					return []string{"true"}
				}
			}
		}
		return []string{"false"}
	}); err != nil {
		return err
	}
	return nil
}

func setupIndexMCPRegistrationToHTTPRoute(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &mcpv1alpha1.MCPServerRegistration{}, HTTPRouteIndex, func(rawObj client.Object) []string {
		mcpsr := rawObj.(*mcpv1alpha1.MCPServerRegistration)
		targetRef := mcpsr.Spec.TargetRef
		if targetRef.Kind == "HTTPRoute" {
			namespace := targetRef.Namespace
			if namespace == "" {
				namespace = mcpsr.Namespace
			}
			return []string{httpRouteIndexValue(namespace, targetRef.Name)}
		}
		return []string{}
	}); err != nil {
		return err
	}
	return nil
}

// findMCPServerRegistrationsForHTTPRoute finds all MCPServerRegistrations that reference the given HTTPRoute
func (r *MCPReconciler) findMCPServerRegistrationsForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	httpRoute := obj.(*gatewayv1.HTTPRoute)
	log := logf.FromContext(ctx).WithValues("HTTPRoute", httpRoute.Name, "namespace", httpRoute.Namespace)

	indexKey := httpRouteIndexValue(httpRoute.Namespace, httpRoute.Name)
	mcpsrList := &mcpv1alpha1.MCPServerRegistrationList{}
	if err := r.List(ctx, mcpsrList, client.MatchingFields{HTTPRouteIndex: indexKey}); err != nil {
		log.Error(err, "Failed to list MCPServerRegistrations using index")
		return nil
	}

	var requests []reconcile.Request
	for _, mcpsr := range mcpsrList.Items {
		log.V(1).Info("Found MCPServerRegistration referencing HTTPRoute via index",
			"MCPServerRegistration", mcpsr.Name,
			"MCPServerRegistrationNamespace", mcpsr.Namespace)
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      mcpsr.Name,
				Namespace: mcpsr.Namespace,
			},
		})
	}

	return requests
}

// findMCPServerRegistrationsForSecret finds MCPServerRegistrations referencing the given secret
func (r *MCPReconciler) findMCPServerRegistrationsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	log := logf.FromContext(ctx).WithValues("Secret", secret.Name, "namespace", secret.Namespace)

	// list mcpservers in same namespace
	mcpsrList := &mcpv1alpha1.MCPServerRegistrationList{}
	if err := r.List(ctx, mcpsrList, client.InNamespace(secret.Namespace)); err != nil {
		log.Error(err, "Failed to list MCPServerRegistrations")
		return nil
	}
	log.Info("findMCPServerRegistrationsForSecret", "total mcpserverregistrations", len(mcpsrList.Items))
	var requests []reconcile.Request
	for _, mcpsr := range mcpsrList.Items {
		// check if references this secret
		if mcpsr.Spec.CredentialRef != nil && mcpsr.Spec.CredentialRef.Name == secret.Name {
			log.Info("findMCPServerRegistrationsForSecret", "requeue", mcpsr.Name)
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      mcpsr.Name,
					Namespace: mcpsr.Namespace,
				},
			})
		}
	}

	// mcpvirtualservers don't have credentials

	return requests
}

// findMCPServerRegistrationsForMCPGatewayExtension finds all MCPServerRegistrations whose HTTPRoutes
// are attached to the Gateway targeted by the given MCPGatewayExtension. When an MCPGatewayExtension
// changes (created, updated, deleted), the associated MCPServerRegistrations need to be reconciled
// to ensure their config is written to the correct namespaces.
func (r *MCPReconciler) findMCPServerRegistrationsForMCPGatewayExtension(ctx context.Context, obj client.Object) []reconcile.Request {
	mcpExt := obj.(*mcpv1alpha1.MCPGatewayExtension)
	logger := logf.FromContext(ctx).WithValues("MCPGatewayExtension", mcpExt.Name, "namespace", mcpExt.Namespace)

	// get the gateway this extension targets
	gatewayNamespace := mcpExt.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = mcpExt.Namespace
	}
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      mcpExt.Spec.TargetRef.Name,
		Namespace: gatewayNamespace,
	}, gateway); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get Gateway for MCPGatewayExtension")
		}
		return nil
	}

	// find all HTTPRoutes that have this gateway as a parent
	httpRouteList := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, httpRouteList); err != nil {
		logger.Error(err, "Failed to list HTTPRoutes")
		return nil
	}

	var requests []reconcile.Request
	for _, httpRoute := range httpRouteList.Items {
		// check if this HTTPRoute references the gateway
		for _, parentRef := range httpRoute.Spec.ParentRefs {
			parentNs := httpRoute.Namespace
			if parentRef.Namespace != nil {
				parentNs = string(*parentRef.Namespace)
			}
			if string(parentRef.Name) == gateway.Name && parentNs == gateway.Namespace {
				// find MCPServerRegistrations targeting this HTTPRoute
				indexKey := httpRouteIndexValue(httpRoute.Namespace, httpRoute.Name)
				mcpsrList := &mcpv1alpha1.MCPServerRegistrationList{}
				if err := r.List(ctx, mcpsrList, client.MatchingFields{HTTPRouteIndex: indexKey}); err != nil {
					logger.Error(err, "Failed to list MCPServerRegistrations for HTTPRoute",
						"HTTPRoute", httpRoute.Name, "namespace", httpRoute.Namespace)
					continue
				}
				for _, mcpsr := range mcpsrList.Items {
					logger.V(1).Info("Enqueueing MCPServerRegistration due to MCPGatewayExtension change",
						"MCPServerRegistration", mcpsr.Name, "namespace", mcpsr.Namespace)
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      mcpsr.Name,
							Namespace: mcpsr.Namespace,
						},
					})
				}
				break
			}
		}
	}

	logger.V(1).Info("Found MCPServerRegistrations for MCPGatewayExtension", "count", len(requests))
	return requests
}
