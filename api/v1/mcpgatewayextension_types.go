package v1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HTTPRouteManagementPolicy defines how the operator manages the gateway HTTPRoute
// +kubebuilder:validation:Enum=Enabled;Disabled
type HTTPRouteManagementPolicy string

// KeyGenerationPolicy defines whether the operator generates an ECDSA P-256 key pair
// +kubebuilder:validation:Enum=Enabled;Disabled
type KeyGenerationPolicy string

// URLElicitationPolicy controls whether URL-based token elicitation is enabled
// +kubebuilder:validation:Enum=Enabled;Disabled
type URLElicitationPolicy string

// LogLevel controls the broker-router log verbosity. It maps to the broker's
// --log-level flag using Go's slog level convention: debug=-4, info=0, warn=4, error=8.
// +kubebuilder:validation:Enum=debug;info;warn;error
type LogLevel string

const (
	// ConditionTypeReady signals if a resource is ready
	ConditionTypeReady = "Ready"
	// ConditionReasonSuccess is the success reason users see
	ConditionReasonSuccess = "ValidMCPGatewayExtension"
	// ConditionReasonInvalid is the reason seen when invalid configuration occurs
	ConditionReasonInvalid = "InvalidMCPGatewayExtension"
	// ConditionReasonRefGrantRequired is the reason users will see when a ReferenceGrant is missing
	ConditionReasonRefGrantRequired = "ReferenceGrantRequired"
	// ConditionReasonDeploymentNotReady is the reason when the broker-router deployment is not ready
	ConditionReasonDeploymentNotReady = "DeploymentNotReady"

	// ConditionReasonSecretNotFound is the reason when the trusted headers secret is missing
	ConditionReasonSecretNotFound = "SecretNotFound"
	// ConditionReasonSecretInvalid is the reason when the secret lacks the required key
	ConditionReasonSecretInvalid = "SecretInvalid"
	// HTTPRouteManagementEnabled means the operator creates and manages the HTTPRoute
	HTTPRouteManagementEnabled HTTPRouteManagementPolicy = "Enabled"
	// HTTPRouteManagementDisabled means the operator does not create an HTTPRoute
	HTTPRouteManagementDisabled HTTPRouteManagementPolicy = "Disabled"

	// KeyGenerationEnabled means the operator generates an ECDSA P-256 key pair
	KeyGenerationEnabled KeyGenerationPolicy = "Enabled"
	// KeyGenerationDisabled means the operator does not generate keys
	KeyGenerationDisabled KeyGenerationPolicy = "Disabled"

	// URLElicitationEnabled enables URL-based token elicitation and creates a /tokens HTTPRoute
	URLElicitationEnabled URLElicitationPolicy = "Enabled"
	// URLElicitationDisabled disables URL-based token elicitation (default)
	URLElicitationDisabled URLElicitationPolicy = "Disabled"

	// LogLevelDebug sets the broker-router --log-level flag to -4
	LogLevelDebug LogLevel = "debug"
	// LogLevelInfo sets the broker-router --log-level flag to 0
	LogLevelInfo LogLevel = "info"
	// LogLevelWarn sets the broker-router --log-level flag to 4
	LogLevelWarn LogLevel = "warn"
	// LogLevelError sets the broker-router --log-level flag to 8
	LogLevelError LogLevel = "error"
)

