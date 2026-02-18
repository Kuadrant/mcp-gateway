package v1alpha1

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

	// AnnotationPublicHost overrides the public host for the MCP Gateway broker-router. Note this a temporary annotation and you should expect it to be removed in a future release
	AnnotationPublicHost = "kuadrant.io/alpha-gateway-public-host"
	// AnnotationPollInterval overrides how often the broker pings upstream MCP servers. Note this a temporary annotation and you should expect it to be removed in a future release
	AnnotationPollInterval = "kuadrant.io/alpha-gateway-poll-interval"
	// AnnotationListenerPort specifies the Gateway listener port for the EnvoyFilter to target. Note this a temporary annotation and you should expect it to be removed in a future release
	AnnotationListenerPort = "kuadrant.io/alpha-gateway-listener-port"

	// DefaultListenerPort is the default port used when no annotation is specified
	DefaultListenerPort = 8080
)

// MCPGatewayExtensionSpec defines the desired state of MCPGatewayExtension.
type MCPGatewayExtensionSpec struct {
	// TargetRef specifies the Gateway to extend with MCP protocol support.
	// The controller will create an EnvoyFilter targeting this Gateway's Envoy proxy.
	TargetRef MCPGatewayExtensionTargetReference `json:"targetRef"`
}

// MCPGatewayExtensionStatus defines the observed state of MCPGatewayExtension.
type MCPGatewayExtensionStatus struct {
	// Conditions represent the current state of the MCPGatewayExtension.
	// The Ready condition indicates whether the broker-router deployment is running
	// and the EnvoyFilter has been successfully applied to the target Gateway.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
	Spec MCPGatewayExtensionSpec `json:"spec"`

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

// MCPGatewayExtensionTargetReference identifies an HTTPRoute that points to MCP servers.
// It follows Gateway API patterns for cross-resource references.
type MCPGatewayExtensionTargetReference struct {
	// Group is the group of the target resource.
	// +kubebuilder:default=gateway.networking.k8s.io
	// +kubebuilder:validation:Enum=gateway.networking.k8s.io
	Group string `json:"group"`

	// Kind is the kind of the target resource.
	// +kubebuilder:default=Gateway
	// +kubebuilder:validation:Enum=Gateway
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	Name string `json:"name"`

	// Namespace of the target resource (optional, defaults to same namespace)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

func init() {
	SchemeBuilder.Register(&MCPGatewayExtension{}, &MCPGatewayExtensionList{})
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

// PublicHost returns the public host override from annotations, or empty string if not set
func (m *MCPGatewayExtension) PublicHost() string {
	if m.Annotations == nil {
		return ""
	}
	return m.Annotations[AnnotationPublicHost]
}

// InternalHost returns the internal/private host computed from the targetRef
func (m *MCPGatewayExtension) InternalHost() string {
	gatewayNamespace := m.Spec.TargetRef.Namespace
	if gatewayNamespace == "" {
		gatewayNamespace = m.Namespace
	}
	return m.Spec.TargetRef.Name + "-istio." + gatewayNamespace + ".svc.cluster.local:8080"
}

// PollInterval returns the upstream MCP server ping interval from annotations, or empty string if not set
func (m *MCPGatewayExtension) PollInterval() string {
	if m.Annotations == nil {
		return ""
	}
	return m.Annotations[AnnotationPollInterval]
}

// ListenerPort returns the Gateway listener port from annotations, or DefaultListenerPort if not set or invalid
func (m *MCPGatewayExtension) ListenerPort() uint32 {
	if m.Annotations == nil {
		return DefaultListenerPort
	}
	portStr, ok := m.Annotations[AnnotationListenerPort]
	if !ok || portStr == "" {
		return DefaultListenerPort
	}
	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return DefaultListenerPort
	}
	return uint32(port)
}
