package controller

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kagenti/mcp-gateway/internal/broker/upstream"
	mcpv1alpha1 "github.com/kagenti/mcp-gateway/pkg/apis/mcp/v1alpha1"
	"github.com/kagenti/mcp-gateway/pkg/config"
)

const (
	// ConfigName is the name of the config
	ConfigName = "mcp-gateway-config"

	// SecretManagedByLabel is the label for managed secrets
	SecretManagedByLabel = "mcp.kagenti.com/managed-by" //nolint:gosec // not a credential, just a label name
	// SecretManagedByValue is the value for managed secrets
	SecretManagedByValue = "mcp-gateway"

	// CredentialSecretLabel is the required label for credential secrets
	CredentialSecretLabel = "mcp.kagenti.com/credential" //nolint:gosec // not a credential, just a label name
	// CredentialSecretValue is the required value for credential secrets
	CredentialSecretValue = "true"
)

// getConfigNamespace returns the namespace for config, using NAMESPACE env var or defaulting to mcp-system
func getConfigNamespace() string {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "mcp-system"
	}
	return namespace
}

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

// MCPReconciler reconciles both MCPServer and MCPVirtualServer resources
type MCPReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader // uncached reader for fetching secrets
}

// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpvirtualservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// Reconcile reconciles both MCPServer and MCPVirtualServer resources
func (r *MCPReconciler) Reconcile(
	ctx context.Context,
	req reconcile.Request,
) (reconcile.Result, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Reconciling MCP resource", "name", req.Name, "namespace", req.Namespace)

	// Try MCPServer first
	mcpServer := &mcpv1alpha1.MCPServer{}
	err := r.Get(ctx, req.NamespacedName, mcpServer)
	if err == nil {
		return r.reconcileMCPServer(ctx, mcpServer)
	}
	if !errors.IsNotFound(err) {
		log.Error(err, "Failed to get MCPServer")
		return reconcile.Result{}, err
	}

	// Try MCPVirtualServer
	mcpVirtualServer := &mcpv1alpha1.MCPVirtualServer{}
	err = r.Get(ctx, req.NamespacedName, mcpVirtualServer)
	if err == nil {
		return r.reconcileMCPVirtualServer(ctx, mcpVirtualServer)
	}
	if !errors.IsNotFound(err) {
		log.Error(err, "Failed to get MCPVirtualServer")
		return reconcile.Result{}, err
	}

	// Neither resource found, regenerate config (handles deletions)
	log.Info("MCP resource not found, regenerating aggregated config",
		"req.NamespacedName", req.NamespacedName)
	return r.regenerateAggregatedConfig(ctx)
}