// MCPGatewayExtensionSpec defines the desired state of MCPGatewayExtension.
type MCPGatewayExtensionSpec struct {
	// targetRef specifies the Gateway to extend with MCP protocol support.
	// The controller will create an EnvoyFilter targeting this Gateway's Envoy proxy.
	// +required
	TargetRef MCPGatewayExtensionTargetReference `json:"targetRef,omitzero"`

	// publicHost overrides the public host derived from the listener hostname.
	// Use when the listener has a wildcard and you need a specific host.
	// +optional
	PublicHost string `json:"publicHost,omitempty"`

	// privateHost overrides the internal host used for hair-pinning requests
	// back through the gateway. Defaults to
	// <gateway>-<gatewayClassName>.<ns>.svc.cluster.local:<port>, with an https://
	// scheme prefix when the targeted Gateway listener uses the HTTPS protocol. The
	// value supplied here is honoured verbatim, so an operator can include a
	// scheme (e.g. "https://my-gw:443") or pin to a different port.
	// +optional
	PrivateHost string `json:"privateHost,omitempty"`

	// backendPingIntervalSeconds specifies how often the broker pings upstream MCP servers.
	// +optional
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=7200
	// +default=60
	BackendPingIntervalSeconds *int32 `json:"backendPingIntervalSeconds,omitempty"`

	// trustedHeadersKey configures trusted-header key pair for JWT-based tool filtering.
	// When set, the public key secret is wired into the broker deployment.
	// +optional
	TrustedHeadersKey *TrustedHeadersKey `json:"trustedHeadersKey,omitempty"`

	// httpRouteManagement controls whether the operator manages the gateway HTTPRoute.
	// Enabled: creates and manages the HTTPRoute (default).
	// Disabled: does not create an HTTPRoute.
	// +optional
	// +default="Enabled"
	HTTPRouteManagement HTTPRouteManagementPolicy `json:"httpRouteManagement,omitempty"`

	// logLevel controls the broker-router log verbosity.
	// Maps to the broker's --log-level flag: debug=-4, info=0, warn=4, error=8.
	// When unset, the operator-wide BROKER_ROUTER_LOG_LEVEL default (if configured) is used.
	// +optional
	LogLevel LogLevel `json:"logLevel,omitempty"`

	// sessionStore references a secret for redis-based session storage.
	// The secret must exist in the MCPGatewayExtension namespace and contain a CACHE_CONNECTION_STRING key.
	// The value is injected as CACHE_CONNECTION_STRING into the broker-router deployment.
	// When not set, in-memory session storage is used.
	// +optional
	SessionStore *SessionStore `json:"sessionStore,omitempty"`

	// urlElicitation controls whether URL-based token elicitation is enabled.
	// Enabled: creates a separate /tokens HTTPRoute and passes --enable-url-elicitation to the broker.
	// Disabled: no /tokens route is created (default).
	// +optional
	// +default="Disabled"
	URLElicitation URLElicitationPolicy `json:"urlElicitation,omitempty"`

	// oauthProtectedResource configures the OAuth protected resource metadata
	// served at /.well-known/oauth-protected-resource. When set, the controller
	// injects the corresponding OAUTH_* env vars into the broker-router deployment.
	// +optional
	OAuthProtectedResource *OAuthProtectedResource `json:"oauthProtectedResource,omitempty"`

	// caCertBundleRef references a Secret containing a PEM-encoded CA certificate
	// bundle used as the base trust pool for all upstream MCP server connections.
	// Per-server caCertSecretRef on MCPServerRegistration appends to this pool.
	// The Secret must have the label mcp.kuadrant.io/secret=true.
	// +optional
	CACertBundleRef *CACertBundleReference `json:"caCertBundleRef,omitempty"`
}

// OAuthProtectedResource configures the OAuth protected resource metadata
// served at /.well-known/oauth-protected-resource.
type OAuthProtectedResource struct {
	// authorizationServers lists the OAuth authorization server URLs.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:Pattern=`^https?://[^,]+$`
	// +listType=atomic
	AuthorizationServers []string `json:"authorizationServers,omitempty"`

	// resourceName is the human-readable name for this resource.
	// Defaults to "MCP Server".
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ResourceName string `json:"resourceName,omitempty"`

	// resource is the URI of the protected resource.
	// Defaults to https://<publicHost>/mcp.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Resource string `json:"resource,omitempty"`

	// bearerMethodsSupported lists the supported bearer token methods.
	// Defaults to ["header"].
	// +optional
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	BearerMethodsSupported []string `json:"bearerMethodsSupported,omitempty"`

	// scopesSupported lists the supported OAuth scopes.
	// Defaults to ["basic"].
	// +optional
	// +kubebuilder:validation:MaxItems=50
	// +listType=atomic
	ScopesSupported []string `json:"scopesSupported,omitempty"`
}

// CACertBundleReference identifies a Secret containing a PEM-encoded CA bundle.
type CACertBundleReference struct {
	// name is the name of the Secret resource.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"`

	// key is the key within the Secret that contains the CA bundle PEM data.
	// If not specified, defaults to "ca.crt".
	// +optional
	// +default="ca.crt"
	Key string `json:"key,omitempty"`
}

// SessionStore references a secret containing a redis connection string for session storage.
type SessionStore struct {
	// secretName is the name of the secret containing the CACHE_CONNECTION_STRING key.
	// The value should be a redis connection string: redis://<user>:<pass>@<host>:<port>/<db>
	// +required
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName,omitempty"`
}

