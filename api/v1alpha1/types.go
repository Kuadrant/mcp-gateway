package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcpsrv
// +kubebuilder:printcolumn:name="Prefix",type="string",JSONPath=".spec.toolPrefix",description="Tool prefix for federation"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name",description="Target HTTPRoute"
// +kubebuilder:printcolumn:name="Path",type="string",JSONPath=".spec.path",description="MCP endpoint path"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".status.discoveredTools",description="Number of discovered tools"
// +kubebuilder:printcolumn:name="Credentials",type="string",JSONPath=".spec.credentialRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServer defines a collection of MCP (Model Context Protocol) servers to be aggregated by the gateway.
// It enables discovery and federation of tools from multiple backend MCP servers through HTTPRoute references,
// providing a declarative way to configure which MCP servers should be accessible through the gateway.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer.
// It specifies which HTTPRoutes point to MCP servers and how their tools should be federated.
type MCPServerSpec struct {
	// TargetRef specifies an HTTPRoute that points to a backend MCP server.
	// The referenced HTTPRoute should have backend services that implement the MCP protocol.
	// The controller will discover the backend service from this HTTPRoute and configure
	// the broker to federate tools from that MCP server.
	TargetRef TargetReference `json:"targetRef"`

	// ToolPrefix is the prefix to add to all federated tools from referenced servers.
	// This helps avoid naming conflicts when aggregating tools from multiple sources.
	// For example, if two servers both provide a 'search' tool, prefixes like 'server1_' and 'server2_'
	// ensure they can coexist as 'server1_search' and 'server2_search'.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf || oldSelf == ''",message="toolPrefix is immutable once set"
	ToolPrefix string `json:"toolPrefix,omitempty"`

	// Path specifies the URL path where the MCP server endpoint is exposed.
	// If not specified, defaults to "/mcp".
	// This allows connecting to MCP servers that use custom paths like "/v1/mcp" or "/api/mcp".
	// +optional
	// +kubebuilder:default="/mcp"
	Path string `json:"path,omitempty"`

	// CredentialRef references a Secret containing authentication credentials for the MCP server.
	// The Secret should contain a key with the authentication token or credentials.
	// The controller will aggregate these credentials and make them available to the broker
	// via environment variables following the pattern: KAGENTI_{MCP_NAME}_CRED
	// +optional
	CredentialRef *SecretReference `json:"credentialRef,omitempty"`
}

// TargetReference identifies an HTTPRoute that points to MCP servers.
// It follows Gateway API patterns for cross-resource references.
type TargetReference struct {
	// Group is the group of the target resource.
	// +kubebuilder:default=gateway.networking.k8s.io
	// +kubebuilder:validation:Enum=gateway.networking.k8s.io
	Group string `json:"group"`

	// Kind is the kind of the target resource.
	// +kubebuilder:default=HTTPRoute
	// +kubebuilder:validation:Enum=HTTPRoute
	Kind string `json:"kind"`

	// Name is the name of the target resource.
	Name string `json:"name"`

	// Namespace of the target resource (optional, defaults to same namespace)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SecretReference identifies a Secret containing credentials for MCP server authentication.
type SecretReference struct {
	// Name is the name of the Secret resource.
	Name string `json:"name"`

	// Key is the key within the Secret that contains the credential value.
	// If not specified, defaults to "token".
	// +kubebuilder:default=token
	// +optional
	Key string `json:"key,omitempty"`
}

// MCPServerStatus represents the observed state of the MCPServer resource.
// It contains conditions that indicate whether the referenced servers have been
// successfully discovered and are ready for use.
type MCPServerStatus struct {
	// Conditions represent the latest available observations of the MCPServer's state.
	// Common conditions include 'Ready' to indicate if all referenced servers are accessible.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DiscoveredTools is the number of tools discovered from this MCPServer
	// +optional
	DiscoveredTools int `json:"discoveredTools,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=mcpvs
// +kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".spec.tools.length()"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPVirtualServer defines a virtual server that exposes a specific set of tools.
// It enables tool-level access control and federation by specifying which tools
// should be accessible through this virtual endpoint.
type MCPVirtualServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MCPVirtualServerSpec `json:"spec,omitempty"`
}

// MCPVirtualServerSpec defines the desired state of MCPVirtualServer.
// It specifies which tools should be exposed by this virtual server.
type MCPVirtualServerSpec struct {
	// Description provides a human-readable description of this virtual server's purpose.
	// +optional
	Description string `json:"description,omitempty"`

	// Tools specifies the list of tool names to expose through this virtual server.
	// These tools must be available from the underlying MCP servers configured in the system.
	// +kubebuilder:validation:MinItems=1
	Tools []string `json:"tools"`
}

// +kubebuilder:object:root=true

// MCPVirtualServerList contains a list of MCPVirtualServer
type MCPVirtualServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPVirtualServer `json:"items"`
}