// reconcileMCPServer handles MCPServer reconciliation (existing logic)
func (r *MCPReconciler) reconcileMCPServer(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
) (reconcile.Result, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Reconciling MCPServer", "name", mcpServer.Name, "namespace", mcpServer.Namespace)

	// validate credential secret if configured
	if mcpServer.Spec.CredentialRef != nil {
		if err := r.validateCredentialSecret(ctx, mcpServer); err != nil {
			log.Error(err, "Credential validation failed")
			// still regenerate config to ensure other servers work
			if _, configErr := r.regenerateAggregatedConfig(ctx); configErr != nil {
				log.Error(configErr, "Failed to regenerate config after credential validation error")
			}
			return reconcile.Result{}, r.updateStatus(ctx, mcpServer, false, fmt.Sprintf("Credential validation failed: %v", err), 0)
		}
		log.V(1).Info("Credential validation success ", "credential ref", mcpServer.Spec.CredentialRef)
	}

	serverInfo, err := r.discoverServersFromHTTPRoutes(ctx, mcpServer)
	if err != nil {
		log.Error(err, "Failed to discover servers from HTTPRoutes")
		// still regenerate config to ensure credentials are aggregated
		if _, configErr := r.regenerateAggregatedConfig(ctx); configErr != nil {
			log.Error(configErr, "Failed to regenerate config after discovery error")
		}
		return reconcile.Result{}, r.updateStatus(ctx, mcpServer, false, err.Error(), 0)
	}

	validator := NewServerValidator(r.Client)
	statusResponse, err := validator.ValidateServers(ctx)
	if err != nil {
		log.Error(err, "Failed to validate server status via broker")
		ready, message := false, fmt.Sprintf("Validation failed: %v", err)
		if err := r.updateStatus(ctx, mcpServer, ready, message, 0); err != nil {
			log.Error(err, "Failed to update status")
			return reconcile.Result{}, err
		}
		return r.regenerateAggregatedConfig(ctx)
	}

	var serverStatus upstream.ServerValidationStatus
	for _, sr := range statusResponse.Servers {
		if sr.ID == serverInfo.ID {
			serverStatus = sr
			break
		}
	}

	if err := r.updateStatus(ctx, mcpServer, serverStatus.Ready, serverStatus.Message, serverStatus.TotalTools); err != nil {
		log.Error(err, "Failed to update status")
		return reconcile.Result{}, err
	}

	if err := r.updateHTTPRouteStatus(ctx, mcpServer, true); err != nil {
		log.Error(err, "Failed to update HTTPRoute status")
	}

	// this handles the case where secret volume mounts take time to propagate (60-120s)
	// retry for any failure, not just "no broker validation data yet", as credentials might not be available initially
	if !serverStatus.Ready {
		// calculate exponential backoff based on elapsed time since condition was set
		baseDelay := 5 * time.Second
		maxDelay := 60 * time.Second
		factor := 2.0

		// find when the Ready condition was last set to estimate retry count
		var lastTransition time.Time
		for _, cond := range mcpServer.Status.Conditions {
			if cond.Type == "Ready" {
				lastTransition = cond.LastTransitionTime.Time
				break
			}
		}

		// calculate how many retries based on elapsed time
		// this avoids modifying the resource and triggering reconciliation loops
		elapsed := time.Since(lastTransition)
		estimatedRetryCount := 0
		totalTime := time.Duration(0)
		delay := baseDelay

		for totalTime < elapsed {
			totalTime += delay
			if totalTime < elapsed {
				estimatedRetryCount++
				delay = time.Duration(float64(delay) * factor)
				if delay > maxDelay {
					delay = maxDelay
				}
			}
		}

		// calculate next retry delay
		retryAfter := baseDelay
		for i := 0; i < estimatedRetryCount && retryAfter < maxDelay; i++ {
			retryAfter = time.Duration(float64(retryAfter) * factor)
		}
		if retryAfter > maxDelay {
			retryAfter = maxDelay
		}

		log.V(1).Info("Retrying MCPServer with credentials pending validation",
			"server", mcpServer.Name,
			"retryAfter", retryAfter,
			"estimatedRetryCount", estimatedRetryCount)

		// Still regenerate config so broker can see the server
		_, configErr := r.regenerateAggregatedConfig(ctx)
		if configErr != nil {
			log.Error(configErr, "Failed to regenerate config while waiting for credential validation")
		}

		return reconcile.Result{RequeueAfter: retryAfter}, nil
	}

	return r.regenerateAggregatedConfig(ctx)
}

// reconcileMCPVirtualServer handles MCPVirtualServer reconciliation
func (r *MCPReconciler) reconcileMCPVirtualServer(
	ctx context.Context,
	mcpVirtualServer *mcpv1alpha1.MCPVirtualServer,
) (reconcile.Result, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Reconciling MCPVirtualServer", "name", mcpVirtualServer.Name, "namespace", mcpVirtualServer.Namespace)

	// For now, just trigger config regeneration like the existing logic
	// This keeps the same behavior as before
	return r.regenerateAggregatedConfig(ctx)
}

