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

type TargetRef struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

type CELAuthorization struct {
	Expression string `json:"expression"`
}

type Authorization struct {
	Type string           `json:"type"`
	CEL  CELAuthorization `json:"cel,omitempty"`
}

type Rule struct {
	Name          string        `json:"name"`
	Authorization Authorization `json:"authorization"`
}

// XAccessPolicySpec defines the desired state of XAccessPolicy
type XAccessPolicySpec struct {
	TargetRefs []TargetRef `json:"targetRefs"`
	Rules      []Rule      `json:"rules"`
}

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
	Spec XAccessPolicySpec `json:"spec"`

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
