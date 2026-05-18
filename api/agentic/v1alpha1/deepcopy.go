package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (in *Backend) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *BackendList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *Backend) DeepCopy() *Backend {
	if in == nil {
		return nil
	}
	out := new(Backend)
	in.DeepCopyInto(out)
	return out
}

func (in *Backend) DeepCopyInto(out *Backend) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *BackendSpec) DeepCopyInto(out *BackendSpec) {
	*out = *in
	if in.MCP != nil {
		in, out := &in.MCP, &out.MCP
		*out = new(MCPBackend)
		**out = **in
	}
}

func (in *BackendStatus) DeepCopyInto(out *BackendStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *BackendList) DeepCopy() *BackendList {
	if in == nil {
		return nil
	}
	out := new(BackendList)
	in.DeepCopyInto(out)
	return out
}

func (in *BackendList) DeepCopyInto(out *BackendList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]Backend, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *AccessPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AccessPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AccessPolicy) DeepCopy() *AccessPolicy {
	if in == nil {
		return nil
	}
	out := new(AccessPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *AccessPolicy) DeepCopyInto(out *AccessPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *AccessPolicySpec) DeepCopyInto(out *AccessPolicySpec) {
	*out = *in
	if in.TargetRefs != nil {
		in, out := &in.TargetRefs, &out.TargetRefs
		*out = make([]gwapiv1.LocalPolicyTargetReference, len(*in))
		copy(*out, *in)
	}
	if in.Rules != nil {
		in, out := &in.Rules, &out.Rules
		*out = make([]AccessRule, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *AccessRule) DeepCopyInto(out *AccessRule) {
	*out = *in
	in.Source.DeepCopyInto(&out.Source)
	if in.Tools != nil {
		in, out := &in.Tools, &out.Tools
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

func (in *Source) DeepCopyInto(out *Source) {
	*out = *in
	if in.OIDC != nil {
		in, out := &in.OIDC, &out.OIDC
		*out = new(OIDCSource)
		(*in).DeepCopyInto(*out)
	}
	if in.ServiceAccount != nil {
		in, out := &in.ServiceAccount, &out.ServiceAccount
		*out = new(ServiceAccountSource)
		**out = **in
	}
	if in.SPIFFE != nil {
		in, out := &in.SPIFFE, &out.SPIFFE
		*out = new(SPIFFESource)
		**out = **in
	}
}

func (in *OIDCSource) DeepCopyInto(out *OIDCSource) {
	*out = *in
	if in.Claims != nil {
		in, out := &in.Claims, &out.Claims
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
}

func (in *AccessPolicyStatus) DeepCopyInto(out *AccessPolicyStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *AccessPolicyList) DeepCopy() *AccessPolicyList {
	if in == nil {
		return nil
	}
	out := new(AccessPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *AccessPolicyList) DeepCopyInto(out *AccessPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]AccessPolicy, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}