func (r *MCPReconciler) regenerateAggregatedConfig(
	ctx context.Context,
) (reconcile.Result, error) {
	log := log.FromContext(ctx)

	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList); err != nil {
		log.Error(err, "Failed to list MCPServers")
		return reconcile.Result{}, err
	}

	mcpVirtualServerList := &mcpv1alpha1.MCPVirtualServerList{}
	if err := r.List(ctx, mcpVirtualServerList); err != nil {
		log.Error(err, "Failed to list MCPVirtualServers")
		return reconcile.Result{}, err
	}

	referencedHTTPRoutes := make(map[string]struct{})
	for _, mcpServer := range mcpServerList.Items {
		targetRef := mcpServer.Spec.TargetRef
		if targetRef.Kind == "HTTPRoute" {
			namespace := mcpServer.Namespace
			if targetRef.Namespace != "" {
				namespace = targetRef.Namespace
			}
			key := fmt.Sprintf("%s/%s", namespace, targetRef.Name)
			referencedHTTPRoutes[key] = struct{}{}
		}
	}

	if len(mcpServerList.Items) == 0 {
		log.Info("No MCPServers found, writing empty ConfigMap")
		emptyConfig := &config.BrokerConfig{
			Servers: []config.ServerConfig{},
		}
		if err := r.writeAggregatedConfig(ctx, emptyConfig); err != nil {
			log.Error(err, "Failed to write empty configuration")
			return reconcile.Result{}, err
		}
		log.Info("Successfully wrote empty ConfigMap")
		return reconcile.Result{}, nil
	}

	brokerConfig := &config.BrokerConfig{
		Servers:        []config.ServerConfig{},
		VirtualServers: []config.VirtualServerConfig{},
	}

	for _, mcpServer := range mcpServerList.Items {

		serverInfo, err := r.discoverServersFromHTTPRoutes(ctx, &mcpServer)
		if err != nil {
			log.Error(err, "Failed to discover server endpoints",
				"name", mcpServer.Name,
				"namespace", mcpServer.Namespace)
			continue
		}

		serverName := fmt.Sprintf(
			"%s/%s",
			serverInfo.HTTPRouteNamespace,
			serverInfo.HTTPRouteName,
		)
		serverConfig := config.ServerConfig{
			Name:       serverName,
			URL:        serverInfo.Endpoint,
			Hostname:   serverInfo.Hostname,
			ToolPrefix: serverInfo.ToolPrefix,
			Enabled:    true,
		}

		// add credential env var if configured
		if mcpServer.Spec.CredentialRef != nil {
			secret := &corev1.Secret{}
			err = r.Get(ctx, types.NamespacedName{
				Name:      mcpServer.Spec.CredentialRef.Name,
				Namespace: mcpServer.Namespace,
			}, secret)
			if err != nil {
				log.Error(err, "failed to read credential secret")
				continue
			}
			val, ok := secret.Data[mcpServer.Spec.CredentialRef.Key]
			if !ok {
				// no key log and continue
				log.V(1).Info("the secret had no key ", "specified key", mcpServer.Spec.CredentialRef.Key)
				continue
			}
			serverConfig.Credential = string(val)

		}

		brokerConfig.Servers = append(brokerConfig.Servers, serverConfig)

	}

	// Process MCPVirtualServer resources
	for _, mcpVirtualServer := range mcpVirtualServerList.Items {
		virtualServerName := fmt.Sprintf("%s/%s", mcpVirtualServer.Namespace, mcpVirtualServer.Name)
		brokerConfig.VirtualServers = append(brokerConfig.VirtualServers, config.VirtualServerConfig{
			Name:  virtualServerName,
			Tools: mcpVirtualServer.Spec.Tools,
		})
	}

	if err := r.writeAggregatedConfig(ctx, brokerConfig); err != nil {
		log.Error(err, "Failed to write aggregated configuration")
		return reconcile.Result{}, err
	}

	log.V(1).Info("Successfully regenerated aggregated configuration",
		"serverCount", len(brokerConfig.Servers),
		"virtualServerCount", len(brokerConfig.VirtualServers))

	if err := r.cleanupOrphanedHTTPRoutes(ctx, referencedHTTPRoutes); err != nil {
		log.Error(err, "Failed to cleanup orphaned HTTPRoute conditions")
	}

	return reconcile.Result{}, nil
}

