package v1alpha1

import (
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

	// AnnotationPublicHost overrides the public host for the MCP Gateway broker-router
	AnnotationPublicHost = "kuadrant.io/alpha-gateway-public-host"
	// AnnotationPollInterval overrides the broker poll interval for config changes
	AnnotationPollInterval = "kuadrant.io/alpha-gateway-poll-interval"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"

// MCPGatewayExtensionSpec defines the desired state of MCPGatewayExtension
type MCPGatewayExtensionSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// TargetRef specifies a Gateway that should be extended to handle the MCP Protocol.
	TargetRef MCPGatewayExtensionTargetReference `json:"targetRef"`
}

// MCPGatewayExtensionStatus defines the observed state of MCPGatewayExtension.
type MCPGatewayExtensionStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the MCPGatewayExtension resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// MCPGatewayExtension is the Schema for the mcpgatewayextensions API
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

// PollInterval returns the poll interval override from annotations, or empty string if not set
func (m *MCPGatewayExtension) PollInterval() string {
	if m.Annotations == nil {
		return ""
	}
	return m.Annotations[AnnotationPollInterval]
}