// TrustedHeadersKey configures trusted-header key pair for JWT-based tool filtering.
// When configured, the public key is injected into the broker deployment via the
// TRUSTED_HEADER_PUBLIC_KEY env var.
type TrustedHeadersKey struct {
	// secretName is the name of the secret containing the public key used by the broker
	// to verify trusted-header JWTs. The secret must have a data entry with key "key"
	// containing the PEM-encoded public key.
	// When Generate is Enabled, the operator creates this secret.
	// When Generate is Disabled, this secret must already exist in the namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName,omitempty"`

	// generate controls whether the operator generates an ECDSA P-256 key pair.
	// Enabled: creates <secretName> (public key) and <secretName>-private (private key)
	// in the MCPGatewayExtension namespace with owner references.
	// Disabled: the secret must already exist (default).
	// Changing this field requires deleting the existing secrets first to ensure
	// the public and private keys are a matching pair.
	// +optional
	// +default="Disabled"
	Generate KeyGenerationPolicy `json:"generate,omitempty"`
}

// MCPGatewayExtensionStatus defines the observed state of MCPGatewayExtension.
type MCPGatewayExtensionStatus struct {
	// conditions represent the current state of the MCPGatewayExtension.
	// The Ready condition indicates whether the broker-router deployment is running
	// and the EnvoyFilter has been successfully applied to the target Gateway.
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Namespaced,shortName=mcpge
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPGatewayExtension extends a Gateway API Gateway to handle the Model Context Protocol (MCP).
// When created, the controller will:
// - Deploy a broker-router Deployment and Service in the MCPGatewayExtension's namespace
// - Create an EnvoyFilter in the Gateway's namespace to route MCP traffic to the broker
// - Configure the Envoy proxy to use the external processor for MCP request handling
//
// The broker aggregates tools from upstream MCP servers registered via MCPServerRegistration
// resources, while the router handles MCP protocol parsing and request routing.
//
// Cross-namespace references to Gateways require a ReferenceGrant in the Gateway's namespace.
type MCPGatewayExtension struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of MCPGatewayExtension
	// +required
	Spec MCPGatewayExtensionSpec `json:"spec,omitzero"`

	// status defines the observed state of MCPGatewayExtension
	// +optional
	Status MCPGatewayExtensionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MCPGatewayExtensionList contains a list of MCPGatewayExtension
type MCPGatewayExtensionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MCPGatewayExtension `json:"items"`
}

// MCPGatewayExtensionTargetReference identifies a Gateway listener to extend with MCP protocol support.
// It follows Gateway API patterns for cross-resource references.
type MCPGatewayExtensionTargetReference struct {
	// group is the group of the target resource.
	// +optional
	// +default="gateway.networking.k8s.io"
	// +kubebuilder:validation:Enum=gateway.networking.k8s.io
	Group string `json:"group,omitempty"`

	// kind is the kind of the target resource.
	// +optional
	// +default="Gateway"
	// +kubebuilder:validation:Enum=Gateway
	Kind string `json:"kind,omitempty"`

	// name is the name of the target resource.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"`

	// namespace of the target resource (optional, defaults to same namespace)
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// sectionName is the name of a listener on the target Gateway. The controller will
	// read the listener's port and hostname to configure the MCP Gateway instance.
	// Only one MCPGatewayExtension is allowed per namespace. MCPGatewayExtensions in
	// different namespaces may target different listeners on the same Gateway, provided
	// those listeners use different ports.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	SectionName string `json:"sectionName,omitempty"`
}

// SetReadyCondition sets the Ready condition on the MCPGatewayExtension status
func (m *MCPGatewayExtension) SetReadyCondition(status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             status,
		ObservedGeneration: m.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// InternalHost returns the internal/private host computed from the targetRef.
// gatewayClassName is used to derive the Service name created by the Gateway controller,
// which follows the convention <gateway-name>-<gatewayClassName>.<namespace>.svc.cluster.local.
func (m *MCPGatewayExtension) InternalHost(port uint32, gatewayClassName string) string {
	if m.Spec.PrivateHost != "" {
		return m.Spec.PrivateHost
	}
	gatewayNamespace := m.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = m.Namespace
	}
	return fmt.Sprintf("%s-%s.%s.svc.cluster.local:%v", m.Spec.TargetRef.Name, gatewayClassName, gatewayNamespace, port)
}

// HTTPRouteDisabled returns true if HTTPRouteManagement is set to Disabled
func (m *MCPGatewayExtension) HTTPRouteDisabled() bool {
	return m.Spec.HTTPRouteManagement == HTTPRouteManagementDisabled
}
