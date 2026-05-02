package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcpsr
// +kubebuilder:printcolumn:name="Prefix",type="string",JSONPath=".spec.toolPrefix",description="Tool prefix for federation"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name",description="Target HTTPRoute.  MCP Gateway only supports routes with a single BackendRef"
// +kubebuilder:printcolumn:name="Path",type="string",JSONPath=".spec.path",description="MCP endpoint path"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".status.discoveredTools",description="Number of discovered tools"
// +kubebuilder:printcolumn:name="Credentials",type="string",JSONPath=".spec.credentialRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServerRegistration defines a collection of MCP (Model Context Protocol) servers to be aggregated by the gateway.
// It enables discovery and federation of tools from multiple backend MCP servers through HTTPRoute references, providing a declarative way to configure which MCP servers should be accessible through the gateway.
type MCPServerRegistration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of MCPServerRegistration.
	// +optional
	Spec MCPServerRegistrationSpec `json:"spec,omitempty"`

	// status defines the observed state of MCPServerRegistration.
	// +optional
	Status MCPServerRegistrationStatus `json:"status,omitempty"`
}

// MCPServerRegistrationSpec defines the desired state of MCPServerRegistration.
// It specifies which HTTPRoutes point to MCP servers and how their tools should be federated.
type MCPServerRegistrationSpec struct {
	// targetRef specifies an HTTPRoute that points to a backend MCP server.
	// The referenced HTTPRoute should have a backend service that implements the MCP protocol.
	// The controller will discover the backend service from this HTTPRoute and configure
	// the broker to federate tools from that MCP server.
	// +required
	TargetRef TargetReference `json:"targetRef,omitzero"`

	// toolPrefix is the prefix to add to all federated tools from referenced servers.
	// This helps avoid naming conflicts when aggregating tools from multiple sources.
	// For example, if two servers both provide a 'search' tool, prefixes like 'server1_' and 'server2_' ensure they can coexist as 'server1_search' and 'server2_search'.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="toolPrefix is immutable once set"
	ToolPrefix string `json:"toolPrefix,omitempty"`

	// path specifies the URL path where the MCP server endpoint is exposed.
	// If not specified, defaults to "/mcp".
	// This allows connecting to MCP servers that use custom paths like "/v1/mcp" or "/api/mcp".
	// +optional
	// +default="/mcp"
	Path string `json:"path,omitempty"`

	// credentialRef references a Secret containing authentication credentials for the MCP server.
	// The Secret should contain a key with the authentication token or credentials.
	// The controller will aggregate these credentials and make them available to the broker via environment variables following the pattern: KAGENTI_{MCP_NAME}_CRED
	// +optional
	CredentialRef *SecretReference `json:"credentialRef,omitempty"`

	// timeouts configures upstream request timeouts for tool calls routed to this server.
	// When omitted, no gateway-enforced timeout is applied and tool calls inherit the
	// gateway's underlying transport defaults.
	// +optional
	Timeouts *MCPServerTimeouts `json:"timeouts,omitempty"`
}

// MCPServerTimeouts configures gateway-enforced execution timeouts for an MCP server.
// Per-tool overrides take precedence over the server-wide default.
type MCPServerTimeouts struct {
	// toolCall is the default timeout applied to every tools/call routed to this server.
	// The value is a Go-style duration string (for example "10s", "500ms", "1m30s").
	// Must be greater than zero. When unset, no default tool-call timeout is applied.
	// +optional
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	ToolCall string `json:"toolCall,omitempty"`

	// perTool is a list of overrides for individual tools. The "name" field must match
	// the upstream tool name as advertised by the backend MCP server (i.e. without the
	// MCPServerRegistration's toolPrefix). Per-tool overrides win over `toolCall`.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=256
	PerTool []ToolTimeout `json:"perTool,omitempty"`
}

// ToolTimeout overrides the server-wide tool-call timeout for a specific tool.
type ToolTimeout struct {
	// name of the upstream tool (the unprefixed name as reported by tools/list).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`

	// toolCall is the timeout applied when this tool is invoked. Format is a Go-style
	// duration string (for example "30s", "2m"). Must be greater than zero.
	// +required
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	ToolCall string `json:"toolCall"`
}

// TargetReference identifies an HTTPRoute that points to MCP servers.
// It follows Gateway API patterns for cross-resource references.
type TargetReference struct {
	// group is the group of the target resource.
	// +optional
	// +default="gateway.networking.k8s.io"
	// +kubebuilder:validation:Enum=gateway.networking.k8s.io
	Group string `json:"group,omitempty"`

	// kind is the kind of the target resource.
	// +optional
	// +default="HTTPRoute"
	// +kubebuilder:validation:Enum=HTTPRoute
	Kind string `json:"kind,omitempty"`

	// name is the name of the target resource.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"`

	// namespace of the target resource (optional, defaults to same namespace).
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SecretReference identifies a Secret containing credentials for MCP server authentication.
type SecretReference struct {
	// name is the name of the Secret resource.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"`

	// key is the key within the Secret that contains the credential value.
	// If not specified, defaults to "token".
	// +optional
	// +default="token"
	Key string `json:"key,omitempty"`
}

// MCPServerRegistrationStatus represents the observed state of the MCPServerRegistration resource.
// It contains conditions that indicate whether the referenced servers have been successfully discovered and are ready for use.
type MCPServerRegistrationStatus struct {
	// conditions represent the latest available observations of the MCPServerRegistration's state.
	// Common conditions include 'Ready' to indicate if all referenced servers are accessible.
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// discoveredTools is the number of tools discovered from this MCPServerRegistration.
	// +optional
	DiscoveredTools int32 `json:"discoveredTools,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerRegistrationList contains a list of MCPServerRegistration
type MCPServerRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServerRegistration `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=mcpvs
// +kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".spec.tools.length()"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPVirtualServer defines a virtual server that exposes a specific set of tools.
// It enables tool-level access control and federation by specifying which tools
// should be accessible through this virtual endpoint.
type MCPVirtualServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of MCPVirtualServer.
	// +optional
	Spec MCPVirtualServerSpec `json:"spec,omitempty"`
}

// MCPVirtualServerSpec defines the desired state of MCPVirtualServer.
// It specifies which tools should be exposed by this virtual server.
type MCPVirtualServerSpec struct {
	// description provides a human-readable description of this virtual server's purpose.
	// +optional
	Description string `json:"description,omitempty"`

	// tools specifies the list of tool names to expose through this virtual server.
	// These tools must be available from the underlying MCP servers configured in the system.
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	Tools []string `json:"tools,omitempty"`
}

// +kubebuilder:object:root=true

// MCPVirtualServerList contains a list of MCPVirtualServer
type MCPVirtualServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPVirtualServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServerRegistration{}, &MCPServerRegistrationList{}, &MCPVirtualServer{}, &MCPVirtualServerList{})
}
