package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcpsr
// +kubebuilder:printcolumn:name="Prefix",type="string",JSONPath=".spec.prefix",description="Prefix for federation"
// +kubebuilder:printcolumn:name="Target",type="string",JSONPath=".spec.targetRef.name",description="Target HTTPRoute.  MCP Gateway only supports routes with a single BackendRef"
// +kubebuilder:printcolumn:name="Path",type="string",JSONPath=".spec.path",description="MCP endpoint path"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".status.discoveredTools",description="Number of discovered tools"
// +kubebuilder:printcolumn:name="Category",type="string",JSONPath=".spec.category",description="Server categories for discovery"
// +kubebuilder:printcolumn:name="Credentials",type="string",JSONPath=".spec.credentialRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServerRegistration registers an upstream MCP server for tool federation through the gateway.
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

	// prefix is the prefix to add to all federated capabilities from referenced servers.
	// This helps avoid naming conflicts when aggregating tools from multiple sources.
	// For example, if two servers both provide a 'search' tool, prefixes like 'server1_' and 'server2_' ensure they can coexist as 'server1_search' and 'server2_search'.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="prefix is immutable once set"
	Prefix string `json:"prefix,omitempty"`

	// path specifies the URL path where the MCP server endpoint is exposed.
	// If not specified, defaults to "/mcp".
	// This allows connecting to MCP servers that use custom paths like "/v1/mcp" or "/api/mcp".
	// +optional
	// +default="/mcp"
	Path string `json:"path,omitempty"`

	// credentialRef references a Secret containing authentication credentials for the MCP server.
	// The Secret should contain a key with the authentication token or credentials.
	// The controller will aggregate these credentials and make them available to the broker via environment variables following the pattern: KAGENTI_{MCP_NAME}_CRED
	// Used exclusively by the broker for tool discovery and session management. Never injected into client tools/call requests.
	// +optional
	CredentialRef *SecretReference `json:"credentialRef,omitempty"`

	// tokenURLElicitation enables per-user token collection via URL elicitation.
	// When set, the router uses the MCP spec's URLElicitationRequiredError (-32042) flow
	// to collect tokens from capable clients at tool-call time.
	// +optional
	TokenURLElicitation *TokenURLElicitationConfig `json:"tokenURLElicitation,omitempty"`

	// category assigns one or more categories to this MCP server for tool discovery.
	// Used by the discover_tools meta-tool to allow agents to filter servers by category.
	// +optional
	// +listType=atomic
	// +default=["uncategorised"]
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	Category []string `json:"category,omitempty"`

	// hint provides a short description of what this MCP server offers.
	// Returned by the discover_tools meta-tool to help agents decide which tools to select.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Hint string `json:"hint,omitempty"`
}

// TokenURLElicitationConfig configures per-user token collection via URL elicitation.
type TokenURLElicitationConfig struct {
	// url overrides the default broker token page URL.
	// When set, users are directed to this external URL (e.g. a Vault UI) instead of the broker's built-in page.
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url,omitempty"`
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

	// prompts specifies the list of prompt names to expose through this virtual server.
	// When omitted, all prompts are exposed.
	// +optional
	// +listType=atomic
	Prompts []string `json:"prompts,omitempty"`
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
