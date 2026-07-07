// Package v1 contains API Schema definitions for the mcp v1 API group.
// +kubebuilder:object:generate=true
// +groupName=mcp.kuadrant.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "mcp.kuadrant.io", Version: "v1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&MCPServerRegistration{},
		&MCPServerRegistrationList{},
		&MCPVirtualServer{},
		&MCPVirtualServerList{},
		&MCPGatewayExtension{},
		&MCPGatewayExtensionList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
