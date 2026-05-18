package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.mcp.serviceName`
type Backend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackendSpec   `json:"spec"`
	Status            BackendStatus `json:"status,omitempty"`
}

type BackendSpec struct {
	// +kubebuilder:validation:Enum=MCP
	Type BackendType `json:"type"`
	MCP  *MCPBackend `json:"mcp,omitempty"`
}

type BackendType string

const (
	BackendTypeMCP BackendType = "MCP"
)

type MCPBackend struct {
	ServiceName string `json:"serviceName,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	// +kubebuilder:default=8080
	Port int32 `json:"port"`
	// +kubebuilder:default="/mcp"
	Path string `json:"path,omitempty"`
}

type BackendStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type BackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backend{}, &BackendList{})
}
