/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// LocalPolicyTargetReference identifies a target resource within the local namespace.
type LocalPolicyTargetReference struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

// LocalPolicyTargetReferenceWithSectionName identifies a target resource and optionally a specific section.
type LocalPolicyTargetReferenceWithSectionName struct {
	LocalPolicyTargetReference `json:",inline"`
	SectionName                *string `json:"sectionName,omitempty"`
}

type AccessPolicySpec struct {
	TargetRefs   []LocalPolicyTargetReferenceWithSectionName `json:"targetRefs"`
	Action       AccessPolicyActionType                      `json:"action"`
	ExternalAuth *runtime.RawExtension                       `json:"externalAuth,omitempty"`
	Rules        []AccessRule                                `json:"rules,omitempty"`
}

type AccessPolicyActionType string

const (
	ActionTypeAllow        AccessPolicyActionType = "Allow"
	ActionTypeExternalAuth AccessPolicyActionType = "ExternalAuth"
)

type MCPBaseProtocolMethodsOption string

const (
	MCPBaseProtocolMethodsOptionSkip  MCPBaseProtocolMethodsOption = "SKIP_BASE_PROTOCOL_METHODS"
	MCPBaseProtocolMethodsOptionMatch MCPBaseProtocolMethodsOption = "MATCH_BASE_PROTOCOL_METHODS"
)

type AccessRule struct {
	Name          string             `json:"name"`
	Source        AccessRuleSource   `json:"source"`
	Authorization *AuthorizationRule `json:"authorization,omitempty"`
}

type AccessRuleSource struct {
	Type           AuthorizationSourceType            `json:"type"`
	SPIFFE         *AuthorizationSourceSPIFFE         `json:"spiffe,omitempty"`
	ServiceAccount *AuthorizationSourceServiceAccount `json:"serviceAccount,omitempty"`
}

type AuthorizationSourceType string

const (
	AuthorizationSourceTypeSPIFFE         AuthorizationSourceType = "SPIFFE"
	AuthorizationSourceTypeServiceAccount AuthorizationSourceType = "ServiceAccount"
)

type AuthorizationSourceSPIFFE string

type AuthorizationSourceServiceAccount struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type AuthorizationRule struct {
	Type AuthorizationRuleType `json:"type"`
	MCP  MCPAttributes         `json:"mcp,omitempty"`
	CEL  *AccessPolicyCELRule  `json:"cel,omitempty"`
}

type AccessPolicyCELRule struct {
	Expression string `json:"expression"`
}

type MCPAttributes struct {
	Methods                      []MCPMethod                  `json:"methods,omitempty"`
	MCPBaseProtocolMethodsOption MCPBaseProtocolMethodsOption `json:"mcpBaseProtocolMethodsOption,omitempty"`
}

type MCPMethod struct {
	Name   MCPMethodName    `json:"name"`
	Params []MCPMethodParam `json:"params,omitempty"`
}

type MCPMethodParam string

type MCPMethodName string

type AuthorizationRuleType string

const (
	AuthorizationRuleTypeInline AuthorizationRuleType = "Inline"
	AuthorizationRuleTypeCEL    AuthorizationRuleType = "CEL"
)

const (
	PolicyConditionAccepted     = "Accepted"
	PolicyConditionResolvedRefs = "ResolvedRefs"
	PolicyConditionProgrammed   = "Programmed"

	PolicyReasonInvalidCEL      = "InvalidCEL"
	PolicyReasonInvalidTarget   = "InvalidTarget"
	PolicyReasonGatewayNotFound = "GatewayNotFound"
	PolicyReasonAccepted        = "Accepted"
	PolicyReasonProgrammed      = "Programmed"
	PolicyReasonResolved        = "Resolved"
)

// XAccessPolicyStatus defines the observed state of XAccessPolicy.
type XAccessPolicyStatus struct {
	// conditions represent the current state of the XAccessPolicy resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=".status.conditions[?(@.type=='Accepted')].status"
// +kubebuilder:printcolumn:name="ResolvedRefs",type=string,JSONPath=".status.conditions[?(@.type=='ResolvedRefs')].status"
// +kubebuilder:printcolumn:name="Programmed",type=string,JSONPath=".status.conditions[?(@.type=='Programmed')].status"

// XAccessPolicy is the Schema for the xaccesspolicies API
type XAccessPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of XAccessPolicy
	// +required
	Spec AccessPolicySpec `json:"spec"`

	// status defines the observed state of XAccessPolicy
	// +optional
	Status XAccessPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// XAccessPolicyList contains a list of XAccessPolicy
type XAccessPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []XAccessPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &XAccessPolicy{}, &XAccessPolicyList{})
		return nil
	})
}