func (r *MCPReconciler) writeAggregatedConfig(
	ctx context.Context,
	brokerConfig *config.BrokerConfig,
) error {
	writer := NewSecretWriter(r.Client, r.Scheme)
	return writer.WriteAggregatedConfig(ctx, getConfigNamespace(), ConfigName, brokerConfig)
}

func (r *MCPReconciler) discoverServersFromHTTPRoutes(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
) (*ServerInfo, error) {

	targetRef := mcpServer.Spec.TargetRef

	// Validate group and kind
	if targetRef.Group != "gateway.networking.k8s.io" {
		return nil, fmt.Errorf(
			"invalid targetRef group %q: only gateway.networking.k8s.io is supported",
			targetRef.Group,
		)
	}
	if targetRef.Kind != "HTTPRoute" {
		return nil, fmt.Errorf(
			"invalid targetRef kind %q: only HTTPRoute is supported",
			targetRef.Kind,
		)
	}

	namespace := mcpServer.Namespace
	if targetRef.Namespace != "" && targetRef.Namespace != namespace {
		return nil, fmt.Errorf(
			"cross-namespace reference to %s/%s not allowed without ReferenceGrant support",
			targetRef.Namespace,
			targetRef.Name,
		)
	}

	httpRoute := &gatewayv1.HTTPRoute{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      targetRef.Name,
		Namespace: namespace,
	}, httpRoute)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("HTTPRoute %s/%s not found", namespace, targetRef.Name)
		}
		return nil, fmt.Errorf(
			"failed to get HTTPRoute %s/%s: %w",
			namespace,
			targetRef.Name,
			err,
		)
	}

	if len(httpRoute.Spec.Rules) == 0 || len(httpRoute.Spec.Rules[0].BackendRefs) == 0 {
		return nil, fmt.Errorf(
			"HTTPRoute %s/%s has no backend references",
			namespace,
			targetRef.Name,
		)
	}

	backendRef := httpRoute.Spec.Rules[0].BackendRefs[0]
	if backendRef.Name == "" {
		return nil, fmt.Errorf("backend reference has no name")
	}

	kind := "Service"
	if backendRef.Kind != nil {
		kind = string(*backendRef.Kind)
	}

	group := ""
	if backendRef.Group != nil {
		group = string(*backendRef.Group)
	}

	// handle Istio Hostname backendRef for external services
	if kind == "Hostname" && group == "networking.istio.io" {
		return r.buildServerInfoForHostnameBackend(httpRoute, mcpServer, backendRef, namespace, targetRef.Name)
	}

	if kind != "Service" {
		return nil, fmt.Errorf("backend reference is not a Service: %s", kind)
	}

	// Determine service namespace, default to HTTPRoute namespace
	serviceNamespace := httpRoute.Namespace
	if backendRef.Namespace != nil {
		serviceNamespace = string(*backendRef.Namespace)
	}

	// check service type
	backendName := string(backendRef.Name)
	var nameAndEndpoint string
	isExternal := false

	service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      backendName,
		Namespace: serviceNamespace,
	}, service)

	if err != nil {
		return nil, fmt.Errorf("failed to get service %s: %w", backendName, err)
	}

	// Extract hostname from HTTPRoute
	if len(httpRoute.Spec.Hostnames) == 0 {
		return nil, fmt.Errorf(
			"HTTPRoute %s/%s must have at least one hostname for MCP backend routing",
			namespace,
			targetRef.Name,
		)
	}
	// use first hostname if multiple are present
	hostname := string(httpRoute.Spec.Hostnames[0])

	if service.Spec.Type == corev1.ServiceTypeExternalName {
		// externalname service points to external host
		isExternal = true
		externalName := service.Spec.ExternalName
		if backendRef.Port != nil {
			nameAndEndpoint = fmt.Sprintf("%s:%d", externalName, *backendRef.Port)
		} else {
			nameAndEndpoint = externalName
		}
	} else {
		// regular k8s service
		serviceDNSName := fmt.Sprintf("%s.%s.svc.cluster.local", backendRef.Name, serviceNamespace)
		if backendRef.Port != nil {
			nameAndEndpoint = fmt.Sprintf("%s:%d", serviceDNSName, *backendRef.Port)
		} else {
			nameAndEndpoint = serviceDNSName
		}
	}

	toolPrefix := mcpServer.Spec.ToolPrefix

	protocol := "http"
	if httpRoute.Spec.ParentRefs != nil {
		for _, parentRef := range httpRoute.Spec.ParentRefs {
			if parentRef.SectionName != nil &&
				strings.Contains(string(*parentRef.SectionName), "https") {
				protocol = "https"
				break
			}
		}
	}

	// determine protocol for external services
	if isExternal {
		// use appProtocol from Service spec (standard k8s field)
		for _, port := range service.Spec.Ports {
			if backendRef.Port != nil && port.Port == *backendRef.Port {
				if port.AppProtocol != nil && strings.ToLower(*port.AppProtocol) == "https" {
					protocol = "https"
				}
				break
			}
		}
	}

	path := mcpServer.Spec.Path
	endpoint := fmt.Sprintf("%s://%s%s", protocol, nameAndEndpoint, path)

	routingHostname := hostname
	if isExternal {
		// extract hostname without port
		if idx := strings.LastIndex(nameAndEndpoint, ":"); idx != -1 {
			routingHostname = nameAndEndpoint[:idx]
		} else {
			routingHostname = nameAndEndpoint
		}
	}
	id := serverID(httpRoute, mcpServer, hostname)
	serverInfo := ServerInfo{
		ID:                 id,
		Endpoint:           endpoint,
		Hostname:           routingHostname,
		ToolPrefix:         toolPrefix,
		HTTPRouteName:      targetRef.Name,
		HTTPRouteNamespace: namespace,
		Credential:         "",
	}
	return &serverInfo, nil
}

