package main

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestControllerSchemeIncludesCustomResourceDefinitions(t *testing.T) {
	if _, _, err := scheme.Scheme.ObjectKinds(&apiextensionsv1.CustomResourceDefinition{}); err != nil {
		t.Fatalf("controller scheme cannot encode CustomResourceDefinition: %v", err)
	}
}
