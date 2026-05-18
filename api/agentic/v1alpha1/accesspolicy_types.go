package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetRefs[0].name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[0].type`
type AccessPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AccessPolicySpec   `json:"spec"`
	Status            AccessPolicyStatus `json:"status,omitempty"`
}

type AccessPolicySpec struct {
	TargetRefs []gwapiv1.LocalPolicyTargetReference `json:"targetRefs"`
	Rules      []AccessRule                         `json:"rules"`
}

type AccessRule struct {
	Source Source   `json:"source"`
	Tools  []string `json:"tools,omitempty"`
}

// +kubebuilder:validation:Enum=OIDC;ServiceAccount;SPIFFE
type AuthorizationSourceType string

const (
	SourceTypeOIDC           AuthorizationSourceType = "OIDC"
	SourceTypeServiceAccount AuthorizationSourceType = "ServiceAccount"
	SourceTypeSPIFFE         AuthorizationSourceType = "SPIFFE"
)

type Source struct {
	Type           AuthorizationSourceType `json:"type"`
	OIDC           *OIDCSource             `json:"oidc,omitempty"`
	ServiceAccount *ServiceAccountSource   `json:"serviceAccount,omitempty"`
	SPIFFE         *SPIFFESource           `json:"spiffe,omitempty"`
}

type OIDCSource struct {
	Issuer string            `json:"issuer"`
	Claims map[string]string `json:"claims,omitempty"`
}

type ServiceAccountSource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type SPIFFESource struct {
	ID string `json:"id"`
}

type AccessPolicyStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type AccessPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AccessPolicy{}, &AccessPolicyList{})
}