// buildServerInfoForHostnameBackend handles Istio Hostname backendRef for external services
func (r *MCPReconciler) buildServerInfoForHostnameBackend(
	httpRoute *gatewayv1.HTTPRoute,
	mcpServer *mcpv1alpha1.MCPServer,
	backendRef gatewayv1.HTTPBackendRef,
	namespace string,
	httpRouteName string,
) (*ServerInfo, error) {
	if len(httpRoute.Spec.Hostnames) == 0 {
		return nil, fmt.Errorf("HTTPRoute %s/%s must have at least one hostname", namespace, httpRouteName)
	}

	externalHost := string(backendRef.Name)
	if !isValidHostname(externalHost) {
		return nil, fmt.Errorf("invalid hostname in backendRef: %s", externalHost)
	}

	port := "443"
	if backendRef.Port != nil {
		port = fmt.Sprintf("%d", *backendRef.Port)
	}

	path := mcpServer.Spec.Path
	endpoint := fmt.Sprintf("https://%s%s", net.JoinHostPort(externalHost, port), path)
	routingHostname := string(httpRoute.Spec.Hostnames[0])

	return &ServerInfo{
		ID:                 serverID(httpRoute, mcpServer, endpoint),
		Endpoint:           endpoint,
		Hostname:           routingHostname,
		ToolPrefix:         mcpServer.Spec.ToolPrefix,
		HTTPRouteName:      httpRouteName,
		HTTPRouteNamespace: namespace,
		Credential:         "",
	}, nil
}

// validates hostname can't contain path injection
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

func (r *MCPReconciler) cleanupOrphanedHTTPRoutes(
	ctx context.Context,
	referencedHTTPRoutes map[string]struct{},
) error {
	log := log.FromContext(ctx)

	httpRouteList := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, httpRouteList, client.MatchingFields{"status.hasProgrammedCondition": "true"}); err != nil {
		return fmt.Errorf("failed to list programmed HTTPRoutes: %w", err)
	}

	for _, httpRoute := range httpRouteList.Items {
		key := fmt.Sprintf("%s/%s", httpRoute.Namespace, httpRoute.Name)

		if _, referenced := referencedHTTPRoutes[key]; referenced {
			continue
		}

		hasProgrammedCondition := false
		updateNeeded := false
		for i, parentStatus := range httpRoute.Status.Parents {
			newConditions := []metav1.Condition{}
			for _, condition := range parentStatus.Conditions {
				if condition.Type == "Programmed" && condition.Status == metav1.ConditionTrue {
					hasProgrammedCondition = true
					updateNeeded = true
				} else {
					newConditions = append(newConditions, condition)
				}
			}
			if updateNeeded {
				httpRoute.Status.Parents[i].Conditions = newConditions
			}
		}

		if hasProgrammedCondition {
			log.Info("Cleaning up Programmed condition on orphaned HTTPRoute",
				"HTTPRoute", httpRoute.Name,
				"namespace", httpRoute.Namespace)

			if err := r.Status().Update(ctx, &httpRoute); err != nil {
				log.Error(err, "Failed to cleanup HTTPRoute status",
					"HTTPRoute", httpRoute.Name,
					"namespace", httpRoute.Namespace)
			}
		}
	}

	return nil
}

