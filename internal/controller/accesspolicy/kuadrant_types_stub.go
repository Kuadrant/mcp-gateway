package accesspolicy

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type AuthPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AuthPolicySpec `json:"spec,omitempty"`
}

type AuthPolicySpec struct {
	TargetRef  gwapiv1.LocalPolicyTargetReference `json:"targetRef"`
	AuthScheme AuthScheme                         `json:"authScheme,omitempty"`
}

type AuthScheme struct {
	Identity      map[string]Identity          `json:"identity,omitempty"`
	Authorization map[string]AuthorizationRule `json:"authorization,omitempty"`
}

type Identity struct {
	OIDC *OIDCIdentity `json:"oidc,omitempty"`
}

type OIDCIdentity struct {
	Endpoint string `json:"endpoint"`
}

type AuthorizationRule struct {
	PatternMatching *PatternMatchingRule `json:"patternMatching,omitempty"`
}

type PatternMatchingRule struct {
	Patterns []PatternExpression `json:"patterns"`
}

type PatternExpression struct {
	Predicate string `json:"predicate,omitempty"`
	Selector  string `json:"selector,omitempty"`
	Operator  string `json:"operator,omitempty"`
	Value     string `json:"value,omitempty"`
}

type AuthPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuthPolicy `json:"items"`
}

func (in *AuthPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AuthPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AuthPolicy) DeepCopy() *AuthPolicy {
	if in == nil {
		return nil
	}
	out := new(AuthPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *AuthPolicy) DeepCopyInto(out *AuthPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *AuthPolicySpec) DeepCopyInto(out *AuthPolicySpec) {
	*out = *in
	out.TargetRef = in.TargetRef
	in.AuthScheme.DeepCopyInto(&out.AuthScheme)
}

func (in *AuthScheme) DeepCopyInto(out *AuthScheme) {
	*out = *in
	if in.Identity != nil {
		in, out := &in.Identity, &out.Identity
		*out = make(map[string]Identity, len(*in))
		for key, val := range *in {
			(*out)[key] = *val.DeepCopy()
		}
	}
	if in.Authorization != nil {
		in, out := &in.Authorization, &out.Authorization
		*out = make(map[string]AuthorizationRule, len(*in))
		for key, val := range *in {
			(*out)[key] = *val.DeepCopy()
		}
	}
}

func (in *Identity) DeepCopy() *Identity {
	if in == nil {
		return nil
	}
	out := new(Identity)
	if in.OIDC != nil {
		in, out := &in.OIDC, &out.OIDC
		*out = new(OIDCIdentity)
		**out = **in
	}
	return out
}

func (in *AuthorizationRule) DeepCopy() *AuthorizationRule {
	if in == nil {
		return nil
	}
	out := new(AuthorizationRule)
	if in.PatternMatching != nil {
		in, out := &in.PatternMatching, &out.PatternMatching
		*out = new(PatternMatchingRule)
		(*in).DeepCopyInto(*out)
	}
	return out
}

func (in *PatternMatchingRule) DeepCopyInto(out *PatternMatchingRule) {
	*out = *in
	if in.Patterns != nil {
		in, out := &in.Patterns, &out.Patterns
		*out = make([]PatternExpression, len(*in))
		copy(*out, *in)
	}
}

func (in *AuthPolicyList) DeepCopy() *AuthPolicyList {
	if in == nil {
		return nil
	}
	out := new(AuthPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *AuthPolicyList) DeepCopyInto(out *AuthPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]AuthPolicy, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}