func (r *MCPReconciler) updateHTTPRouteStatus(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	affected bool,
) error {
	log := log.FromContext(ctx)
	targetRef := mcpServer.Spec.TargetRef

	if targetRef.Kind != "HTTPRoute" {
		return nil
	}

	namespace := mcpServer.Namespace
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

	if affected {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "InUseByMCPServer"
		// We don't include the MCP Server in the status because >1 MCPServer may reference the same HTTPRoute
		condition.Message = "HTTPRoute is referenced by at least one MCPServer"
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "NotInUse"
		condition.Message = "HTTPRoute is not referenced by any MCPServer"
	}

	found := false
	for i, cond := range httpRoute.Status.Parents {
		conditionFound := false
		for j, c := range cond.Conditions {
			if c.Type == condition.Type {
				httpRoute.Status.Parents[i].Conditions[j] = condition
				conditionFound = true
				break
			}
		}
		if !conditionFound {
			httpRoute.Status.Parents[i].Conditions = append(
				httpRoute.Status.Parents[i].Conditions,
				condition,
			)
		}
		found = true
	}

	if !found {
		log.Info("HTTPRoute has no parent statuses, skipping condition update",
			"HTTPRoute", httpRoute.Name,
			"namespace", httpRoute.Namespace)
		return nil
	}

	if err := r.Status().Update(ctx, httpRoute); err != nil {
		return fmt.Errorf("failed to update HTTPRoute status: %w", err)
	}

	log.V(1).Info("Updated HTTPRoute status",
		"HTTPRoute", httpRoute.Name,
		"namespace", httpRoute.Namespace,
		"affected", affected)

	return nil
}

func serverID(httpRoute *gatewayv1.HTTPRoute, mcpServer *mcpv1alpha1.MCPServer, endpoint string) string {
	return fmt.Sprintf("%s:%s:%s", fmt.Sprintf("%s/%s", httpRoute.Namespace, httpRoute.Name), mcpServer.Spec.ToolPrefix, endpoint)
}

func (r *MCPReconciler) updateStatus(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
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
	for i, cond := range mcpServer.Status.Conditions {
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
			mcpServer.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		mcpServer.Status.Conditions = append(mcpServer.Status.Conditions, condition)
		statusChanged = true
	}
	if mcpServer.Status.DiscoveredTools != toolCount {
		mcpServer.Status.DiscoveredTools = toolCount
		statusChanged = true
	}

	// only update if something actually changed
	if !statusChanged {
		return nil
	}

	return r.Status().Update(ctx, mcpServer)
}

// SetupWithManager sets up the reconciler
func (r *MCPReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &mcpv1alpha1.MCPServer{}, "spec.targetRef.httproute", func(rawObj client.Object) []string {
		mcpServer := rawObj.(*mcpv1alpha1.MCPServer)

		targetRef := mcpServer.Spec.TargetRef
		if targetRef.Kind == "HTTPRoute" {
			namespace := targetRef.Namespace
			if namespace == "" {
				namespace = mcpServer.Namespace
			}
			return []string{fmt.Sprintf("%s/%s", namespace, targetRef.Name)}
		}
		return []string{}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &gatewayv1.HTTPRoute{}, "status.hasProgrammedCondition", func(rawObj client.Object) []string {
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

	controller := ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServer{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&mcpv1alpha1.MCPVirtualServer{},
			&handler.EnqueueRequestForObject{},
		).
		Watches(
			&gatewayv1.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findMCPServersForHTTPRoute),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findMCPServersForSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// only watch secrets with the credential label
				secret := obj.(*corev1.Secret)
				return secret.Labels != nil && secret.Labels[CredentialSecretLabel] == CredentialSecretValue
			})),
		)

	// Perform startup reconciliation to ensure config exists even with zero MCPServers
	if err := mgr.Add(&startupReconciler{reconciler: r}); err != nil {
		return err
	}

	return controller.Complete(r)
}

// findMCPServersForHTTPRoute finds all MCPServers that reference the given HTTPRoute
func (r *MCPReconciler) findMCPServersForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	httpRoute := obj.(*gatewayv1.HTTPRoute)
	log := log.FromContext(ctx).WithValues("HTTPRoute", httpRoute.Name, "namespace", httpRoute.Namespace)

	indexKey := fmt.Sprintf("%s/%s", httpRoute.Namespace, httpRoute.Name)
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList, client.MatchingFields{"spec.targetRef.httproute": indexKey}); err != nil {
		log.Error(err, "Failed to list MCPServers using index")
		return nil
	}

	var requests []reconcile.Request
	for _, mcpServer := range mcpServerList.Items {
		log.V(1).Info("Found MCPServer referencing HTTPRoute via index",
			"MCPServer", mcpServer.Name,
			"MCPServerNamespace", mcpServer.Namespace)
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      mcpServer.Name,
				Namespace: mcpServer.Namespace,
			},
		})
	}

	return requests
}

// validates credential secret has required label
func (r *MCPReconciler) validateCredentialSecret(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	if mcpServer.Spec.CredentialRef == nil {
		return nil // no credentials to validate
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.CredentialRef.Name,
		Namespace: mcpServer.Namespace,
	}, secret)
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("credential secret %s not found", mcpServer.Spec.CredentialRef.Name)
		}
		return fmt.Errorf("failed to get credential secret: %w", err)
	}

	// check for required label
	if secret.Labels == nil || secret.Labels[CredentialSecretLabel] != CredentialSecretValue {
		return fmt.Errorf("credential secret %s is missing required label %s=%s",
			mcpServer.Spec.CredentialRef.Name, CredentialSecretLabel, CredentialSecretValue)
	}

	// validate key exists
	key := mcpServer.Spec.CredentialRef.Key
	if key == "" {
		key = "token" // default
	}
	if _, exists := secret.Data[key]; !exists {
		return fmt.Errorf("credential secret %s is missing key %s",
			mcpServer.Spec.CredentialRef.Name, key)
	}

	return nil
}

// finds mcpservers referencing the given secret
func (r *MCPReconciler) findMCPServersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	log := log.FromContext(ctx).WithValues("Secret", secret.Name, "namespace", secret.Namespace)

	// list mcpservers in same namespace
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList, client.InNamespace(secret.Namespace)); err != nil {
		log.Error(err, "Failed to list MCPServers")
		return nil
	}

	var requests []reconcile.Request
	for _, mcpServer := range mcpServerList.Items {
		// check if references this secret
		if mcpServer.Spec.CredentialRef != nil && mcpServer.Spec.CredentialRef.Name == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      mcpServer.Name,
					Namespace: mcpServer.Namespace,
				},
			})
		}
	}

	// mcpvirtualservers don't have credentials

	return requests
}

// startupReconciler ensures initial configuration is written even with zero MCPServers
type startupReconciler struct {
	reconciler *MCPReconciler
}

// Start implements manager.Runnable
func (s *startupReconciler) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("startup-reconciler")
	log.Info("Running startup reconciliation to ensure config exists")

	if _, err := s.reconciler.regenerateAggregatedConfig(ctx); err != nil {
		log.Error(err, "Failed to run startup reconciliation")
		return err
	}

	log.Info("Startup reconciliation completed successfully")
	return nil
}
